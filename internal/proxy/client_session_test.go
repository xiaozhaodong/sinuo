package proxy

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestClientSessionIDHeaders(t *testing.T) {
	cases := []struct {
		name   string
		header http.Header
		want   string
	}{
		{"Claude", http.Header{"X-Claude-Code-Session-Id": {"claude-session"}}, "claude-session"},
		{"Codex", http.Header{"Session-Id": {"codex-session"}}, "codex-session"},
		{"旧版 Codex", http.Header{"Session_id": {"old-session"}}, "old-session"},
		{"thread 兜底", http.Header{"Thread-Id": {"thread-session"}}, "thread-session"},
		{"优先级", http.Header{"X-Claude-Code-Session-Id": {"claude"}, "Session-Id": {"codex"}}, "claude"},
		{"去首尾空白", http.Header{"Session-Id": {"  trimmed\t"}}, "trimmed"},
		{"128 字节", http.Header{"Session-Id": {strings.Repeat("a", 128)}}, strings.Repeat("a", 128)},
		{"超长", http.Header{"Session-Id": {strings.Repeat("a", 129)}}, ""},
		{"空格", http.Header{"Session-Id": {"a b"}}, ""},
		{"控制字符", http.Header{"Session-Id": {"a\nb"}}, ""},
		{"非 ASCII", http.Header{"Session-Id": {"会话"}}, ""},
		{"DEL", http.Header{"Session-Id": {"a\x7f"}}, ""},
		{"多值", http.Header{"Session-Id": {"a", "b"}}, ""},
		{"重复同值", http.Header{"Session-Id": {"a", "a"}}, ""},
		{"非法值后兜底", http.Header{"Session-Id": {"a b"}, "Thread-Id": {"fallback"}}, "fallback"},
		{"多值后兜底", http.Header{"Session-Id": {"a", "b"}, "Thread-Id": {"fallback"}}, "fallback"},
		{"不按父线程归并", http.Header{"X-Codex-Parent-Thread-Id": {"parent"}}, ""},
		{"无会话", nil, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
			r.Header = tc.header
			if got := clientSessionID(r); got != tc.want {
				t.Fatalf("会话提取 = %q, want %q", got, tc.want)
			}
		})
	}
	if clientSessionID(nil) != "" {
		t.Fatal("nil 请求必须返回空会话")
	}
}

func TestClientSessionIDSubscriptionAssociation(t *testing.T) {
	for _, tc := range []struct{ session, thread, want string }{
		{"client-original", "child-thread", "client-original"},
		{"", "root-thread", "root-thread"},
		{"bad value", "root-thread", "root-thread"},
		{"", "", "header-session"},
	} {
		r := httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
		r.Header.Set("session-id", "header-session")
		r = r.WithContext(withSubscriptionRequestInfo(r.Context(), &subscriptionRequestInfo{
			Association: subscriptionClientAssociation{SessionID: tc.session, ThreadID: tc.thread},
		}))
		if got := clientSessionID(r); got != tc.want {
			t.Fatalf("association 优先级错误: got %q, want %q", got, tc.want)
		}
	}
}
