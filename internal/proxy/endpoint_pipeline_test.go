package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"sinuo/config"
	"sinuo/internal/endpoint"
	"sinuo/internal/privacy"
)

func newEndpointPipelineTestHandler(t *testing.T, endpoints ...config.EndpointConfig) (*Handler, *endpoint.Manager) {
	t.Helper()

	cfg := &config.Config{
		Failover: config.FailoverConfig{
			Enabled: true,
		},
		FailureTracker: config.FailureTrackerConfig{
			Enabled:    true,
			TimeWindow: 5 * time.Minute,
			Threshold:  3,
		},
		Streaming: config.StreamingConfig{
			ResponseHeaderTimeout: 2 * time.Second,
		},
		GlobalTimeout: 2 * time.Second,
		Endpoints:     endpoints,
	}

	manager := endpoint.NewManager(cfg)
	t.Cleanup(manager.Stop)
	return NewHandler(manager, cfg), manager
}

func endpointPipelineConfig(name, url string, priority int) config.EndpointConfig {
	return config.EndpointConfig{
		Name:     name,
		URL:      url,
		Priority: priority,
		Timeout:  2 * time.Second,
	}
}

func endpointPipelineClosedURL(t *testing.T) string {
	t.Helper()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen for closed endpoint URL: %v", err)
	}
	url := "http://" + listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatalf("close endpoint listener: %v", err)
	}
	return url
}

func performEndpointPipelineRequest(t *testing.T, handler *Handler, streaming bool) *httptest.ResponseRecorder {
	t.Helper()

	body := `{"model":"claude-test","messages":[]}`
	if streaming {
		body = `{"model":"claude-test","messages":[],"stream":true}`
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewBufferString(body))
	req = req.WithContext(context.WithValue(req.Context(), "conn_id", "req-endpoint-pipeline"))
	req.Header.Set("Content-Type", "application/json")
	if streaming {
		req.Header.Set("Accept", "text/event-stream")
	}

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)
	return recorder
}

func requireLatestEndpointScheduleSnapshot(t *testing.T, manager *endpoint.Manager, outcome, selectedEndpoint string) *endpoint.EndpointScheduleSnapshot {
	t.Helper()
	snapshot := manager.GetLatestEndpointScheduleSnapshot()
	if snapshot == nil {
		t.Fatal("expected latest endpoint schedule snapshot")
	}
	if snapshot.RequestID != "req-endpoint-pipeline" || snapshot.RequestPath != "/v1/messages" {
		t.Fatalf("unexpected snapshot request metadata: %+v", snapshot)
	}
	if snapshot.FinalOutcome != outcome || snapshot.SelectedEndpoint != selectedEndpoint {
		t.Fatalf("unexpected snapshot final state: %+v", snapshot)
	}
	return snapshot
}

type pipelineRouteScopePolicy struct {
	scope endpoint.RouteScope
}

func (p pipelineRouteScopePolicy) ResolveScope(string, bool) (endpoint.RouteScope, bool) {
	return p.scope, true
}

func (pipelineRouteScopePolicy) NoteDecision(endpoint.RouteScope, string)           {}
func (pipelineRouteScopePolicy) SettleDecision(endpoint.RouteScope, string, string) {}
func (pipelineRouteScopePolicy) ScopeStillValid(endpoint.RouteScope) bool           { return true }

func TestEndpointPipeline_ModelRoutingUnassignedReturns422AndScopedSnapshot(t *testing.T) {
	handler, manager := newEndpointPipelineTestHandler(t, endpointPipelineConfig("primary", endpointPipelineClosedURL(t), 1))
	manager.SetRouteScopePolicy(pipelineRouteScopePolicy{scope: endpoint.RouteScope{
		Kind:             endpoint.RouteScopeUnassigned,
		Model:            "claude-test",
		UnassignedReason: "model_routing_unassigned",
	}})

	recorder := performEndpointPipelineRequest(t, handler, false)
	if recorder.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected unassigned model to return 422, got status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), "model_routing_unassigned") || !strings.Contains(recorder.Body.String(), "claude-test") {
		t.Fatalf("expected model and routing error code in response, got %s", recorder.Body.String())
	}
	snapshot := requireLatestEndpointScheduleSnapshot(t, manager, endpoint.EndpointScheduleOutcomeModelRoutingUnassigned, "")
	if snapshot.RouteScope != endpoint.RouteScopeUnassigned || snapshot.RequestModel != "claude-test" {
		t.Fatalf("expected unassigned scope and request model in snapshot, got %+v", snapshot)
	}
}

