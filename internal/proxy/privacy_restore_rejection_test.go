package proxy

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tidwall/gjson"

	"sinuo/internal/privacy"
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
			env.handler.proxy.SetPrivacyFilter(newRestoreTestFilter(t))
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
	e := &privacy.PolicyError{Code: "privacy_restore_rejected", StatusCode: 422, Message: "private parser detail",
		Result: privacy.ApplyResult{Body: []byte("private body"), Restore: &privacy.RestoreView{}, RuleHits: []privacy.RuleHit{{Matches: []string{"private match"}}}}}
	clean := sanitizeSubscriptionPolicyError(e)
	if clean.Message != "privacy policy rejected the request" || clean.Result.Body != nil || clean.Result.Restore != nil || clean.Result.RuleHits[0].Matches != nil {
		t.Fatal("自定义过滤器的错误细节或请求映射进入摘要")
	}
}
