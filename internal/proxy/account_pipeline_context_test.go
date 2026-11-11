package proxy

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"sinuo/internal/privacy"
	"sinuo/internal/store"

	"github.com/tidwall/gjson"
)

// 通过两个真实入口验证同一出站规则，不依赖账号的标准 Responses 兼容开关。
func TestAccountPipeline_CodexHTTPContextBothEntrances(t *testing.T) {
	for _, subscription := range []bool{false, true} {
		for _, provider := range []string{"api_key", "chatgpt_refresh_token"} {
			for _, streaming := range []bool{false, true} {
				for _, stripAndCompress := range []bool{false, true} {
					t.Run(fmt.Sprintf("subscription=%v/%s/stream=%v/strip-zstd=%v", subscription, provider, streaming, stripAndCompress), func(t *testing.T) {
						testCodexHTTPContextEntrance(t, subscription, provider, "/v1/responses", streaming, stripAndCompress, false)
					})
				}
			}
		}
	}
	for _, path := range []string{"/v1/responses", "/v1/responses/compact"} {
		t.Run("privacy-model-rewrite"+path, func(t *testing.T) {
			testCodexHTTPContextEntrance(t, false, "api_key", path, false, true, true)
		})
	}
	t.Run("subscription-privacy-model-rewrite", func(t *testing.T) {
		testCodexHTTPContextEntrance(t, true, "api_key", "/v1/responses", false, true, true)
	})
}

