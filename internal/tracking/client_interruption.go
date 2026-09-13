package tracking

// SubscriptionClientDisconnected 只描述客户端连接先结束，不推断用户取消或任务续接。
// 复用 cancelled 结算类别，页面再将这个有明确来源的原因展示为“已中断”。
const SubscriptionClientDisconnected = "subscription_client_disconnected"

const clientInterruptionSQL = `(status = 'cancelled'
	AND COALESCE(request_source, '') = '` + RequestSourceCodexSubscriptionProxy + `'
	AND COALESCE(failure_reason, '') = '` + SubscriptionClientDisconnected + `')`

func isClientInterruption(status, source, reason string) bool {
	return status == "cancelled" && source == RequestSourceCodexSubscriptionProxy && reason == SubscriptionClientDisconnected
}

// requestDisplayStatus 只供查询筛选和统计使用，不改写原始请求记录。
func requestDisplayStatus(status, source, reason string) string {
	if isClientInterruption(status, source, reason) {
		return "interrupted"
	}
	return status
}
