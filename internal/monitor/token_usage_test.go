package monitor

import "testing"

func TestTokenUsage_MixedProtocolsKeepRawCountsAndDeduplicateTotals(t *testing.T) {
	m := NewMetrics()
	codexID := m.RecordRequest("upstream", "127.0.0.1", "synthetic", "POST", "/v1/responses")
	codex := &TokenUsage{InputTokens: 272077, OutputTokens: 1671, CacheCreationTokens: 2000, CacheReadTokens: 265159}
	codex.SetProtocol("codex", "/v1/responses")
	codex.SetProtocol("codex", "/v1/responses")
	m.RecordTokenUsage(codexID, "upstream", codex)

	claude := &TokenUsage{InputTokens: 100, OutputTokens: 50, CacheCreationTokens: 10, CacheReadTokens: 20}
	claude.SetProtocol("claude", "/v1/messages")
	m.RecordTokenUsage("claude-request", "upstream", claude)
	const wantTotal = 273748 + 180
	got := m.GetTotalTokenStats()
	if got.InputTokens != 272177 || got.OutputTokens != 1721 || got.CacheCreationTokens != 2010 || got.CacheReadTokens != 265179 {
		t.Fatalf("原始分项丢失：%+v", got)
	}
	if got.TotalTokens() != wantTotal {
		t.Fatalf("混合协议总量 = %d，期望 %d", got.TotalTokens(), wantTotal)
	}

	snapshot := m.GetMetrics()
	if snapshot.TotalTokenUsage.TotalTokens() != wantTotal || snapshot.EndpointStats["upstream"].TokenUsage.TotalTokens() != wantTotal {
		t.Fatal("快照或上游统计丢失了缓存包含关系")
	}
	if snapshot.ActiveConnections[codexID].TokenUsage.TotalTokens() != 273748 {
		t.Fatal("单连接重复累计了缓存")
	}
	m.AddHistoryDataPoints()
	history := m.GetTokenHistory()
	if len(history) != 1 || history[0].TotalTokens != wantTotal || history[0].CacheCreationTokens != 2010 {
		t.Fatalf("历史采样错误：%+v", history)
	}
	m.RecordFailedRequestTokenUsage(codexID, "upstream", codex, "synthetic_failure")
	if m.FailedRequestTokens != 273748 || m.FailedTokensByReason["synthetic_failure"] != 273748 {
		t.Fatal("失败统计重复累计了缓存")
	}
	m.RecordTokenUsage("later", "upstream", claude)
	if snapshot.TotalTokenUsage.TotalTokens() != wantTotal {
		t.Fatal("已获取的快照被后续统计改变")
	}
}

func TestTokenUsage_UnclassifiedUsageKeepsLegacyCounting(t *testing.T) {
	u := &TokenUsage{InputTokens: 100, OutputTokens: 50, CacheCreationTokens: 10, CacheReadTokens: 20}
	if u.TotalTokens() != 180 {
		t.Fatal("未指定协议的统计口径改变")
	}
	u.SetProtocol("", "/backend-api/codex/responses/compact")
	if u.TotalTokens() != 150 {
		t.Fatal("未按历史 Responses 路径识别缓存包含关系")
	}
	u.SetProtocol("claude", "/v1/messages")
	if u.TotalTokens() != 180 {
		t.Fatal("重新指定协议后保留了旧缓存包含关系")
	}
}