func testCodexHTTPContextEntrance(t *testing.T, subscription bool, provider, path string, streaming, stripAndCompress, transforms bool) {
	t.Helper()
	received := make(chan metadataWireRequest, 2)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
		}
		received <- metadataWireRequest{body: body, encoding: r.Header.Get("Content-Encoding"), contentLength: r.ContentLength}
		if streaming {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"fixture\",\"status\":\"completed\",\"output\":[]}}\n\n")
		} else {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"id":"fixture","status":"completed","output":[]}`)
		}
	}))
	defer upstream.Close()
	credential := "fixture-credential"
	if provider == "chatgpt_refresh_token" {
		credential = `{"refresh_token":"fixture-refresh","access_token":"fixture-access","expires_at":"2099-01-01T00:00:00Z","chatgpt_account_id":"fixture-account"}`
	}
	account := &store.UpstreamAccountRecord{ID: 1, ProviderType: provider, AccountName: "context-fixture", CredentialRaw: credential,
		BaseURL: upstream.URL, Enabled: true, StripCodexInternalMetadata: stripAndCompress, EnableRequestCompression: stripAndCompress}
	if transforms {
		account.ModelRewriteRules = testGPT54ModelRewriteRules
	}
	env := newSubscriptionPoolTestEnv(t, []*store.UpstreamAccountRecord{account}, true)
	handler := env.handler.pool.(*subscriptionPoolAdapter).proxy
	routeMetadataFixtureTransport(t, handler, upstream.URL)
	if transforms {
		filter := newProxyTestPrivacyService(t)
		settings, err := filter.GetSettings(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		settings.Mode = privacy.ModeRedact
		if _, err := filter.UpdateSettings(context.Background(), settings); err != nil {
			t.Fatal(err)
		}
		if _, err := filter.CreateRule(context.Background(), &store.PrivacyRuleRecord{Enabled: true, Name: "context-output", Priority: 1,
			MatchType: "regex", Pattern: "Companion Page snapshot", Placeholder: "redacted-context", Action: "redact",
			ScopeJSON: `{"paths":["/v1/responses","/v1/responses/compact"]}`}); err != nil {
			t.Fatal(err)
		}
		handler.SetPrivacyFilter(filter)
	}
	body := []byte(strings.Replace(codexClientContextFixture, `"model"`, fmt.Sprintf(`"stream":%v,"model"`, streaming), 1))
	wire := body
	if stripAndCompress {
		wire = mustEncodeZstd(t, body)
	}
	var previous []byte
	for attempt := 0; attempt < 2; attempt++ {
		mutate := func(req *http.Request) {
			req.Body = io.NopCloser(bytes.NewReader(wire))
			req.ContentLength = int64(len(wire))
			req.Header.Set("Content-Type", "application/json")
			if stripAndCompress {
				req.Header.Set("Content-Encoding", "zstd")
			}
		}
		var rec *httptest.ResponseRecorder
		if subscription {
			rec = env.serve(t, http.MethodPost, "responses", mutate)
		} else {
			req := httptest.NewRequest(http.MethodPost, path, nil)
			mutate(req)
			rec = httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
		}
		if rec.Code != http.StatusOK || streaming && !strings.Contains(rec.Body.String(), "response.completed") {
			t.Fatalf("status=%d response=%s", rec.Code, rec.Body.String())
		}
		got := <-received
		if got.contentLength != int64(len(got.body)) {
			t.Fatal("修改正文后 Content-Length 不匹配")
		}
		decoded := got.body
		if stripAndCompress {
			if got.encoding != "zstd" || bytes.Equal(wire, got.body) {
				t.Fatal("转换后仍复用了旧压缩帧，或丢失压缩头")
			}
			var err error
			decoded, err = decodeZstdRequestBody(got.body, defaultEncodedRequestBodyMaxBytes)
			if err != nil {
				t.Fatal(err)
			}
		}
		assertHTTPContextBody(t, decoded, stripAndCompress, transforms)
		if attempt > 0 && !bytes.Equal(previous, decoded) {
			t.Fatal("重复请求的兼容结果不一致")
		}
		previous = decoded
	}
}

func assertHTTPContextBody(t *testing.T, body []byte, strip, transforms bool) {
	t.Helper()
	items := gjson.GetBytes(body, "input").Array()
	if len(items) != 6 || items[0].Get("type").Str != "message" || items[1].Get("role").Str != "user" ||
		items[0].Get("content.0.text").Str != `<external_codex_apps_open_page>{"page_id":null}</external_codex_apps_open_page>` ||
		items[2].Get("call_id").Str != "call-fixture" || items[3].Get("call_id").Str != "call-fixture" || items[3].Get("type").Str != "function_call_output" ||
		items[4].Get("encrypted_content").Str != "reasoning-ciphertext" || items[5].Get("encrypted_content").Str != "compaction-ciphertext" ||
		gjson.GetBytes(body, "number").Raw != "9007199254740993" {
		t.Fatalf("上下文未转换，或工具历史/密文被破坏：%s", body)
	}
	if items[0].Get(codexInternalMetadataKey).Exists() == strip {
		t.Fatal("标准 Responses 兼容开关未按原逻辑生效")
	}
	if transforms && (gjson.GetBytes(body, "model").Str != "gpt-5.5" || !strings.Contains(items[1].Get("content.0.text").Str, "redacted-context") || strings.Contains(items[1].Get("content.0.text").Str, "Companion Page snapshot")) {
		t.Fatal("转换后的上下文未经过隐私脱敏或模型改写")
	}
}

func TestAccountPipeline_CodexHTTPContextFailover(t *testing.T) {
	var hits atomic.Int32
	received := make(chan []byte, 2)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
		}
		received <- body
		w.Header().Set("Content-Type", "application/json")
		if hits.Add(1) == 1 {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = io.WriteString(w, `{"error":{"message":"fixture auth failure"}}`)
			return
		}
		_, _ = io.WriteString(w, `{"id":"fixture","status":"completed","output":[]}`)
	}))
	defer upstream.Close()
	svc := newRealAccountPipelineTestService(t, []*store.UpstreamAccountRecord{
		{ProviderType: "api_key", AccountName: "first", CredentialRaw: "fixture-a", BaseURL: upstream.URL, Enabled: true, GroupKey: "primary", Priority: 10, StripCodexInternalMetadata: true},
		{ProviderType: "api_key", AccountName: "second", CredentialRaw: "fixture-b", BaseURL: upstream.URL, Enabled: true, GroupKey: "primary", Priority: 20},
	})
	handler := newAccountPipelineTestHandler(t, upstream.URL, svc)
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(codexClientContextFixture))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || hits.Load() != 2 {
		t.Fatalf("status=%d hits=%d response=%s", rec.Code, hits.Load(), rec.Body.String())
	}
	assertHTTPContextBody(t, <-received, true, false)
	assertHTTPContextBody(t, <-received, false, false)
}

func TestAccountPipeline_CodexHTTPContextInvalidDoesNotPenalizeAccount(t *testing.T) {
	var hits atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1) }))
	defer upstream.Close()
	svc := &mockAccountPoolService{accounts: []*store.UpstreamAccountRecord{
		{ID: 1, ProviderType: "api_key", CredentialRaw: "fixture", BaseURL: upstream.URL, Enabled: true},
	}}
	handler := newAccountPipelineTestHandler(t, upstream.URL, svc)
	body := strings.Replace(codexClientContextFixture, `"namespace":"agent"`, `"namespace":"agent","namespace":"agent"`, 1)
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), requestBodyCodeCodexContextInvalid) {
		t.Fatalf("status=%d response=%s", rec.Code, rec.Body.String())
	}
	if hits.Load() != 0 || len(svc.softFailureCalls) != 0 || len(svc.transientCalls) != 0 || len(svc.authFailCalls) != 0 ||
		len(svc.completeCalls) != 1 || svc.completeCalls[0].outcome != "invalid_request" {
		t.Fatal("本地无效请求不应请求上游、换号或惩罚账号")
	}
}

func TestAccountPipeline_CodexHTTPContextExactScope(t *testing.T) {
	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/v1/responses"}, {http.MethodPut, "/v1/responses/compact"},
		{http.MethodPost, "/v1/responses/other"}, {http.MethodPost, "/v1/messages"},
	} {
		t.Run(tc.method+tc.path, func(t *testing.T) {
			received := make(chan []byte, 1)
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, err := io.ReadAll(r.Body)
				if err != nil {
					t.Error(err)
				}
				received <- body
				w.WriteHeader(http.StatusOK)
			}))
			defer upstream.Close()
			handler := newAccountPipelineTestHandler(t, upstream.URL, nil)
			account := &store.UpstreamAccountRecord{ProviderType: "api_key", CredentialRaw: "fixture", BaseURL: upstream.URL}
			req := httptest.NewRequest(tc.method, tc.path, strings.NewReader(codexClientContextFixture))
			resp, release, _, err := handler.forwardRequestToAccount(req.Context(), req, []byte(codexClientContextFixture), account, false, nil)
			if err != nil {
				t.Fatal(err)
			}
			_ = resp.Body.Close()
			if release != nil {
				release()
			}
			if string(<-received) != codexClientContextFixture {
				t.Fatal("非 Responses POST 请求不应执行上下文兼容")
			}
		})
	}
}
