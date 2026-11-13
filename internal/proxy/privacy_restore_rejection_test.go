package proxy

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tidwall/gjson"

	"sinuo/internal/privacy"
	"sinuo/internal/proxy/handlers"
	"sinuo/internal/store"
)

func TestPoolRestoreRejectionReportsSafeReason(t *testing.T) {
	const marker = "fixture-private-value"
	for _, tt := range []struct{ fields, reason string }{
		{`"input":[{"type":"message","encrypted_content":"fixture-private-value"}]`, "无法识别加密历史项"},
		{`"input":[{"type":"function_call_output","encrypted_output":"fixture-private-value"}]`, "无歧义"},
		{`"input":[{"type":"function_call","encrypted_function_args":"fixture-private-value"}]`, "无歧义"},
	} {
		t.Run(tt.reason, func(t *testing.T) {
			logs := &syncBuffer{}
			prev := slog.Default()
			slog.SetDefault(slog.New(slog.NewTextHandler(logs, nil)))
			t.Cleanup(func() { slog.SetDefault(prev) })
			env := newSubscriptionPoolTestEnv(t, []*store.UpstreamAccountRecord{identityAPIKeyAccount(1)}, true)
			filter := newRestoreTestFilter(t)
			filter.Settings.OnError = privacy.OnErrorFailClosed
			env.handler.proxy.SetPrivacyFilter(filter)
			request := identityRequestFixture(true, `{"model":"gpt-5","stream":true,`+tt.fields+`}`)
			request.Header.Set("Accept", "text/event-stream")
			w := httptest.NewRecorder()
			env.handler.ServeHTTP(w, request)
			sent, _ := env.transport.captured()
			body := w.Body.String()
			if w.Code != http.StatusBadRequest || len(sent) != 0 || !strings.Contains(body, "privacy_restore_rejected") {
				t.Fatalf("预期本地 400，得到 %d，上游次数 %d，响应 %s", w.Code, len(sent), body)
			}
			msg := gjson.Get(body, "error.message").String()
			if !strings.Contains(msg, tt.reason) || !strings.Contains(logs.String(), msg) ||
				strings.Contains(body, marker) || strings.Contains(logs.String(), marker) {
				t.Fatal("响应和日志应使用相同的固定原因，且不含输入值", msg)
			}
		})
	}
}

func TestSubscriptionRestoreRejectionSanitizesUntrustedMessage(t *testing.T) {
	for _, tc := range []struct{ code, message string }{
		{"privacy_restore_rejected", "privacy policy rejected the request"},
		{privacy.CodeScanFailed, "privacy scan failed and the request was rejected"},
	} {
		t.Run(tc.code, func(t *testing.T) {
			e := &privacy.PolicyError{Code: tc.code, StatusCode: 422, Message: "private parser detail",
				Result: privacy.ApplyResult{Body: []byte("private body"), Restore: &privacy.RestoreView{}, RuleHits: []privacy.RuleHit{{Matches: []string{"private match"}}}}}
			clean := sanitizeSubscriptionPolicyError(e)
			if clean.Message != tc.message || clean.Result.Body != nil || clean.Result.Restore != nil || clean.Result.RuleHits[0].Matches != nil {
				t.Fatal("自定义过滤器的错误细节或请求映射进入摘要")
			}
		})
	}
}

func TestSubscriptionCompileBudgetFailureReportsSafeReason(t *testing.T) {
	for _, onError := range []string{privacy.OnErrorFailOpen, privacy.OnErrorFailClosed} {
		t.Run(onError, func(t *testing.T) {
			filter := newRestoreTestFilter(t)
			filter.Settings.OnError = onError
			filter.CompileBlocked, filter.CompileError = true, "private compile detail"
			state := handlers.NewPrivacyRequestState("budget-reason")
			var summary string
			state.SetSummaryRecorder(func(value string) { summary = value })
			request := withSubscriptionPrivacyState(httptest.NewRequest(http.MethodPost, "/v1/responses", nil), state)
			raw := []byte(`{"input":"synthetic"}`)
			got, err := applySubscriptionPrivacy(filter, request, "/v1/responses", raw)
			reason := privacy.ErrLiteralBudget.Error()
			if gjson.Get(summary, "failure_reason").Str != reason || strings.Contains(summary, filter.CompileError) {
				t.Fatal("请求详情未保留固定容量原因，或泄漏编译细节", summary)
			}
			if onError == privacy.OnErrorFailOpen {
				if err != nil || !bytes.Equal(got, raw) || !gjson.Get(summary, "fail_open").Bool() {
					t.Fatal("容量提示改变了原文放行策略", err)
				}
				return
			}
			policy := handlers.AsPrivacyPolicyError(err)
			if policy == nil || policy.StatusCode != http.StatusServiceUnavailable || policy.Code != privacy.CodeScanFailed || policy.Message != reason || got != nil {
				t.Fatal("订阅拒绝丢失容量提示或改变了错误契约", err)
			}
			response := httptest.NewRecorder()
			handlers.WritePrivacyPolicyErrorResponse(response, policy)
			if response.Code != http.StatusServiceUnavailable || gjson.Get(response.Body.String(), "error.message").Str != reason {
				t.Fatal("客户端响应缺少词典容量原因和删减提示", response.Body.String())
			}
		})
	}
}
