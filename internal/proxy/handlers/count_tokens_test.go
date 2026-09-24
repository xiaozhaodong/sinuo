package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"cc-forwarder/config"
	"cc-forwarder/internal/endpoint"
)

func TestIsCountTokensUnsupportedRequiresCountTokensContextForTextMatches(t *testing.T) {
	if !isCountTokensUnsupported(http.StatusNotFound, `{"error":"missing route"}`) {
		t.Fatal("expected 404 to mark count_tokens as unsupported")
	}
	if !isCountTokensUnsupported(http.StatusBadRequest, `{"error":"count_tokens endpoint is not supported"}`) {
		t.Fatal("expected explicit count_tokens not supported message to be marked unsupported")
	}
	if isCountTokensUnsupported(http.StatusBadRequest, `{"error":"model unsupported"}`) {
		t.Fatal("model unsupported should not mark count_tokens as unsupported")
	}
	if isCountTokensUnsupported(http.StatusUnprocessableEntity, `{"error":"parameter temperature is not supported"}`) {
		t.Fatal("generic parameter errors should not mark count_tokens as unsupported")
	}
}

func TestCountTokensTryForwardRewritesEndpointModel(t *testing.T) {
	var receivedModel string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode upstream body failed: %v", err)
		}
		receivedModel, _ = payload["model"].(string)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"input_tokens":12}`))
	}))
	defer server.Close()

	cfg := &config.Config{}
	manager := endpoint.NewManager(cfg)
	defer manager.Stop()
	forwarder := NewForwarder(cfg, manager)
	handler := NewCountTokensHandler(cfg, manager, forwarder)
	target := acquireForwarderTestTarget(t, manager, config.EndpointConfig{
		Name:                "count-rewrite",
		URL:                 server.URL,
		Timeout:             2 * time.Second,
		SupportsCountTokens: true,
		ModelRewriteRules:   `[{"paths":["/v1/messages/count_tokens"],"match":"exact","from":"claude-sonnet-4-5","to":"provider-sonnet"}]`,
	})
	body := []byte(`{"model":"claude-sonnet-4-5","messages":[]}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/messages/count_tokens", bytes.NewReader(body))

	result, outcome, err := handler.attemptEndpoint(context.Background(), req, body, target, endpoint.BuildRouteRequestProfile(req.URL.Path, body), "req-count-rewrite")
	if err != nil || outcome != countTokensAttemptSuccess {
		t.Fatalf("count_tokens forward failed: outcome=%v err=%v body=%s", outcome, err, string(result))
	}
	if receivedModel != "provider-sonnet" {
		t.Fatalf("expected rewritten count_tokens model, got %q", receivedModel)
	}
}

func newCountTokensBaselineHandler(t *testing.T, endpoints ...config.EndpointConfig) (*CountTokensHandler, *endpoint.Manager) {
	t.Helper()
	cfg := &config.Config{
		Strategy: config.StrategyConfig{Type: "priority"},
		Failover: config.FailoverConfig{Enabled: true, DefaultCooldown: 10 * time.Minute},
		FailureTracker: config.FailureTrackerConfig{
			Enabled:    true,
			TimeWindow: 5 * time.Minute,
			Threshold:  3,
			Action:     "failover",
		},
		TokenCounting: config.TokenCountingConfig{EstimationRatio: 4.0},
		Endpoints:     endpoints,
	}
	manager := endpoint.NewManager(cfg)
	t.Cleanup(manager.Stop)
	return NewCountTokensHandler(cfg, manager, NewForwarder(cfg, manager)), manager
}

