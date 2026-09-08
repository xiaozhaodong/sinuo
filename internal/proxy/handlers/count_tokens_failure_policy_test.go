package handlers

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"cc-forwarder/config"
	"cc-forwarder/internal/endpoint"
)

func TestCountTokens_SoftFailurePolicy_独立阈值仅冷却计数接口(t *testing.T) {
	var hits int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&hits, 1)
		http.Error(w, "rate limited", http.StatusTooManyRequests)
	}))
	t.Cleanup(upstream.Close)
	window, threshold := 10, 1
	handler, manager := newCountTokensBaselineHandler(t, config.EndpointConfig{
		Name: "policy-count", URL: upstream.URL, Priority: 1, SupportsCountTokens: true,
		SoftFailureWindowMinutes: &window, SoftFailureThreshold: &threshold,
	})
	first := performCountTokensBaselineRequest(t, handler)
	if first.Code != http.StatusOK {
		t.Fatalf("本地估算失败: %d", first.Code)
	}
	if active, _, _ := manager.ScopedCooldownActive("policy-count", endpoint.SoftFailureScopeCountTokens); !active {
		t.Fatal("独立阈值未应用于 count_tokens")
	}
	if manager.GetEndpointByNameAny("policy-count").IsInCooldown() {
		t.Fatal("count_tokens 失败影响了 messages")
	}
	if counts := manager.GetSoftFailureCounts("policy-count", endpoint.SoftFailureScopeMessages); len(counts) != 0 {
		t.Fatal("计数接口污染了 messages 的累计失败")
	}
	performCountTokensBaselineRequest(t, handler)
	if atomic.LoadInt32(&hits) != 1 {
		t.Fatal("后续请求未跳过已冷却的计数接口")
	}
}
