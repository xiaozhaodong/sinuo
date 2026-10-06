package main

import (
	"context"
	"fmt"
	"log/slog"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"cc-forwarder/internal/proxy"
	"cc-forwarder/internal/tray"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/services/notifications"
)

const (
	// trayFailoverQueue：故障转移回调在请求路径上，只入队不等待；队列满时丢弃（节流后本来也只会发一条）。
	trayFailoverQueue = 64
	// noticeDefaultAction 是用户点击通知本身（而不是关闭或按钮）时的动作标识；
	// Wails 只在 darwin 实现里导出这个常量，这里自己定义，避免其他平台编译失败。
	noticeDefaultAction = "com.apple.UNNotificationDefaultActionIdentifier"
)

// trayNotify 在主窗口隐藏或最小化时，把故障转移与「请求一定会失败」发成系统通知（仅 macOS）；
// 窗口可见时只有窗口内 Toast，两者不重复。
// 通知服务不注册进 Wails 的 Services：任一服务启动失败都会让整个 App 起不来，
// 而没有 bundle ID（直接 go run）时它一定失败。这里手动启动，失败就静默关闭通知。
// 由 appTray 调用的方法对 nil 接收者安全（测试里直接构造的 appTray 没有通知）。
type trayNotify struct {
	tray     *appTray
	service  atomic.Pointer[notifications.NotificationService] // 启动失败时为空，所有通知静默跳过
	enabled  atomic.Bool                                       // 设置 desktop.system_notifications，随菜单重建刷新
	events   chan proxy.FailoverEvent
	authOnce sync.Once
	mu       sync.Mutex
	state    tray.Notifier
}

func newTrayNotify(t *appTray) *trayNotify {
	n := &trayNotify{tray: t, events: make(chan proxy.FailoverEvent, trayFailoverQueue)}
	n.enabled.Store(true)
	return n
}

// trayLogger：通知服务在业务组件装配前就会启动，那时 logger 可能还没就绪。
func (a *App) trayLogger() *slog.Logger {
	if a.logger != nil {
		return a.logger
	}
	return slog.Default()
}

// start 在 ApplicationStarted 之后调用，只在 macOS 启动通知服务。
func (n *trayNotify) start() {
	if runtime.GOOS != "darwin" {
		return
	}
	service := notifications.New()
	if err := service.ServiceStartup(context.Background(), application.ServiceOptions{}); err != nil {
		n.tray.app.trayLogger().Warn("系统通知不可用，故障转移只在窗口内提示", "error", err)
		return
	}
	service.OnNotificationResponse(n.handleResponse)
	n.service.Store(service)
	go n.loop()
}

func (n *trayNotify) setEnabled(enabled bool) {
	if n != nil {
		n.enabled.Store(enabled)
	}
}

func (n *trayNotify) loop() {
	for {
		select {
		case <-n.tray.done:
			return
		case event := <-n.events:
			n.failover(event)
		}
	}
}

// offerFailover 由故障转移回调调用（在请求路径上），只入队、不阻塞。
func (n *trayNotify) offerFailover(event proxy.FailoverEvent) {
	if n == nil || n.service.Load() == nil || !n.enabled.Load() {
		return
	}
	select {
	case n.events <- event:
	default:
	}
}

func (n *trayNotify) failover(event proxy.FailoverEvent) {
	if !n.enabled.Load() || !n.tray.windowHidden() {
		return
	}
	n.mu.Lock()
	notice, ok := n.state.Offer(failoverNotice(event), time.Now())
	n.mu.Unlock()
	if ok {
		n.send(notice)
	}
}

// problemsComparable：代理未运行、或有一侧读取失败时，问题清单不完整（Diagnose 跳过读取失败的一侧），
// 不拿它与上次比较，否则会把「暂时读不到」误报成「已恢复」。
func problemsComparable(s tray.Snapshot) bool {
	return s.Running && !s.Recovery && s.StartupState == "" && s.ClaudeError == "" && s.CodexError == ""
}

// problems 由菜单重建调用（刷新循环），只在 problemsComparable 时调用。
func (n *trayNotify) problems(problems []tray.Problem) {
	if n == nil || n.service.Load() == nil {
		return
	}
	deliver := n.enabled.Load() && n.tray.windowHidden()
	n.mu.Lock()
	notices := n.state.Problems(problems, deliver, time.Now())
	n.mu.Unlock()
	for _, notice := range notices {
		go n.send(notice) // 发送最多等 5 秒结果，不占用刷新循环
	}
}