func TestEndpointPipeline_ModelRoutingGroupUsesOnlyGroupMembers(t *testing.T) {
	var primaryHits, memberHits int32
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&primaryHits, 1)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(primary.Close)
	member := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&memberHits, 1)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(member.Close)

	handler, manager := newEndpointPipelineTestHandler(t,
		endpointPipelineConfig("primary", primary.URL, 1),
		endpointPipelineConfig("member", member.URL, 2),
	)
	manager.SetRouteScopePolicy(pipelineRouteScopePolicy{scope: endpoint.RouteScope{
		Kind:            endpoint.RouteScopeUpstreamGroup,
		GroupID:         7,
		GroupName:       "claude-group",
		Members:         []string{"member"},
		Mode:            endpoint.RouteModeAuto,
		FallbackEnabled: true,
		Revision:        1,
		SnapshotVersion: 1,
		Model:           "claude-test",
	}})

	recorder := performEndpointPipelineRequest(t, handler, false)
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected group member success, got status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if atomic.LoadInt32(&primaryHits) != 0 || atomic.LoadInt32(&memberHits) != 1 {
		t.Fatalf("expected only group member to be attempted, primary=%d member=%d", primaryHits, memberHits)
	}
	snapshot := requireLatestEndpointScheduleSnapshot(t, manager, endpoint.EndpointScheduleOutcomeSuccess, "member")
	if snapshot.RouteScope != endpoint.RouteScopeUpstreamGroup || snapshot.RouteScopeGroupID != 7 || snapshot.UpstreamGroup == nil {
		t.Fatalf("expected group scope in snapshot, got %+v", snapshot)
	}
}

// [Phase3 §11.2 D9] fallback 成功只更新运行时 retained，active/legacy 状态不变
func TestEndpointPipeline_ConnectionFailure_FailsOverAndRetainsWithoutMigratingActive(t *testing.T) {
	var backupHits int32
	backup := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&backupHits, 1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(backup.Close)

	handler, manager := newEndpointPipelineTestHandler(t,
		endpointPipelineConfig("primary", endpointPipelineClosedURL(t), 1),
		endpointPipelineConfig("backup", backup.URL, 2),
	)

	tracker := attachRetryTracker(t, handler)

	recorder := performEndpointPipelineRequest(t, handler, false)
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected backup success, got status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if got := atomic.LoadInt32(&backupHits); got != 1 {
		t.Fatalf("expected backup to be called once, got %d", got)
	}
	if got := manager.RetainedInTier(2); got != "backup" {
		t.Fatalf("expected backup retained in tier 2, got %q", got)
	}
	if got := manager.GetFailureStats()["primary"]; got != 1 {
		t.Fatalf("expected primary failure count 1, got %d", got)
	}
	snapshot := requireLatestEndpointScheduleSnapshot(t, manager, endpoint.EndpointScheduleOutcomeSuccess, "backup")
	if snapshot.Decisions[0].RuntimeOutcome != endpoint.EndpointScheduleRuntimeTryNext || snapshot.Decisions[1].RuntimeOutcome != endpoint.EndpointScheduleOutcomeSuccess {
		t.Fatalf("unexpected failover attempt decisions: %+v", snapshot.Decisions)
	}
	requireOnlyRetryObservation(t, tracker, 1)
}

// [Phase3 §11.2] auth failover 成功后 active 不迁移；auth cooldown 断言保留
func TestEndpointPipeline_Unauthorized_FailsOverWithoutMigratingActive(t *testing.T) {
	var primaryHits, backupHits int32
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&primaryHits, 1)
		http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
	}))
	t.Cleanup(primary.Close)
	backup := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&backupHits, 1)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(backup.Close)

	handler, manager := newEndpointPipelineTestHandler(t,
		endpointPipelineConfig("primary", primary.URL, 1),
		endpointPipelineConfig("backup", backup.URL, 2),
	)

	recorder := performEndpointPipelineRequest(t, handler, false)
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected backup success, got status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if got := atomic.LoadInt32(&primaryHits); got != 1 {
		t.Fatalf("expected primary to be called once, got %d", got)
	}
	if got := atomic.LoadInt32(&backupHits); got != 1 {
		t.Fatalf("expected backup to be called once, got %d", got)
	}
	if primaryEndpoint := manager.GetEndpointByNameAny("primary"); primaryEndpoint == nil || !primaryEndpoint.IsInCooldown() {
		t.Fatal("expected unauthorized primary endpoint to enter auth cooldown")
	}
}

