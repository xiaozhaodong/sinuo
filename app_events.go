// app_events.go - Wails 事件发射
// 将 Go 后端状态变化通知到前端

package main

import (
	"fmt"

	"cc-forwarder/internal/proxy"
)

// 事件名称常量
const (
	EventSystemStatus   = "system:status"
	EventEndpointUpdate = "endpoint:update"
	EventUsageUpdate    = "usage:update"
	EventRequestUpdate  = "request:update"
	EventConfigReloaded = "config:reloaded"
	EventError          = "error"
	EventNotification   = "notification"
	// EventCodexSubscriptionProxyStatus 只推运行态摘要；请求列表仍用 request:update。
	EventCodexSubscriptionProxyStatus = "codex-subscription-proxy:status"
)

// wailsRequestBroadcaster 将内部请求生命周期事件桥接到 Wails WebView。
// 现有 EventBus 接口沿用 SSEBroadcaster 命名，但这里只处理 request 类型，
// 避免与端点、分组等已经存在的 Wails 推送路径重复广播。
type wailsRequestBroadcaster struct {
	app *App
}

// emit 是 Go 侧唯一的 Wails 事件出口。应用尚未完成 v3 runtime 初始化时静默跳过，
// 保留旧版 ctx nil 判断的语义。
func (a *App) emit(name string, data any) {
	if a == nil || a.closing.Load() || a.ctx == nil || a.wailsApp == nil || a.wailsApp.Event == nil {
		return
	}
	a.wailsApp.Event.Emit(name, data)
}

func (b *wailsRequestBroadcaster) BroadcastEvent(eventType string, data map[string]interface{}) {
	if eventType != "request" || b == nil || b.app == nil || b.app.ctx == nil {
		return
	}
	b.app.emit(EventRequestUpdate, data)
}

func (b *wailsRequestBroadcaster) IsEventManagerActive() bool {
	return b != nil && b.app != nil && b.app.ctx != nil && b.app.wailsApp != nil
}

// emitSystemStatus 发送系统状态更新到前端
func (a *App) emitSystemStatus() {
	if a.ctx == nil {
		return
	}

	status := a.GetSystemStatus()
	a.emit(EventSystemStatus, status)
}

// emitEndpointUpdate 发送端点状态更新到前端
func (a *App) emitEndpointUpdate() {
	if a.ctx == nil {
		return
	}

	endpoints := a.GetEndpoints()
	// 包装为前端期望的格式 { endpoints: [...] }
	data := map[string]interface{}{
		"endpoints": endpoints,
	}

	if a.logger != nil {
		a.logger.Info("📡 [Wails Event] 推送端点更新", "count", len(endpoints))
	}

	a.emit(EventEndpointUpdate, data)
}

// emitUsageUpdate 发送使用统计更新到前端
func (a *App) emitUsageUpdate() {
	if a.ctx == nil {
		return
	}

	summary, _ := a.GetUsageSummary("", "")
	a.emit(EventUsageUpdate, summary)
}

// emitNotification 发送通知到前端
func (a *App) emitNotification(level, title, message string) {
	a.emitNotificationData(map[string]interface{}{
		"level":   level, // "info", "warning", "error", "success"
		"title":   title,
		"message": message,
	})
}

// emitFailoverNotification 将请求级故障转移转换为窗口内全局 Toast 事件。
// 不调用任何 macOS/系统通知 API，避免在桌面之外产生额外打扰。
func (a *App) emitFailoverNotification(event proxy.FailoverEvent) {
	laneLabel := "CC 端点"
	if event.Lane == proxy.FailoverLaneCodex {
		laneLabel = "Codex 账号"
	}

	reason := event.ReasonLabel
	if reason == "" {
		reason = proxy.FailoverReasonLabel(event.ReasonCode)
	}
	if event.ReasonDetail != "" {
		reason = fmt.Sprintf("%s（%s）", reason, event.ReasonDetail)
	}

	title := "发生故障转移"
	message := fmt.Sprintf("%s已从「%s」切换到「%s」。原因：%s", laneLabel, event.From, event.To, reason)
	if event.Lane == proxy.FailoverLaneCC {
		// v8 §13.5：临时 fallback 不得描述为"主端点已切换"
		title = "本次请求临时改用备用端点"
		message = fmt.Sprintf("本次请求临时改用「%s」。原因：%s（原端点「%s」）", event.To, reason, event.From)
	}

	a.emitNotificationData(map[string]interface{}{
		"level":         "warning",
		"title":         title,
		"message":       message,
		"kind":          event.Kind,
		"lane":          event.Lane,
		"from":          event.From,
		"to":            event.To,
		"reason_code":   event.ReasonCode,
		"reason_label":  reason,
		"reason_detail": event.ReasonDetail,
		"request_id":    event.RequestID,
		"request_path":  event.RequestPath,
		"attempt":       event.Attempt,
	})
}

func (a *App) emitNotificationData(data map[string]interface{}) {
	if a.ctx == nil {
		return
	}
	a.emit(EventNotification, data)
}