func performCountTokensBaselineRequest(t *testing.T, handler *CountTokensHandler) *httptest.ResponseRecorder {
	t.Helper()
	body := []byte(`{"model":"claude-test","messages":[{"role":"user","content":"hello world"}]}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/messages/count_tokens", bytes.NewReader(body))
	recorder := httptest.NewRecorder()
	handler.Handle(context.Background(), recorder, req, body, "req-count-baseline")
	return recorder
}

func requireCountTokensEstimation(t *testing.T, recorder *httptest.ResponseRecorder) {
	t.Helper()
	if recorder.Code != http.StatusOK {
		t.Fatalf("baseline expects estimation 200, got status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if recorder.Header().Get("X-Token-Estimation") != "true" {
		t.Fatalf("expected estimation marker header, got %v", recorder.Header())
	}
	switch recorder.Header().Get("X-Token-Estimation-Reason") {
	case "no_eligible_endpoint", "unsupported", "upstream_failed", "attempt_budget_exhausted":
	default:
		t.Fatalf("estimation reason must be whitelisted, got %q", recorder.Header().Get("X-Token-Estimation-Reason"))
	}
	var payload CountTokensResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil || payload.InputTokens <= 0 {
		t.Fatalf("expected positive estimated tokens, err=%v body=%s", err, recorder.Body.String())
	}
}

// [Phase3 §10.1] 无支持端点：估算 200 + 受控 reason
func TestCountTokens_NoSupportedEndpointFallsBackToEstimation(t *testing.T) {
	handler, _ := newCountTokensBaselineHandler(t, config.EndpointConfig{
		Name:     "no-count",
		URL:      "http://127.0.0.1:1",
		Priority: 1,
		Timeout:  time.Second,
	})
	requireCountTokensEstimation(t, performCountTokensBaselineRequest(t, handler))
}

// [Phase3 §10] 上游 5xx：估算兜底 + count_tokens scoped 软失败，不污染 /v1/messages
// （不写端点全局冷却）
func TestCountTokens_UpstreamFailureFallsBackToEstimationWithScopedSoftFailure(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"error":"boom"}`, http.StatusInternalServerError)
	}))
	t.Cleanup(upstream.Close)

	handler, manager := newCountTokensBaselineHandler(t, config.EndpointConfig{
		Name:                "count-500",
		URL:                 upstream.URL,
		Priority:            1,
		Timeout:             time.Second,
		SupportsCountTokens: true,
	})
	requireCountTokensEstimation(t, performCountTokensBaselineRequest(t, handler))

	counts := manager.GetSoftFailureCounts("count-500", endpoint.SoftFailureScopeCountTokens)
	if counts[endpoint.SoftFailureCategoryServerError] != 1 {
		t.Fatalf("expected one count_tokens server_error soft failure, got %+v", counts)
	}
	if msgCounts := manager.GetSoftFailureCounts("count-500", endpoint.SoftFailureScopeMessages); len(msgCounts) != 0 {
		t.Fatalf("count_tokens failure must not pollute messages scope, got %+v", msgCounts)
	}
	if ep := manager.GetEndpointByNameAny("count-500"); ep == nil || ep.IsInCooldown() {
		t.Fatal("count_tokens failure must not trigger endpoint-global cooldown")
	}
}

func TestCountTokens_ForbiddenCapabilityErrorsDoNotTriggerGlobalAuthCooldown(t *testing.T) {
	tests := []struct {
		name             string
		body             string
		wantCountRoute   bool
		wantMessageRoute bool
	}{
		{
			name:             "count_tokens unsupported",
			body:             `{"error":"count_tokens endpoint is not supported"}`,
			wantCountRoute:   false,
			wantMessageRoute: true,
		},
		{
			name:             "model unsupported",
			body:             `{"error":"model is not supported"}`,
			wantCountRoute:   false,
			wantMessageRoute: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				http.Error(w, tc.body, http.StatusForbidden)
			}))
			t.Cleanup(upstream.Close)

			handler, manager := newCountTokensBaselineHandler(t, config.EndpointConfig{
				Name:                "count-403",
				URL:                 upstream.URL,
				Priority:            1,
				Timeout:             time.Second,
				SupportsCountTokens: true,
			})
			requireCountTokensEstimation(t, performCountTokensBaselineRequest(t, handler))

			status := manager.GetEndpointStatus("count-403")
			if !status.GlobalCooldownUntil.IsZero() {
				t.Fatalf("capability error must not trigger global auth cooldown: %+v", status)
			}
			body := []byte(`{"model":"claude-test","messages":[]}`)
			countResult := manager.PrepareRouteCandidates(context.Background(), endpoint.BuildRouteRequestProfile("/v1/messages/count_tokens", body))
			if got := len(countResult.Candidates) > 0; got != tc.wantCountRoute {
				t.Fatalf("count_tokens routable=%v, want %v", got, tc.wantCountRoute)
			}
			messageResult := manager.PrepareRouteCandidates(context.Background(), endpoint.BuildRouteRequestProfile("/v1/messages", body))
			if got := len(messageResult.Candidates) > 0; got != tc.wantMessageRoute {
				t.Fatalf("messages routable=%v, want %v", got, tc.wantMessageRoute)
			}
		})
	}
}