func TestEndpointPipeline_ModelRewriteUsesOriginalBodyForEachCandidate(t *testing.T) {
	var primaryModel, backupModel string
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode primary body failed: %v", err)
		}
		primaryModel, _ = payload["model"].(string)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"model not found"}}`))
	}))
	t.Cleanup(primary.Close)
	backup := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode backup body failed: %v", err)
		}
		backupModel, _ = payload["model"].(string)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(backup.Close)

	primaryConfig := endpointPipelineConfig("primary", primary.URL, 1)
	primaryConfig.ModelRewriteRules = `[{"paths":["/v1/messages"],"match":"exact","from":"claude-test","to":"primary-model"}]`
	backupConfig := endpointPipelineConfig("backup", backup.URL, 2)
	backupConfig.ModelRewriteRules = `[{"paths":["/v1/messages"],"match":"exact","from":"claude-test","to":"backup-model"}]`
	handler, _ := newEndpointPipelineTestHandler(t, primaryConfig, backupConfig)

	recorder := performEndpointPipelineRequest(t, handler, false)
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected backup success, got status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if primaryModel != "primary-model" || backupModel != "backup-model" {
		t.Fatalf("expected endpoint-specific rewrites from original body, got primary=%q backup=%q", primaryModel, backupModel)
	}
}

func TestEndpointPipeline_Generic5xx_ReturnsRetryableErrorWithoutFailover(t *testing.T) {
	var primaryHits, backupHits int32
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&primaryHits, 1)
		http.Error(w, `{"error":"upstream unavailable"}`, http.StatusServiceUnavailable)
	}))
	t.Cleanup(primary.Close)
	backup := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&backupHits, 1)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(backup.Close)

	handler, manager := newEndpointPipelineTestHandler(t,
		endpointPipelineConfig("primary", primary.URL, 1),
		endpointPipelineConfig("backup", backup.URL, 2),
	)

	recorder := performEndpointPipelineRequest(t, handler, false)
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("expected generic 5xx to become 500, got status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if got := recorder.Header().Get("Retry-After"); got != "5" {
		t.Fatalf("expected Retry-After=5, got %q", got)
	}
	if got := atomic.LoadInt32(&primaryHits); got != 1 {
		t.Fatalf("expected primary to be called once, got %d", got)
	}
	if got := atomic.LoadInt32(&backupHits); got != 0 {
		t.Fatalf("expected no failover after ambiguous 5xx, backup hits=%d", got)
	}
	if got := manager.GetFailureStats()["primary"]; got != 1 {
		t.Fatalf("expected primary failure count 1, got %d", got)
	}
	requireLatestEndpointScheduleSnapshot(t, manager, endpoint.EndpointScheduleOutcomePassthroughError, "primary")
}

func TestEndpointPipeline_NotFound_PassesThroughWithoutFailover(t *testing.T) {
	var primaryHits, backupHits int32
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&primaryHits, 1)
		w.Header().Set("X-Upstream", "primary")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"not found"}`))
	}))
	t.Cleanup(primary.Close)
	backup := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&backupHits, 1)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(backup.Close)

	handler, manager := newEndpointPipelineTestHandler(t,
		endpointPipelineConfig("primary", primary.URL, 1),
		endpointPipelineConfig("backup", backup.URL, 2),
	)

	recorder := performEndpointPipelineRequest(t, handler, false)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("expected raw 404 passthrough, got status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if recorder.Header().Get("X-Upstream") != "primary" {
		t.Fatalf("expected upstream headers to be preserved, got %v", recorder.Header())
	}
	if !strings.Contains(recorder.Body.String(), `"error":"not found"`) {
		t.Fatalf("expected upstream body to be preserved, got %s", recorder.Body.String())
	}
	if got := atomic.LoadInt32(&primaryHits); got != 1 {
		t.Fatalf("expected primary to be called once, got %d", got)
	}
	if got := atomic.LoadInt32(&backupHits); got != 0 {
		t.Fatalf("expected no failover for 404, backup hits=%d", got)
	}
	if got := manager.GetFailureStats()["primary"]; got != 0 {
		t.Fatalf("expected 404 not to count as endpoint failure, got %d", got)
	}
	requireLatestEndpointScheduleSnapshot(t, manager, endpoint.EndpointScheduleOutcomePassthroughRaw, "primary")
}

