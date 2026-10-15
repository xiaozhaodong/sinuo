package main

import (
	"fmt"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"sinuo/internal/tray"
)

const (
	// healthRefreshInterval：账号冷却、鉴权失效等没有变化事件，今日花费也要等用量落库，
	// 一段请求结束（进行中归零）后补算一次图标状态与统计；两次补算至少间隔这么久，避免连续工具调用时频繁读库。
	healthRefreshInterval = 10 * time.Second
	// traySettleDelay：请求结束后等这么久再补算。用量记录按批写库（内置配置 5 秒一批），太早读不到这次的花费。
	traySettleDelay = 6 * time.Second
)

// trayIcon 管理菜单栏图标：静态三态与请求进行中的流光，由 iconLoop 串行驱动。
// animator、active、settleAt 与 healthAt 只由 iconLoop（stepIcon）读写；mu 只保护与菜单重建共享的状态。
type trayIcon struct {
	setter   tray.IconSetter
	mac      bool
	motion   func() tray.Motion // 为空时用 appTray.motion；测试可替换
	wake     chan struct{}
	animator tray.Animator
	active   bool      // 上一步是否有请求在跑
	settleAt time.Time // 待补算的时刻；零值表示没有
	healthAt time.Time
	running  atomic.Bool // 最近一次快照中代理是否运行，用于识别启动 / 停止
	mu       sync.Mutex
	status   tray.IconStatus
	applied  string // 上次下发的「状态/帧」，避免重复设置同一张图
}

func newTrayIcon(setter tray.IconSetter) trayIcon {
	return trayIcon{setter: setter, mac: runtime.GOOS == "darwin", wake: make(chan struct{}, 1)}
}

// iconLoop 在有动画时按帧唤醒，没有请求时只等活动变化，不占用资源。
func (t *appTray) iconLoop() {
	timer := time.NewTimer(time.Hour)
	timer.Stop()
	defer timer.Stop()
	for {
		select {
		case <-t.done:
			return
		case <-t.icon.wake:
		case <-timer.C:
		}
		wait := t.stepIcon(time.Now())
		timer.Stop()
		if wait > 0 {
			timer.Reset(wait)
		}
	}
}

func (t *appTray) stepIcon(now time.Time) time.Duration {
	motion := t.icon.motion
	if motion == nil {
		motion = t.motion
	}
	active := t.activeCount() > 0
	if t.icon.active && !active && t.icon.settleAt.IsZero() {
		// 一段请求刚结束（与动画是否开启无关）：等用量落库后补算，且与上次补算至少间隔 healthRefreshInterval
		t.icon.settleAt = now.Add(traySettleDelay)
		if next := t.icon.healthAt.Add(healthRefreshInterval); next.After(t.icon.settleAt) {
			t.icon.settleAt = next
		}
	}
	t.icon.active = active
	t.icon.animator.SetActive(active, now)
	frame, wait := t.icon.animator.Advance(now, motion)
	t.icon.mu.Lock()
	state := t.icon.status.State
	t.icon.mu.Unlock()
	t.applyIcon(state, frame)
	return t.settle(now, wait)
}

// settle 到点时让统计缓存失效并重建菜单（顺带补算降级状态与菜单栏文字）；没到点就把唤醒间隔缩到补算时刻。
func (t *appTray) settle(now time.Time, wait time.Duration) time.Duration {
	if t.icon.settleAt.IsZero() {
		return wait
	}
	if left := t.icon.settleAt.Sub(now); left > 0 {
		if wait == 0 || left < wait {
			return left
		}
		return wait
	}
	t.icon.settleAt, t.icon.healthAt = time.Time{}, now
	t.statsStale.Store(true)
	t.signal(t.refresh)
	return wait
}

// motion 只在起播时调用：读设置与系统「减少动态效果」，每段请求读一次。
func (t *appTray) motion() tray.Motion {
	if runtime.GOOS != "darwin" {
		return tray.MotionOff
	}
	// 设置读取失败时按默认开启处理
	if enabled, _ := t.app.readTraySetting("request_animation", true); !enabled {
		return tray.MotionOff
	}
	if trayReduceMotion() {
		return tray.MotionStill
	}
	return tray.MotionFlow
}

func (t *appTray) applyIcon(state tray.IconState, frame int) {
	png, template := tray.IconFrame(state, frame, t.icon.mac)
	key := fmt.Sprintf("%d/%d", state, frame)
	if !template {
		key = "color" // 非 macOS 只有一张彩色图标
	}
	t.icon.mu.Lock()
	same := t.icon.applied == key
	t.icon.applied = key
	t.icon.mu.Unlock()
	if same || t.closed.Load() || t.icon.setter == nil {
		return
	}
	t.icon.setter.SetFrame(png, template)
}

// setIconStatus 由菜单重建调用；状态变化时立即换图（动画中则换成对应的帧组）。
func (t *appTray) setIconStatus(snapshot tray.Snapshot) tray.IconStatus {
	status := tray.EvaluateIcon(snapshot)
	t.icon.running.Store(snapshot.Running)
	t.icon.mu.Lock()
	changed := t.icon.status != status
	t.icon.status = status
	t.icon.mu.Unlock()
	if changed {
		t.signal(t.icon.wake)
	}
	return status
}

// notifyTrayRunning 由每 2 秒的状态推送调用：只有代理运行状态与托盘所知不同时才触发重建。
func (a *App) notifyTrayRunning(running bool) {
	if a.tray != nil && a.tray.icon.running.Load() != running {
		a.tray.signal(a.tray.refresh)
	}
}

func trayTooltip(statusLabel string, status tray.IconStatus) string {
	tooltip := "Sinuo · " + statusLabel
	if status.Reason != "" {
		tooltip += " · " + status.Reason
	}
	return tooltip
}