func (n *trayNotify) send(notice tray.Notice) {
	service := n.service.Load()
	if service == nil || n.tray.closed.Load() {
		return
	}
	err := service.SendNotification(notifications.NotificationOptions{ID: notice.ID, Title: notice.Title, Body: notice.Body,
		Data: map[string]interface{}{"target": notice.Target}})
	if err != nil {
		n.tray.app.trayLogger().Debug("系统通知发送失败", "error", err)
	}
}

// authorize 在第一次隐藏到托盘或最小化时申请通知权限（这时最容易理解为什么需要它）；
// 已授权或已拒绝时系统不会再弹窗。每次运行只申请一次；申请最长等 3 分钟，放到后台。
func (n *trayNotify) authorize() {
	if n == nil {
		return
	}
	service := n.service.Load()
	if service == nil || !n.enabled.Load() {
		return
	}
	n.authOnce.Do(func() {
		go func() {
			if granted, err := service.CheckNotificationAuthorization(); err == nil && granted {
				return
			}
			if _, err := service.RequestNotificationAuthorization(); err != nil {
				n.tray.app.trayLogger().Warn("系统通知授权申请失败", "error", err)
			}
		}()
	})
}

// handleResponse 只处理点击通知本身；回调可能在主线程上，打开窗口放到后台。
func (n *trayNotify) handleResponse(result notifications.NotificationResult) {
	if result.Error != nil || result.Response.ActionIdentifier != noticeDefaultAction {
		return
	}
	target, _ := result.Response.UserInfo["target"].(string)
	go n.tray.showWindow(target)
}

// windowHidden：主窗口已隐藏到托盘或最小化。查询会派发到主线程，只在后台 goroutine 调用。
func (t *appTray) windowHidden() bool {
	return !t.window.IsVisible() || t.window.IsMinimised()
}

// recordFailover 由故障转移回调调用（在请求路径上）：只记内存、入队与合并重建信号，不阻塞。
func (t *appTray) recordFailover(event proxy.FailoverEvent) {
	t.failoverMu.Lock()
	t.failovers = tray.RecordFailover(t.failovers, failoverKey(event), failoverLogText(event), time.Now())
	t.failoverMu.Unlock()
	t.notify.offerFailover(event)
	t.signal(t.refresh)
}

func (a *App) trayFailover(event proxy.FailoverEvent) {
	if a.tray != nil {
		a.tray.recordFailover(event)
	}
}

// failoverReasonLabel 与窗口内 Toast 同一口径；系统通知与「最近切换」只用标签，详情留给 Toast。
func failoverReasonLabel(event proxy.FailoverEvent) string {
	if event.ReasonLabel != "" {
		return event.ReasonLabel
	}
	return proxy.FailoverReasonLabel(event.ReasonCode)
}

// failoverKey：通道、原目标、新目标与原因相同的切换归为一类，通知节流与「最近切换」都按它合并。
func failoverKey(event proxy.FailoverEvent) string {
	return event.Lane + "|" + event.From + "|" + event.To + "|" + event.ReasonCode
}

// failoverNotice 与窗口内 Toast 的口径一致：CC 端点是「本次请求临时改用备用端点」，不说成主端点已切换。
func failoverNotice(event proxy.FailoverEvent) tray.Notice {
	notice := tray.Notice{ID: "failover:" + failoverKey(event), Target: "requests"}
	if event.Lane == proxy.FailoverLaneCodex {
		notice.Title = "Codex 账号已切换"
		notice.Body = fmt.Sprintf("「%s」→「%s」：%s", event.From, event.To, failoverReasonLabel(event))
		return notice
	}
	notice.Title = "Claude 请求临时改用备用端点"
	notice.Body = fmt.Sprintf("改用「%s」（原端点「%s」）：%s", event.To, event.From, failoverReasonLabel(event))
	return notice
}

func failoverLogText(event proxy.FailoverEvent) string {
	if event.Lane == proxy.FailoverLaneCodex {
		return fmt.Sprintf("Codex %s → %s（%s）", event.From, event.To, failoverReasonLabel(event))
	}
	return fmt.Sprintf("Claude 临时改用 %s（原 %s · %s）", event.To, event.From, failoverReasonLabel(event))
}