func TestEndpointPipeline_QualityIncomplete_DoesNotMigrateActive(t *testing.T) {
	var backupHits int32
	backup := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&backupHits, 1)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: message_start\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_test\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"claude-test\",\"content\":[],\"usage\":{\"input_tokens\":10,\"output_tokens\":1}}}\n\n"))
		_, _ = w.Write([]byte("event: message_delta\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"input_tokens\":10,\"output_tokens\":5}}\n\n"))
	}))
	t.Cleanup(backup.Close)

	handler, manager := newEndpointPipelineTestHandler(t,
		endpointPipelineConfig("primary", endpointPipelineClosedURL(t), 1),
		endpointPipelineConfig("backup", backup.URL, 2),
	)

	recorder := performEndpointPipelineRequest(t, handler, true)
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected incomplete stream to remain a completed response, got status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if got := atomic.LoadInt32(&backupHits); got != 1 {
		t.Fatalf("expected backup to be called once, got %d", got)
	}
	if !strings.Contains(recorder.Body.String(), "event: message_delta") {
		t.Fatalf("expected incomplete upstream stream to be relayed, got %s", recorder.Body.String())
	}
	snapshot := requireLatestEndpointScheduleSnapshot(t, manager, endpoint.EndpointScheduleOutcomeQualityIncomplete, "backup")
	if snapshot.FinalError == "" {
		t.Fatal("expected incomplete stream reason in snapshot")
	}
}

func TestEndpointPipeline_NoCandidatesRecordsSnapshot(t *testing.T) {
	handler, manager := newEndpointPipelineTestHandler(t)

	recorder := performEndpointPipelineRequest(t, handler, false)
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 without candidates, got status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	snapshot := requireLatestEndpointScheduleSnapshot(t, manager, endpoint.EndpointScheduleOutcomeNoCandidates, "")
	if len(snapshot.Decisions) != 0 {
		t.Fatalf("expected no candidate decisions, got %+v", snapshot.Decisions)
	}
}

func TestEndpointPipeline_PrivacyBlockRecordsSnapshotWithoutFailover(t *testing.T) {
	var upstreamHits int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&upstreamHits, 1)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(upstream.Close)

	handler, manager := newEndpointPipelineTestHandler(t,
		endpointPipelineConfig("primary", upstream.URL, 1),
		endpointPipelineConfig("backup", upstream.URL, 2),
	)
	handler.SetPrivacyFilter(&stubPrivacyFilter{
		err: &privacy.PolicyError{StatusCode: http.StatusRequestEntityTooLarge, Code: privacy.CodeScanBodyTooLarge, Message: "scannable text too large"},
	})

	recorder := performEndpointPipelineRequest(t, handler, false)
	if recorder.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("expected privacy 413, got status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if got := atomic.LoadInt32(&upstreamHits); got != 0 {
		t.Fatalf("privacy block must not reach upstream, hits=%d", got)
	}
	requireLatestEndpointScheduleSnapshot(t, manager, endpoint.EndpointScheduleOutcomePrivacyBlocked, "primary")
}

// [Phase1 §9.2] 普通 429：同端点最多重试一次；两次均 429 只记一次 rate_limit 软失败，
// 阈值前不换端点，把规范化 429（保留 Retry-After）返回客户端，不进入冷却。
func TestEndpointPipeline_Ordinary429RetriesSameEndpointThenReturns429(t *testing.T) {
	var primaryHits, backupHits int32
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&primaryHits, 1)
		w.Header().Set("Retry-After", "1")
		http.Error(w, `{"error":{"type":"rate_limit_error"}}`, http.StatusTooManyRequests)
	}))
	t.Cleanup(primary.Close)
	backup := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&backupHits, 1)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(backup.Close)

	handler, manager := newEndpointPipelineTestHandler(t,
		endpointPipelineConfig("primary", primary.URL, 1),
		endpointPipelineConfig("backup", backup.URL, 2),
	)

	recorder := performEndpointPipelineRequest(t, handler, false)
	if recorder.Code != http.StatusTooManyRequests {
		t.Fatalf("expected normalized 429, got status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if got := recorder.Header().Get("Retry-After"); got != "1" {
		t.Fatalf("expected Retry-After preserved, got %q", got)
	}
	if got := atomic.LoadInt32(&primaryHits); got != 2 {
		t.Fatalf("expected exactly one same-endpoint retry (2 upstream hits), got %d", got)
	}
	if got := atomic.LoadInt32(&backupHits); got != 0 {
		t.Fatalf("threshold not reached: must not switch endpoint, backup hits=%d", got)
	}
	if primaryEndpoint := manager.GetEndpointByNameAny("primary"); primaryEndpoint.IsInCooldown() {
		t.Fatal("single logical 429 must not enter cooldown before threshold")
	}
	if got := manager.GetFailureStats()["primary"]; got != 1 {
		t.Fatalf("two upstream 429 must settle as one soft failure, got %d", got)
	}
	requireLatestEndpointScheduleSnapshot(t, manager, endpoint.EndpointScheduleOutcomeRateLimited, "primary")
}

