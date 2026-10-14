package tracking

// SetRetentionDays 热更新清理任务使用的保留天数，0 表示永久保留。
func (ut *UsageTracker) SetRetentionDays(days int) {
	if days < 0 {
		days = 0
	}
	ut.retentionDays.Store(int64(days))
}

func (ut *UsageTracker) getRetentionDays() int {
	return int(ut.retentionDays.Load())
}
