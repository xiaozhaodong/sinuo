package tray

import "time"

// 流光节奏与 docs/prototypes/tray-icon-animation.html 方案 A 的场景模拟一致。
const (
	FrameInterval = time.Second / 12
	// StartDelay：请求持续这么久才开始播，count_tokens 一类的瞬时请求不触发
	StartDelay = 300 * time.Millisecond
	// TailDelay：最后一个请求结束后再等这么久才收尾，工具调用之间的空隙不会闪停
	TailDelay = 600 * time.Millisecond
	// StillFrame：系统「减少动态效果」时固定显示的一帧，高亮停在 S 中段
	StillFrame = FlowFrameCount * 45 / 100
)

// Motion 是起播时决定的播放方式。
type Motion int

const (
	MotionOff   Motion = iota // 不播（设置关闭或非 macOS），图标保持静态
	MotionStill               // 固定一帧，不逐帧切换
	MotionFlow                // 逐帧流光
)

// Animator 是请求进行中流光动画的纯状态机；时间由调用方传入，便于测试。
// 非并发安全，由调用方串行驱动。
type Animator struct {
	active, playing, stopping, still bool
	frame                            int
	changedAt                        time.Time // 最近一次进入 / 离开「有请求」的时刻
}

// SetActive 记录是否有请求在进行。
func (a *Animator) SetActive(active bool, now time.Time) {
	if active != a.active {
		a.active, a.changedAt = active, now
	}
	if active {
		a.stopping = false
	}
}

func (a *Animator) Playing() bool { return a.playing }

// Advance 推进到 now。frame < 0 表示显示静态图标；wait 是下次需要唤醒的间隔，0 表示等下一次
// SetActive 变化即可。motion 只在起播时调用一次。
func (a *Animator) Advance(now time.Time, motion func() Motion) (frame int, wait time.Duration) {
	if !a.playing {
		return a.tryStart(now, motion)
	}
	if !a.active && !a.stopping {
		idle := now.Sub(a.changedAt)
		if idle >= TailDelay {
			a.stopping = true
		} else if a.still {
			return StillFrame, TailDelay - idle
		}
	}
	if a.still {
		if a.stopping {
			return a.stop()
		}
		return StillFrame, 0
	}
	a.frame = (a.frame + 1) % FlowFrameCount
	// 收尾时播完当前周期，回到第 0 帧（只有底色、尚无高亮）再换回静态图标
	if a.stopping && a.frame == 0 {
		return a.stop()
	}
	return a.frame, FrameInterval
}

func (a *Animator) tryStart(now time.Time, motion func() Motion) (int, time.Duration) {
	if !a.active {
		return -1, 0
	}
	if elapsed := now.Sub(a.changedAt); elapsed < StartDelay {
		return -1, StartDelay - elapsed
	}
	m := motion()
	if m == MotionOff {
		return -1, 0
	}
	a.playing, a.stopping, a.still, a.frame = true, false, m == MotionStill, 0
	if a.still {
		return StillFrame, 0
	}
	return 0, FrameInterval
}

func (a *Animator) stop() (int, time.Duration) {
	a.playing, a.stopping, a.still, a.frame = false, false, false, 0
	return -1, 0
}