// [Phase1 §9.2 规则 6] 首次 429 后本地重试成功：返回 200，本次不记软失败
func TestEndpointPipeline_Ordinary429RetrySucceedsWithoutSoftFailure(t *testing.T) {
	var primaryHits int32
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if atomic.AddInt32(&primaryHits, 1) == 1 {
			http.Error(w, `{"error":{"type":"rate_limit_error"}}`, http.StatusTooManyRequests)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(primary.Close)

	handler, manager := newEndpointPipelineTestHandler(t,
		endpointPipelineConfig("primary", primary.URL, 1),
	)

	tracker := attachRetryTracker(t, handler)

	recorder := performEndpointPipelineRequest(t, handler, false)
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected retry success 200, got status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if got := atomic.LoadInt32(&primaryHits); got != 2 {
		t.Fatalf("expected two upstream hits (429 then 200), got %d", got)
	}
	if got := manager.GetFailureStats()["primary"]; got != 0 {
		t.Fatalf("retry success must not record soft failure, got %d", got)
	}
	requireOnlyRetryObservation(t, tracker, 1)
}

// [Phase1 §9.2 规则 9] 第 3 个逻辑 429 达阈值：进入冷却并允许本请求内换下一候选
func TestEndpointPipeline_Ordinary429ThirdLogicalFailureTripsCooldownAndFailsOver(t *testing.T) {
	var primaryHits, backupHits int32
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&primaryHits, 1)
		http.Error(w, `{"error":{"type":"rate_limit_error"}}`, http.StatusTooManyRequests)
	}))
	t.Cleanup(primary.Close)
	backup := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&backupHits, 1)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(backup.Close)

	handler, manager := newEndpointPipelineTestHandler(t,
		endpointPipelineConfig("primary", primary.URL, 1),
		endpointPipelineConfig("backup", backup.URL, 2),
	)

	// 前两个逻辑失败（每个客户端请求只记一次）
	for i := 0; i < 2; i++ {
		recorder := performEndpointPipelineRequest(t, handler, false)
		if recorder.Code != http.StatusTooManyRequests {
			t.Fatalf("request %d: expected 429 before threshold, got %d", i+1, recorder.Code)
		}
	}
	if got := manager.GetFailureStats()["primary"]; got != 2 {
		t.Fatalf("expected 2 soft failures before threshold, got %d", got)
	}

	// 第 3 个逻辑失败触发阈值：冷却 + 换候选成功
	recorder := performEndpointPipelineRequest(t, handler, false)
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected threshold-triggered failover success, got status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if got := atomic.LoadInt32(&backupHits); got != 1 {
		t.Fatalf("expected backup hit after threshold, got %d", got)
	}
	if primaryEndpoint := manager.GetEndpointByNameAny("primary"); !primaryEndpoint.IsInCooldown() {
		t.Fatal("expected primary in cooldown after threshold")
	}
	if got := manager.GetFailureStats()["primary"]; got != 0 {
		t.Fatalf("threshold trip should clear the triggering window, got %d", got)
	}
}

// [Phase1 §8.5] 请求内尝试预算：最多尝试 3 个不同端点，不穿透剩余候选
func TestEndpointPipeline_CandidateAttemptBudgetStopsAtThree(t *testing.T) {
	var fourthHits int32
	fourth := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&fourthHits, 1)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(fourth.Close)

	handler, _ := newEndpointPipelineTestHandler(t,
		endpointPipelineConfig("e1", endpointPipelineClosedURL(t), 1),
		endpointPipelineConfig("e2", endpointPipelineClosedURL(t), 2),
		endpointPipelineConfig("e3", endpointPipelineClosedURL(t), 3),
		endpointPipelineConfig("e4", fourth.URL, 4),
	)

	recorder := performEndpointPipelineRequest(t, handler, false)
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 after budget exhausted, got status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if got := atomic.LoadInt32(&fourthHits); got != 0 {
		t.Fatalf("4th endpoint beyond budget must not be attempted, hits=%d", got)
	}
	if !strings.Contains(recorder.Body.String(), "budget exhausted") {
		t.Fatalf("expected budget exhausted reason, got %s", recorder.Body.String())
	}
}

