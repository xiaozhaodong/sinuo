package proxy

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func claudeRateLimitHeader(values map[string]string) http.Header {
	header := http.Header{}
	for name, value := range values {
		header.Set(claudeRateLimitHeaderPrefix+name, value)
	}
	return header
}

func assertClaudeRateLimitPercent(t *testing.T, label string, got *float64, want float64) {
	t.Helper()
	if got == nil || *got != want {
		t.Fatalf("%s 应为 %v，got %v", label, want, got)
	}
}

func assertClaudeRateLimitTime(t *testing.T, label string, got *time.Time, wantUnix int64) {
	t.Helper()
	if got == nil || got.Unix() != wantUnix || got.Location() != time.UTC {
		t.Fatalf("%s 应为 UTC 的 %d，got %v", label, wantUnix, got)
	}
}

func TestParseClaudeSubscriptionRateLimit_FullHeaders(t *testing.T) {
	now := time.Date(2026, 9, 27, 11, 30, 0, 0, time.FixedZone("CST", 8*3600))
	snapshot, ok := parseClaudeSubscriptionRateLimit(claudeRateLimitHeader(map[string]string{
		"status":               " Rejected ",
		"representative-claim": "five_hour",
		"reset":                "1790500000",
		"overage-status":       "allowed",
		"5h-utilization":       "0.29",
		"5h-reset":             "1790500000.6",
		"7d-utilization":       "1.02",
		"7d-reset":             "1790900000",
	}), now)
	if !ok {
		t.Fatal("带 unified-status 的响应应采集")
	}
	if snapshot.Status != "rejected" || snapshot.RepresentativeClaim != "five_hour" || snapshot.OverageStatus != "allowed" {
		t.Fatalf("状态字段应去空白并小写: %+v", snapshot)
	}
	assertClaudeRateLimitTime(t, "整体重置时间", snapshot.ResetAt, 1790500000)
	assertClaudeRateLimitPercent(t, "5h 已用", snapshot.FiveHour.UsedPercent, 29)
	assertClaudeRateLimitTime(t, "5h 重置时间（小数秒四舍五入）", snapshot.FiveHour.ResetAt, 1790500001)
	assertClaudeRateLimitPercent(t, "7d 已用（超额不截断）", snapshot.SevenDay.UsedPercent, 102)
	assertClaudeRateLimitTime(t, "7d 重置时间", snapshot.SevenDay.ResetAt, 1790900000)
	if !snapshot.CapturedAt.Equal(now) || snapshot.CapturedAt.Location() != time.UTC {
		t.Fatalf("采集时间应为 UTC，got %v", snapshot.CapturedAt)
	}
}

func TestParseClaudeSubscriptionRateLimit_MissingAndInvalidFieldsStayNil(t *testing.T) {
	snapshot, ok := parseClaudeSubscriptionRateLimit(claudeRateLimitHeader(map[string]string{
		"status":         "allowed",
		"reset":          "NaN",
		"5h-utilization": "abc",
		"5h-reset":       "-1",
		"7d-utilization": "-0.1",
		"7d-reset":       "1e300",
	}), time.Now())
	if !ok {
		t.Fatal("只有 status 也应采集")
	}
	if snapshot.RepresentativeClaim != "" || snapshot.OverageStatus != "" {
		t.Fatalf("缺失的文本字段应为空: %+v", snapshot)
	}
	if snapshot.ResetAt != nil || snapshot.FiveHour.UsedPercent != nil || snapshot.FiveHour.ResetAt != nil ||
		snapshot.SevenDay.UsedPercent != nil || snapshot.SevenDay.ResetAt != nil {
		t.Fatalf("缺失或非法的数值应保持 nil，不能填 0: %+v", snapshot)
	}
}

func TestParseClaudeSubscriptionRateLimit_WithoutStatusIsIgnored(t *testing.T) {
	header := claudeRateLimitHeader(map[string]string{"5h-utilization": "0.5"})
	if _, ok := parseClaudeSubscriptionRateLimit(header, time.Now()); ok {
		t.Fatal("不带 unified-status 的响应不应采集")
	}
}