func TestCountTokens_GenericForbiddenTriggersGlobalAuthCooldown(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"error":"permission denied"}`, http.StatusForbidden)
	}))
	t.Cleanup(upstream.Close)

	handler, manager := newCountTokensBaselineHandler(t, config.EndpointConfig{
		Name:                "count-auth-403",
		URL:                 upstream.URL,
		Priority:            1,
		Timeout:             time.Second,
		SupportsCountTokens: true,
	})
	requireCountTokensEstimation(t, performCountTokensBaselineRequest(t, handler))

	status := manager.GetEndpointStatus("count-auth-403")
	if status.GlobalCooldownReason != "auth_rejected" || !status.GlobalCooldownUntil.After(time.Now()) {
		t.Fatalf("generic 403 must trigger global auth cooldown: %+v", status)
	}
}

// TestCountTokens_StaleEpochSettlementDropped
// 场景:旧 epoch 的 auth 结算与 scoped 软失败结算均被丢弃——global 槽无冷却、
// count_tokens 计数不增加、无 scoped cooldown
func TestCountTokens_StaleEpochSettlementDropped(t *testing.T) {
	handler, manager := newCountTokensBaselineHandler(t, config.EndpointConfig{
		Name: "count-stale", URL: "http://127.0.0.1:1", Timeout: 2 * time.Second,
		SupportsCountTokens: true,
	})
	body := []byte(`{"model":"claude-test","messages":[{"role":"user","content":"hello"}]}`)
	profile := endpoint.BuildRouteRequestProfile("/v1/messages/count_tokens", body)
	plan := manager.PrepareRouteCandidates(context.Background(), profile).Plans[0]
	oldEpoch := plan.FailureEpoch

	if _, found := manager.ResetEndpointFailureState("count-stale"); !found {
		t.Fatal("reset endpoint failure state failed")
	}

	// 旧 epoch auth 结算
	handler.recordAuthCooldown("count-stale", plan.ConfigRevision, oldEpoch, "req-count-stale")
	// 旧 epoch scoped 软失败结算(连记 3 次,若未被拒绝将 trip 并写 scoped cooldown)
	for i := 0; i < 3; i++ {
		handler.recordScopedSoftFailure("count-stale", plan.ConfigRevision, oldEpoch,
			endpoint.SoftFailureCategoryServerError, 0)
	}

	if in, _, reason := manager.GetEndpointCooldownInfo("count-stale"); in {
		t.Fatalf("旧 epoch auth 结算不应写 global 冷却, reason=%s", reason)
	}
	if counts := manager.GetSoftFailureCounts("count-stale", endpoint.SoftFailureScopeCountTokens); len(counts) != 0 {
		t.Fatalf("旧 epoch 结算不应增加 count_tokens 计数: %+v", counts)
	}
	if active, _, _ := manager.ScopedCooldownActive("count-stale", endpoint.SoftFailureScopeCountTokens); active {
		t.Fatal("旧 epoch 结算不应写入 scoped cooldown")
	}
}

// TestCountTokens_NewEpochSettlementNormal
// 场景:解除冷却后重新规划(新 epoch),count_tokens 计数/trip/scoped cooldown 行为与现状一致
func TestCountTokens_NewEpochSettlementNormal(t *testing.T) {
	handler, manager := newCountTokensBaselineHandler(t, config.EndpointConfig{
		Name: "count-fresh", URL: "http://127.0.0.1:1", Timeout: 2 * time.Second,
		SupportsCountTokens: true,
	})
	body := []byte(`{"model":"claude-test","messages":[{"role":"user","content":"hello"}]}`)
	profile := endpoint.BuildRouteRequestProfile("/v1/messages/count_tokens", body)

	if _, found := manager.ResetEndpointFailureState("count-fresh"); !found {
		t.Fatal("reset endpoint failure state failed")
	}
	plan := manager.PrepareRouteCandidates(context.Background(), profile).Plans[0]
	if plan.FailureEpoch != 1 {
		t.Fatalf("reset 后 plan 应携带新 epoch=1, got=%d", plan.FailureEpoch)
	}

	for i := 0; i < 2; i++ {
		handler.recordScopedSoftFailure("count-fresh", plan.ConfigRevision, plan.FailureEpoch,
			endpoint.SoftFailureCategoryServerError, 0)
	}
	if counts := manager.GetSoftFailureCounts("count-fresh", endpoint.SoftFailureScopeCountTokens); counts[endpoint.SoftFailureCategoryServerError] != 2 {
		t.Fatalf("新 epoch 计数应正常累计为 2: %+v", counts)
	}
	// 第 3 次 trip → scoped cooldown
	handler.recordScopedSoftFailure("count-fresh", plan.ConfigRevision, plan.FailureEpoch,
		endpoint.SoftFailureCategoryServerError, 0)
	if active, _, _ := manager.ScopedCooldownActive("count-fresh", endpoint.SoftFailureScopeCountTokens); !active {
		t.Fatal("新 epoch trip 后应写入 scoped cooldown")
	}
	// scoped cooldown 不影响 messages 路径
	if in, _, _ := manager.GetEndpointCooldownInfo("count-fresh"); in {
		t.Fatal("count_tokens scoped cooldown 不应冷却 /v1/messages")
	}
}