// 首选端点软失败冷却时，自动与手动优选模式都继续使用健康备用端点。
func TestEndpointPipeline_SoftFailureCooldownFailsOverToBackup(t *testing.T) {
	for _, mode := range []string{endpoint.RouteModeAuto, endpoint.RouteModeManualPreferred} {
		t.Run(mode, func(t *testing.T) {
			var backupHits int32
			backup := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				atomic.AddInt32(&backupHits, 1)
				_, _ = w.Write([]byte(`{"ok":true}`))
			}))
			t.Cleanup(backup.Close)
			handler, manager := newEndpointPipelineTestHandler(t,
				endpointPipelineConfig("primary", endpointPipelineClosedURL(t), 1),
				endpointPipelineConfig("backup", backup.URL, 2),
			)
			manager.SetClaudeRoutingOverride(endpoint.RouteOverrideState{
				Mode: mode, EndpointName: "primary", FallbackEnabled: true,
			})
			manager.SetEndpointCooldown("primary", time.Minute,
				endpoint.SoftFailureCooldownReason(endpoint.SoftFailureCategoryRateLimit))
			recorder := performEndpointPipelineRequest(t, handler, false)
			if recorder.Code != http.StatusOK || atomic.LoadInt32(&backupHits) != 1 {
				t.Fatalf("首选冷却后应命中备用端点: status=%d hits=%d body=%s",
					recorder.Code, atomic.LoadInt32(&backupHits), recorder.Body.String())
			}
			requireLatestEndpointScheduleSnapshot(t, manager, endpoint.EndpointScheduleOutcomeSuccess, "backup")
		})
	}
}

// TestEndpointPipeline_StaleEpochSettlementDropped
// 场景:清除前发出的请求(旧 epoch 的 plan),清除后失败结算——冷却槽/软失败计数
// 三类状态零残留(auth / 软失败 / 429 三类结算全部被丢弃)
func TestEndpointPipeline_StaleEpochSettlementDropped(t *testing.T) {
	handler, manager := newEndpointPipelineTestHandler(t,
		endpointPipelineConfig("primary", "http://127.0.0.1:1", 1))
	profile := endpoint.BuildRouteRequestProfile("/v1/messages", []byte(`{"model":"claude-test","messages":[]}`))

	plan := manager.PrepareRouteCandidates(context.Background(), profile).Plans[0]
	oldEpoch := plan.FailureEpoch

	// 用户解除冷却:epoch 推进,旧 plan 作废
	if _, found := manager.ResetEndpointFailureState("primary"); !found {
		t.Fatal("reset endpoint failure state failed")
	}

	// 旧 epoch auth 结算
	handler.markEndpointFailure("primary", plan.ConfigRevision, oldEpoch, EndpointFailureDecision{
		Mark: EndpointMarkAuthCooldown, Reason: "auth_rejected",
	}, profile, "stale auth settlement")
	// 旧 epoch 软失败结算
	handler.recordEndpointAttemptSoftFailure("primary", plan.ConfigRevision, oldEpoch,
		endpoint.SoftFailureCategoryServerError, 0, "stale soft failure")
	// 旧 epoch 429 结算路径
	applied := handler.endpointManager.ApplyEndpointFailureSettlement("primary", plan.ConfigRevision, oldEpoch, func() {
		handler.endpointManager.RecordSoftFailureFenced("primary", endpoint.SoftFailureScopeMessages,
			endpoint.SoftFailureCategoryRateLimit, oldEpoch)
	})
	if applied {
		t.Fatal("旧 epoch 429 结算入口应被拒绝")
	}

	if in, _, reason := manager.GetEndpointCooldownInfo("primary"); in {
		t.Fatalf("旧 epoch 结算不应写冷却, reason=%s", reason)
	}
	if counts := manager.GetSoftFailureCounts("primary", endpoint.SoftFailureScopeMessages); len(counts) != 0 {
		t.Fatalf("旧 epoch 结算不应增加软失败计数: %+v", counts)
	}
}

// TestEndpointPipeline_NewEpochSettlementNormal
// 场景:解除冷却后重新规划(新 epoch),新请求失败结算正常记录、trip 正常冷却——自愈不破坏
func TestEndpointPipeline_NewEpochSettlementNormal(t *testing.T) {
	handler, manager := newEndpointPipelineTestHandler(t,
		endpointPipelineConfig("primary", "http://127.0.0.1:1", 1))
	profile := endpoint.BuildRouteRequestProfile("/v1/messages", []byte(`{"model":"claude-test","messages":[]}`))

	if _, found := manager.ResetEndpointFailureState("primary"); !found {
		t.Fatal("reset endpoint failure state failed")
	}
	plan := manager.PrepareRouteCandidates(context.Background(), profile).Plans[0]
	if plan.FailureEpoch != 1 {
		t.Fatalf("reset 后 plan 应携带新 epoch=1, got=%d", plan.FailureEpoch)
	}

	count, tripped := handler.recordEndpointAttemptSoftFailure("primary", plan.ConfigRevision, plan.FailureEpoch,
		endpoint.SoftFailureCategoryServerError, 0, "fresh failure")
	if !tripped && count != 1 {
		t.Fatalf("新 epoch 第一次结算应正常计数: count=%d tripped=%v", count, tripped)
	}
	handler.recordEndpointAttemptSoftFailure("primary", plan.ConfigRevision, plan.FailureEpoch,
		endpoint.SoftFailureCategoryServerError, 0, "fresh failure 2")
	_, tripped = handler.recordEndpointAttemptSoftFailure("primary", plan.ConfigRevision, plan.FailureEpoch,
		endpoint.SoftFailureCategoryServerError, 0, "fresh failure 3")
	if !tripped {
		t.Fatal("新 epoch 达阈值应 trip")
	}
	if !manager.IsEndpointInCooldown("primary") {
		t.Fatal("新 epoch trip 后应进入冷却(自愈保护不破坏)")
	}
}

