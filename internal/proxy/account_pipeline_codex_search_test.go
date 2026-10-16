package proxy

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"sinuo/internal/service"
	"sinuo/internal/store"
)

func newCodexSearchPipeline(t *testing.T, configured bool) (*Handler, *mockAccountPoolService, *service.CodexModelRoutingService, []*store.UpstreamAccountRecord) {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "search-routing.db"))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	accounts := []*store.UpstreamAccountRecord{
		{ID: 1, AccountName: "mapped-oauth", ProviderType: "chatgpt_refresh_token", CredentialRaw: `{"refresh_token":"rt-test","access_token":"at-mapped","expires_at":"2099-01-01T00:00:00Z","chatgpt_account_id":"test-account"}`, Enabled: true},
		{ID: 2, AccountName: "global-api", ProviderType: "api_key", CredentialRaw: "test-global-api", BaseURL: "https://global.invalid", Enabled: true},
	}
	router := service.NewCodexModelRoutingService(store.NewSQLiteModelRoutingStore(db), func(context.Context, bool) ([]*store.UpstreamAccountRecord, error) { return accounts, nil })
	if err := router.Initialize(context.Background()); err != nil {
		t.Fatal(err)
	}
	if configured {
		if _, err := router.CreateBinding(context.Background(), service.CodexModelRouteBindingInput{Kind: "search", AccountIDs: []int64{1}, RouteMode: "auto", Enabled: true}); err != nil {
			t.Fatal(err)
		}
	}
	pool := &mockAccountPoolService{accounts: accounts[1:]}
	h := newAccountPipelineTestHandler(t, "https://endpoint.invalid", pool)
	h.SetCodexModelRouter(router)
	h.accountHTTPInitOnce.Do(func() {})
	h.accountHTTPClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"id":"test","status":"completed","output":[],"results":[]}`)), Request: req}, nil
	})}
	h.accountSSEHTTPClient = h.accountHTTPClient
	return h, pool, router, accounts
}

func TestCodexSearchPipeline_OffSwitchRoutesSearchAndKeepsResponsesGlobal(t *testing.T) {
	h, pool, router, _ := newCodexSearchPipeline(t, true)
	if router.Enabled() {
		t.Fatal("测试应关闭模型路由")
	}
	var authorizations []string
	base := h.accountHTTPClient.Transport
	h.accountHTTPClient.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		authorizations = append(authorizations, req.Header.Get("Authorization"))
		return base.RoundTrip(req)
	})
	rec := performStandaloneSearchRequest(t, h)
	if rec.Code != 200 || len(pool.prepareCalls) != 0 || len(authorizations) != 1 || authorizations[0] != "Bearer at-mapped" {
		t.Fatalf("搜索必须只用映射账号: %d %s %+v %+v", rec.Code, rec.Body, pool.prepareCalls, authorizations)
	}
	if len(pool.savedDrafts) != 1 || pool.savedDrafts[0].ModelRoutingFamily != "codex" || pool.savedDrafts[0].SelectedAccountID != 1 || pool.savedDrafts[0].RequestModel != "gpt-5.6" {
		t.Fatalf("映射快照应记录请求模型和账号: %+v", pool.savedDrafts)
	}
	rec = performResponsesRequest(t, h)
	if rec.Code != 200 || len(pool.prepareCalls) != 1 || len(authorizations) != 2 || authorizations[1] != "Bearer test-global-api" {
		t.Fatalf("普通响应仍用全局: %d %s %+v %+v", rec.Code, rec.Body, pool.prepareCalls, authorizations)
	}
}

func TestCodexSearchPipeline_UnconfiguredKeepsOAuthUnavailable(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		h, pool, router, accounts := newCodexSearchPipeline(t, false)
		if err := router.UpdateEnabled(context.Background(), enabled); err != nil {
			t.Fatal(err)
		}
		pool.accounts = nil
		rec := performStandaloneSearchRequest(t, h)
		if rec.Code != 503 || !strings.Contains(rec.Body.String(), "codex_search_oauth_unavailable") || len(pool.prepareCalls) != 1 {
			t.Fatalf("%d %s %+v", rec.Code, rec.Body, pool.prepareCalls)
		}
		pool.accounts = accounts[:1]
		rec = performStandaloneSearchRequest(t, h)
		if rec.Code != 200 || len(pool.prepareCalls) != 2 {
			t.Fatalf("未配置时仍按全局 OAuth 编排: %d %s", rec.Code, rec.Body)
		}
	}
}

func TestCodexSearchPipeline_UnavailableNeverFallsBack(t *testing.T) {
	for _, mode := range []string{"auto", "manual_fixed"} {
		h, pool, router, accounts := newCodexSearchPipeline(t, true)
		list, err := router.ListBindings(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		input := service.CodexModelRouteBindingInput{Kind: "search", AccountIDs: []int64{1}, RouteMode: mode, Enabled: true}
		if mode == "manual_fixed" {
			input.RouteTargetID = 1
		}
		if err := router.UpdateBinding(context.Background(), list[0].AssignmentID, input); err != nil {
			t.Fatal(err)
		}
		accounts[0].Enabled = false
		rec := performStandaloneSearchRequest(t, h)
		want := "联网搜索映射中没有可用账号"
		if mode == "manual_fixed" {
			want = "联网搜索映射的固定账号不可用"
		}
		if rec.Code != 503 || !strings.Contains(rec.Body.String(), want) || len(pool.prepareCalls) != 0 || strings.Contains(rec.Body.String(), "codex_search_oauth_unavailable") {
			t.Fatalf("%d %s %+v", rec.Code, rec.Body, pool.prepareCalls)
		}
	}
}

type codexSearchLoadFailureStore struct{ store.ModelRoutingStore }

func (s codexSearchLoadFailureStore) ListAssignments(context.Context) ([]*store.ModelRoutingAssignmentRecord, error) {
	return nil, errors.New("test routing load failure")
}

func TestCodexSearchPipeline_LoadFailureRespectsSwitch(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		h, pool, _, _ := newCodexSearchPipeline(t, false)
		db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "failed.db"))
		if err != nil {
			t.Fatal(err)
		}
		db.SetMaxOpenConns(1)
		t.Cleanup(func() { _ = db.Close() })
		base := store.NewSQLiteModelRoutingStore(db)
		if err := base.UpdateSettings(context.Background(), store.ModelRoutingSettingsPatch{CodexEnabled: &enabled}); err != nil {
			t.Fatal(err)
		}
		router := service.NewCodexModelRoutingService(codexSearchLoadFailureStore{base}, nil)
		if err := router.Initialize(context.Background()); err == nil {
			t.Fatal("加载应失败")
		}
		h.SetCodexModelRouter(router)
		pool.accounts = nil
		rec := performStandaloneSearchRequest(t, h)
		if enabled {
			if rec.Code != 422 || !strings.Contains(rec.Body.String(), "model_routing_load_failed") || len(pool.prepareCalls) != 0 {
				t.Fatalf("%d %s", rec.Code, rec.Body)
			}
		} else if rec.Code != 503 || !strings.Contains(rec.Body.String(), "codex_search_oauth_unavailable") || len(pool.prepareCalls) != 1 {
			t.Fatalf("%d %s", rec.Code, rec.Body)
		}
	}
}
