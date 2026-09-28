package proxy

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// claudeUsageFetchStub 记录每次查询用的凭据；gate 非 nil 时查询阻塞到放行
type claudeUsageFetchStub struct {
	calls chan claudeUsageCredential
	mu    sync.Mutex
	err   error
	gate  chan struct{}
}

func newClaudeUsageFetchStub() *claudeUsageFetchStub {
	return &claudeUsageFetchStub{calls: make(chan claudeUsageCredential, 16)}
}

func (s *claudeUsageFetchStub) set(err error, gate chan struct{}) {
	s.mu.Lock()
	s.err, s.gate = err, gate
	s.mu.Unlock()
}

func (s *claudeUsageFetchStub) fetch(_ context.Context, credential claudeUsageCredential) (ClaudeSubscriptionUsage, error) {
	s.mu.Lock()
	err, gate := s.err, s.gate
	s.mu.Unlock()
	s.calls <- credential
	if gate != nil {
		<-gate
	}
	if err != nil {
		return ClaudeSubscriptionUsage{}, err
	}
	used := 12.0
	return ClaudeSubscriptionUsage{FiveHour: ClaudeSubscriptionQuotaWindow{UsedPercent: &used}, FetchedAt: time.Now().UTC()}, nil
}

func (s *claudeUsageFetchStub) waitCall(t *testing.T) claudeUsageCredential {
	t.Helper()
	select {
	case credential := <-s.calls:
		return credential
	case <-time.After(2 * time.Second):
		t.Fatal("应发起额度查询")
		return claudeUsageCredential{}
	}
}

func (s *claudeUsageFetchStub) expectNoCall(t *testing.T, wait time.Duration, reason string) {
	t.Helper()
	select {
	case <-s.calls:
		t.Fatal(reason)
	case <-time.After(wait):
	}
}

func testClaudeUsagePolicy() claudeUsagePolicy {
	return claudeUsagePolicy{
		idleDelay:      40 * time.Millisecond,
		minInterval:    400 * time.Millisecond,
		manualCooldown: 200 * time.Millisecond,
		credentialTTL:  10 * time.Second,
		defaultBackoff: 500 * time.Millisecond,
		requestTimeout: time.Second,
	}
}

// newTestClaudeQuota 返回已开启的额度模块；开关本身的行为由 InactiveOutsideSubscriptionMode 覆盖
func newTestClaudeQuota(policy claudeUsagePolicy) (*claudeSubscriptionQuota, *claudeUsageFetchStub) {
	stub := newClaudeUsageFetchStub()
	q := newClaudeSubscriptionQuota(stub.fetch, policy)
	q.setActive(true)
	return q, stub
}

func (q *claudeSubscriptionQuota) testState() ClaudeSubscriptionQuotaState {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.stateLocked()
}