func TestClaudeSubscription_RecordsRateLimitAndKeepsItWhenHeadersAbsent(t *testing.T) {
	var withHeaders atomic.Bool
	subscription := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if withHeaders.Load() {
			w.Header().Set("Anthropic-Ratelimit-Unified-Status", "rejected")
			w.Header().Set("Anthropic-Ratelimit-Unified-Representative-Claim", "five_hour")
			w.Header().Set("Anthropic-Ratelimit-Unified-5h-Utilization", "1")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"type":"error","error":{"type":"rate_limit_error","message":"usage limit reached"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"input_tokens":12}`))
	}))
	defer subscription.Close()

	handler, _, _ := newClaudeSubscriptionTestHandler(t, subscription.URL)
	notified := make(chan ClaudeSubscriptionQuotaState, 8)
	handler.SetOnClaudeSubscriptionQuota(func(state ClaudeSubscriptionQuotaState) { notified <- state })

	if state := handler.ClaudeSubscriptionQuota(); state.Headers != nil || state.CredentialAvailable {
		t.Fatalf("还没有订阅请求时不应有快照或凭据: %+v", state)
	}
	withHeaders.Store(true)
	if recorder := performClaudeSubscriptionRequest(t, handler, claudeSubscriptionRequest{requestID: "req-rate-limit-429"}); recorder.Code != http.StatusTooManyRequests {
		t.Fatalf("429 应原样透传，got %d", recorder.Code)
	}
	state := handler.ClaudeSubscriptionQuota()
	snapshot := state.Headers
	if snapshot == nil || snapshot.Status != "rejected" || snapshot.RepresentativeClaim != "five_hour" {
		t.Fatalf("429 带回的额度头应被采集: %+v", snapshot)
	}
	assertClaudeRateLimitPercent(t, "5h 已用", snapshot.FiveHour.UsedPercent, 100)
	if !state.CredentialAvailable {
		t.Fatal("订阅请求经过后应在内存里保留查询凭据")
	}
	if got := drainClaudeQuotaStates(notified); len(got) != 2 || !got[0].CredentialAvailable || got[1].Headers == nil || got[1].Headers.Status != "rejected" {
		t.Fatalf("应先通知拿到凭据、再通知采集到额度，got %+v", got)
	}

	withHeaders.Store(false)
	performClaudeSubscriptionRequest(t, handler, claudeSubscriptionRequest{path: "/v1/messages/count_tokens", requestID: "req-rate-limit-count"})
	if kept := handler.ClaudeSubscriptionQuota().Headers; kept == nil || !kept.CapturedAt.Equal(snapshot.CapturedAt) || kept.Status != "rejected" {
		t.Fatalf("不带额度头的响应应保留旧快照，got %+v", kept)
	}
	if got := drainClaudeQuotaStates(notified); len(got) != 0 {
		t.Fatalf("没有采集到新额度、凭据也未变化时不应通知，got %+v", got)
	}
}

func drainClaudeQuotaStates(ch chan ClaudeSubscriptionQuotaState) []ClaudeSubscriptionQuotaState {
	var states []ClaudeSubscriptionQuotaState
	for {
		select {
		case state := <-ch:
			states = append(states, state)
		default:
			return states
		}
	}
}

func TestClaudeSubscription_StreamingSuccessRecordsRateLimit(t *testing.T) {
	subscription := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Anthropic-Ratelimit-Unified-Status", "allowed")
		w.Header().Set("Anthropic-Ratelimit-Unified-5h-Utilization", "0.42")
		w.Header().Set("Anthropic-Ratelimit-Unified-7d-Utilization", "0.1")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"))
	}))
	defer subscription.Close()

	handler, _, _ := newClaudeSubscriptionTestHandler(t, subscription.URL)
	performClaudeSubscriptionRequest(t, handler, claudeSubscriptionRequest{streaming: true})

	snapshot := handler.ClaudeSubscriptionQuota().Headers
	if snapshot == nil || snapshot.Status != "allowed" {
		t.Fatalf("流式成功响应应采集额度: %+v", snapshot)
	}
	assertClaudeRateLimitPercent(t, "5h 已用", snapshot.FiveHour.UsedPercent, 42)
	assertClaudeRateLimitPercent(t, "7d 已用", snapshot.SevenDay.UsedPercent, 10)
}