// runEndpointPipelineWithLifecycle 直接驱动管线并回收生命周期内部状态。
// usageTracker 传 nil：落库调用被守卫跳过，但归属字段仍写入内部状态，
// 从而在不起 SQLite 的前提下断言「追踪记录会拿到什么上游」。
func runEndpointPipelineWithLifecycle(t *testing.T, handler *Handler) (*httptest.ResponseRecorder, lifecycleStateSnapshot) {
	t.Helper()

	body := `{"model":"claude-test","messages":[]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")

	recorder := httptest.NewRecorder()
	lifecycleManager := NewRequestLifecycleManager(nil, nil, "req-endpoint-pipeline", nil)
	handler.handleEndpointPipeline(req.Context(), recorder, req, []byte(body), lifecycleManager, false)
	return recorder, lifecycleManager.snapshotState()
}

// manual_fixed 被 block 时端点是已知的，必须落进上游归属；
// effectiveEndpoint 仍为空——本次并未真正打到上游，两者语义不同不可合并。
func TestEndpointPipeline_ManualFixedBlock_RecordsBlockedEndpointAsUpstream(t *testing.T) {
	handler, manager := newEndpointPipelineTestHandler(t,
		endpointPipelineConfig("primary", endpointPipelineClosedURL(t), 1))
	manager.SetClaudeRoutingOverride(endpoint.RouteOverrideState{
		Mode:         endpoint.RouteModeManualFixed,
		EndpointName: "primary",
	})
	manager.SetEndpointCooldown("primary", time.Minute,
		endpoint.SoftFailureCooldownReason(endpoint.SoftFailureCategoryRateLimit))

	_, state := runEndpointPipelineWithLifecycle(t, handler)
	if state.endpointName != "primary" || state.upstreamName != "primary" {
		t.Fatalf("被 block 端点须落进上游归属: endpoint=%q upstream=%q", state.endpointName, state.upstreamName)
	}
	if state.effectiveEndpoint != "" {
		t.Fatalf("block 未真正打到上游，effectiveEndpoint 须留空, got=%q", state.effectiveEndpoint)
	}
	if state.routeMode != endpoint.RouteModeManualFixed || state.requestedEndpoint != "primary" {
		t.Fatalf("路由上下文丢失: mode=%q requested=%q", state.routeMode, state.requestedEndpoint)
	}
}

// 候选为空时确实没有端点可归属，但路由模式必须落真实值：
// 不补则 route_mode 落库为默认 auto，manual_preferred 期间的失败会被误读成自动调度。
func TestEndpointPipeline_NoCandidates_PreservesRouteContext(t *testing.T) {
	handler, manager := newEndpointPipelineTestHandler(t,
		endpointPipelineConfig("primary", endpointPipelineClosedURL(t), 1))
	manager.SetClaudeRoutingOverride(endpoint.RouteOverrideState{
		Mode:            endpoint.RouteModeManualPreferred,
		EndpointName:    "primary",
		FallbackEnabled: true,
	})
	manager.SetEndpointCooldown("primary", time.Minute,
		endpoint.SoftFailureCooldownReason(endpoint.SoftFailureCategoryRateLimit))

	recorder, state := runEndpointPipelineWithLifecycle(t, handler)
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 without candidates, got status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if state.endpointName != "" || state.upstreamName != "" {
		t.Fatalf("无候选时不得伪造上游归属: endpoint=%q upstream=%q", state.endpointName, state.upstreamName)
	}
	if state.routeMode != endpoint.RouteModeManualPreferred || state.requestedEndpoint != "primary" {
		t.Fatalf("route_mode 须落真实值而非默认 auto: mode=%q requested=%q", state.routeMode, state.requestedEndpoint)
	}
	if state.fallbackReason != "no_routable_candidates" {
		t.Fatalf("unexpected fallback reason: %q", state.fallbackReason)
	}
	if state.routeDecisionAt.IsZero() {
		t.Fatal("routeDecisionAt 为零值时 attachRouteDiagnostics 会整体跳过，路由诊断仍会丢失")
	}
}

// 无候选且路由为 auto 时不得污染全局 LastEffectiveEndpoint——
// NoteRouteDecision 会把它清空并触发前端事件，本请求诊断不该有这种副作用。
func TestEndpointPipeline_NoCandidates_DoesNotClearGlobalRouteDecision(t *testing.T) {
	handler, manager := newEndpointPipelineTestHandler(t,
		endpointPipelineConfig("primary", endpointPipelineClosedURL(t), 1))
	manager.NoteRouteDecision("primary", "")
	manager.SetEndpointCooldown("primary", time.Minute,
		endpoint.SoftFailureCooldownReason(endpoint.SoftFailureCategoryRateLimit))

	if _, state := runEndpointPipelineWithLifecycle(t, handler); state.routeMode != endpoint.RouteModeAuto {
		t.Fatalf("expected auto route mode, got %q", state.routeMode)
	}
	if got := manager.GetClaudeRoutingOverride().LastEffectiveEndpoint; got != "primary" {
		t.Fatalf("全局 LastEffectiveEndpoint 被本请求诊断改写: %q", got)
	}
}

func TestEndpointPipeline_SoftFailurePolicy_429保留短重试并按独立阈值切换(t *testing.T) {
	var primaryHits, backupHits int32
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&primaryHits, 1)
		http.Error(w, "rate limited", http.StatusTooManyRequests)
	}))
	t.Cleanup(primary.Close)
	backup := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&backupHits, 1)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(backup.Close)
	window, threshold := 10, 2
	primaryConfig := endpointPipelineConfig("policy-primary", primary.URL, 1)
	primaryConfig.SoftFailureWindowMinutes, primaryConfig.SoftFailureThreshold = &window, &threshold
	handler, manager := newEndpointPipelineTestHandler(t, primaryConfig, endpointPipelineConfig("policy-backup", backup.URL, 2))
	first := performEndpointPipelineRequest(t, handler, false)
	if first.Code != http.StatusTooManyRequests || atomic.LoadInt32(&primaryHits) != 2 || atomic.LoadInt32(&backupHits) != 0 {
		t.Fatal("第一次逻辑失败应只短重试一次")
	}
	if manager.GetFailureStats()["policy-primary"] != 1 {
		t.Fatal("一次请求的两次 429 必须只计一次失败")
	}
	second := performEndpointPipelineRequest(t, handler, false)
	if second.Code != http.StatusOK || atomic.LoadInt32(&primaryHits) != 4 || atomic.LoadInt32(&backupHits) != 1 {
		t.Fatalf("第二次逻辑失败未按独立阈值切换: %d", second.Code)
	}
}

func TestEndpointPipeline_SoftFailurePolicy_5xx仍结束当前请求(t *testing.T) {
	var backupHits int32
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "busy", http.StatusServiceUnavailable) }))
	t.Cleanup(primary.Close)
	backup := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&backupHits, 1)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(backup.Close)
	window, threshold := 10, 1
	primaryConfig := endpointPipelineConfig("policy-primary", primary.URL, 1)
	primaryConfig.SoftFailureWindowMinutes, primaryConfig.SoftFailureThreshold = &window, &threshold
	handler, manager := newEndpointPipelineTestHandler(t, primaryConfig, endpointPipelineConfig("policy-backup", backup.URL, 2))
	first := performEndpointPipelineRequest(t, handler, false)
	if first.Code != http.StatusInternalServerError || atomic.LoadInt32(&backupHits) != 0 {
		t.Fatal("5xx 不应在当前请求内重放")
	}
	if !manager.GetEndpointByNameAny("policy-primary").IsInCooldown() {
		t.Fatal("自定义阈值未触发冷却")
	}
	second := performEndpointPipelineRequest(t, handler, false)
	if second.Code != http.StatusOK || atomic.LoadInt32(&backupHits) != 1 {
		t.Fatal("后续请求未跳过已冷却端点")
	}
}

func TestEndpointPipeline_StreamingClientErrorHasSingleContentType(t *testing.T) {
	vendor := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"type":"error","error":{"type":"invalid_request_error","message":"bad"}}`))
	}))
	defer vendor.Close()

	handler, _ := newEndpointPipelineTestHandler(t, endpointPipelineConfig("vendor", vendor.URL, 1))
	recorder := performEndpointPipelineRequest(t, handler, true)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("普通 4xx 应原样透传，got %d", recorder.Code)
	}
	if values := recorder.Header().Values("Content-Type"); len(values) != 1 || values[0] != "application/json" {
		t.Fatalf("流式请求透传 4xx 时 Content-Type 只能是上游值，got %v", values)
	}
}