// waitClaudeQuotaState 查询结果在 fetch 返回后才写回，轮询等待
func waitClaudeQuotaState(t *testing.T, q *claudeSubscriptionQuota, label string, ok func(ClaudeSubscriptionQuotaState) bool) ClaudeSubscriptionQuotaState {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		state := q.testState()
		if ok(state) {
			return state
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s，当前状态 %+v", label, state)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestClaudeSubscriptionQuota_QueriesOnceAfterAllRequestsGoIdle(t *testing.T) {
	q, stub := newTestClaudeQuota(testClaudeUsagePolicy())
	q.beginRequest("Bearer old", "claude-cli/1")
	q.beginRequest("Bearer new", "claude-cli/2")
	q.endRequest()
	stub.expectNoCall(t, 120*time.Millisecond, "仍有进行中的订阅请求时不应补查")

	q.endRequest()
	if got := stub.waitCall(t); got.authorization != "Bearer new" || got.userAgent != "claude-cli/2" {
		t.Fatalf("应使用最近一次请求的凭据与 UA，got %+v", got)
	}
	state := waitClaudeQuotaState(t, q, "查询结果应写回", func(s ClaudeSubscriptionQuotaState) bool { return s.Usage != nil })
	if state.LastQueriedAt == nil || state.RefreshAvailableAt == nil || state.LastError != "" {
		t.Fatalf("查询后应记录查询时间与下次可刷新时间: %+v", state)
	}
	stub.expectNoCall(t, 150*time.Millisecond, "一轮请求结束后只应补查一次")
}

func TestClaudeSubscriptionQuota_NewRequestRestartsIdleTimer(t *testing.T) {
	policy := testClaudeUsagePolicy()
	policy.idleDelay = 150 * time.Millisecond
	q, stub := newTestClaudeQuota(policy)

	q.beginRequest("Bearer a", "")
	q.endRequest()
	time.Sleep(100 * time.Millisecond)
	q.beginRequest("Bearer a", "")
	q.endRequest()
	stub.expectNoCall(t, 100*time.Millisecond, "静默期内来了新请求，应重新计时")
	stub.waitCall(t)
}

func TestClaudeSubscriptionQuota_DefersAutoQueryWithinMinInterval(t *testing.T) {
	q, stub := newTestClaudeQuota(testClaudeUsagePolicy())
	q.beginRequest("Bearer a", "")
	q.endRequest()
	stub.waitCall(t)
	waitClaudeQuotaState(t, q, "第一次查询应完成", func(s ClaudeSubscriptionQuotaState) bool { return s.Usage != nil })

	q.beginRequest("Bearer a", "")
	q.endRequest()
	stub.expectNoCall(t, 200*time.Millisecond, "距上次查询不足最小间隔时应推迟")
	stub.waitCall(t)
	stub.expectNoCall(t, 150*time.Millisecond, "推迟后只补查一次")
}

func TestClaudeSubscriptionQuota_SkipsIdleQueryWhenRefreshedAfterLastRequest(t *testing.T) {
	q, stub := newTestClaudeQuota(testClaudeUsagePolicy())
	q.beginRequest("Bearer a", "")
	q.endRequest()
	if _, err := q.refresh(); err != nil {
		t.Fatalf("首次手动刷新应立即可用: %v", err)
	}
	stub.waitCall(t)
	stub.expectNoCall(t, 150*time.Millisecond, "请求结束后已手动查过，静默期满不应再补查")
}

func TestClaudeSubscriptionQuota_ManualRefreshGuards(t *testing.T) {
	q, stub := newTestClaudeQuota(testClaudeUsagePolicy())
	if _, err := q.refresh(); !errors.Is(err, errClaudeUsageNoCredential) {
		t.Fatalf("没有凭据时应拒绝刷新，got %v", err)
	}

	q.beginRequest("Bearer a", "")
	gate := make(chan struct{})
	stub.set(nil, gate)
	done := make(chan error, 1)
	go func() {
		_, err := q.refresh()
		done <- err
	}()
	stub.waitCall(t)
	if _, err := q.refresh(); !errors.Is(err, errClaudeUsageBusy) {
		t.Fatalf("查询进行中应拒绝重复刷新，got %v", err)
	}
	close(gate)
	if err := <-done; err != nil {
		t.Fatalf("第一次刷新应成功: %v", err)
	}

	stub.set(nil, nil)
	var cooldown *claudeUsageCooldownError
	if _, err := q.refresh(); !errors.As(err, &cooldown) {
		t.Fatalf("冷却期内应拒绝刷新，got %v", err)
	}
	time.Sleep(testClaudeUsagePolicy().manualCooldown)
	if _, err := q.refresh(); err != nil {
		t.Fatalf("冷却结束后应允许刷新: %v", err)
	}
	stub.waitCall(t)
}

func TestClaudeSubscriptionQuota_UnauthorizedDropsOnlyTheRejectedCredential(t *testing.T) {
	q, stub := newTestClaudeQuota(testClaudeUsagePolicy())
	stub.set(&claudeUsageStatusError{status: http.StatusUnauthorized}, nil)
	q.beginRequest("Bearer expired", "")
	state, err := q.refresh()
	if err != nil || state.CredentialAvailable || !strings.Contains(state.LastError, "失效") {
		t.Fatalf("401 应丢弃凭据并提示失效: err=%v %+v", err, state)
	}
	stub.waitCall(t)

	time.Sleep(testClaudeUsagePolicy().manualCooldown)
	gate := make(chan struct{})
	stub.set(&claudeUsageStatusError{status: http.StatusForbidden}, gate)
	q.beginRequest("Bearer stale", "")
	done := make(chan ClaudeSubscriptionQuotaState, 1)
	go func() {
		state, _ := q.refresh()
		done <- state
	}()
	stub.waitCall(t)
	q.beginRequest("Bearer fresh", "")
	close(gate)
	if state := <-done; !state.CredentialAvailable {
		t.Fatalf("查询期间凭据已换新，不应丢弃新凭据: %+v", state)
	}
}

func TestClaudeSubscriptionQuota_RateLimitedBacksOff(t *testing.T) {
	q, stub := newTestClaudeQuota(testClaudeUsagePolicy())
	stub.set(&claudeUsageStatusError{status: http.StatusTooManyRequests, retryAfter: 2 * time.Second}, nil)
	q.beginRequest("Bearer a", "")
	state, err := q.refresh()
	if err != nil || state.RefreshAvailableAt == nil || time.Until(*state.RefreshAvailableAt) < time.Second {
		t.Fatalf("429 应按 Retry-After 推迟下次可刷新时间: err=%v %+v", err, state)
	}
	time.Sleep(testClaudeUsagePolicy().manualCooldown)
	var cooldown *claudeUsageCooldownError
	if _, err := q.refresh(); !errors.As(err, &cooldown) {
		t.Fatalf("退避期内即使过了手动冷却也应拒绝，got %v", err)
	}
	stub.waitCall(t)
	stub.expectNoCall(t, 50*time.Millisecond, "退避期内不应再查询")
}

func TestClaudeSubscriptionQuota_CredentialExpiresAfterIdleTTL(t *testing.T) {
	policy := testClaudeUsagePolicy()
	policy.idleDelay = time.Hour
	policy.credentialTTL = 120 * time.Millisecond
	q, _ := newTestClaudeQuota(policy)
	notified := make(chan ClaudeSubscriptionQuotaState, 8)
	q.onChange = func(state ClaudeSubscriptionQuotaState) { notified <- state }

	q.beginRequest("Bearer a", "")
	q.endRequest()
	time.Sleep(60 * time.Millisecond)
	q.beginRequest("Bearer a", "")
	time.Sleep(150 * time.Millisecond)
	if !q.testState().CredentialAvailable {
		t.Fatal("有进行中的请求时凭据不应过期")
	}
	q.endRequest()
	waitClaudeQuotaState(t, q, "最后一个请求结束满 TTL 后应丢弃凭据", func(s ClaudeSubscriptionQuotaState) bool { return !s.CredentialAvailable })

	states := drainClaudeQuotaStates(notified)
	if len(states) != 2 || !states[0].CredentialAvailable || states[1].CredentialAvailable || states[1].Revision <= states[0].Revision {
		t.Fatalf("应通知拿到凭据与凭据过期两次，revision 递增，got %+v", states)
	}
}

func TestClaudeSubscriptionQuota_InactiveOutsideSubscriptionMode(t *testing.T) {
	policy := testClaudeUsagePolicy()
	policy.idleDelay = 150 * time.Millisecond
	stub := newClaudeUsageFetchStub()
	q := newClaudeSubscriptionQuota(stub.fetch, policy)

	q.beginRequest("Bearer a", "")
	q.endRequest()
	if q.testState().CredentialAvailable {
		t.Fatal("默认关闭时不应记下凭据")
	}
	if _, err := q.refresh(); !errors.Is(err, errClaudeUsageInactive) {
		t.Fatalf("关闭时应拒绝手动刷新，got %v", err)
	}
	stub.expectNoCall(t, 250*time.Millisecond, "关闭时请求结束后不应补查")

	notified := make(chan ClaudeSubscriptionQuotaState, 8)
	q.onChange = func(state ClaudeSubscriptionQuotaState) { notified <- state }
	q.setActive(true)
	q.beginRequest("Bearer b", "")
	q.endRequest()
	q.setActive(false)
	if states := drainClaudeQuotaStates(notified); len(states) != 2 || !states[0].CredentialAvailable || states[1].CredentialAvailable {
		t.Fatalf("切走时应立即丢弃凭据并通知，got %+v", states)
	}
	stub.expectNoCall(t, 250*time.Millisecond, "切走后已排好的补查不应再发出")

	q.setActive(true)
	q.beginRequest("Bearer c", "")
	q.setActive(false)
	q.endRequest()
	q.setActive(true)
	if q.testState().CredentialAvailable {
		t.Fatal("切回后要等下一次订阅请求才有凭据")
	}
	stub.expectNoCall(t, 250*time.Millisecond, "切走后才结束的在途请求不应排补查")

	q.beginRequest("Bearer d", "")
	q.endRequest()
	if got := stub.waitCall(t); got.authorization != "Bearer d" {
		t.Fatalf("切回后应按新请求的凭据补查，got %+v", got)
	}
}

func TestClaudeSubscription_IdleQueryUsesClientCredentialAgainstOfficialUsage(t *testing.T) {
	usageRequests := make(chan *http.Request, 4)
	subscription := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/oauth/usage" {
			usageRequests <- r.Clone(context.Background())
			_, _ = w.Write([]byte(`{"five_hour":{"utilization":37.5,"resets_at":"2026-09-28T05:00:00Z"},"seven_day":{"utilization":61,"resets_at":null}}`))
			return
		}
		_, _ = w.Write([]byte(`{"id":"msg_1","type":"message","content":[],"usage":{"input_tokens":1,"output_tokens":1}}`))
	}))
	defer subscription.Close()

	handler, _, _ := newClaudeSubscriptionTestHandler(t, subscription.URL)
	handler.claudeQuota = newClaudeSubscriptionQuota(handler.fetchClaudeSubscriptionUsage, testClaudeUsagePolicy())
	handler.SetClaudeSubscriptionQuotaActive(true)
	performClaudeSubscriptionRequest(t, handler, claudeSubscriptionRequest{requestID: "req-usage-idle", userAgent: "claude-cli/2.1.283 (external, cli)"})

	var got *http.Request
	select {
	case got = <-usageRequests:
	case <-time.After(2 * time.Second):
		t.Fatal("请求结束静默后应查询官方额度")
	}
	if got.Method != http.MethodGet || got.Header.Get("Authorization") != claudeSubscriptionTestToken ||
		got.Header.Get("anthropic-beta") != "oauth-2025-04-20" || got.Header.Get("User-Agent") != "claude-cli/2.1.283 (external, cli)" {
		t.Fatalf("额度查询应带客户端凭据、OAuth beta 与客户端 UA，got %s %v", got.Method, got.Header)
	}
	if got.Header.Get("X-Api-Key") != "" {
		t.Fatal("额度查询不应带上客户端的其他请求头")
	}
	state := waitClaudeQuotaState(t, handler.claudeQuota, "查询结果应写回", func(s ClaudeSubscriptionQuotaState) bool { return s.Usage != nil })
	assertClaudeRateLimitPercent(t, "5h 已用", state.Usage.FiveHour.UsedPercent, 37.5)
}
