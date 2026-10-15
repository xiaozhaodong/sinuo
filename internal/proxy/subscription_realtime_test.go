package proxy

import (
	"io"
	"net/http"
	"strings"
	"testing"

	svc "sinuo/internal/service"
)

// 语音建连在两种模式下都直通，协议版本头不能被白名单丢弃或改写。
func TestSubscriptionHandlerForwardsRealtimeAlphaHeader(t *testing.T) {
	for _, mode := range []string{svc.CodexSubscriptionProxyModeSubscription, svc.CodexSubscriptionProxyModePool} {
		t.Run(mode, func(t *testing.T) {
			for _, tc := range []struct {
				name  string
				alpha string
			}{
				{name: "v2", alpha: "quicksilver=v2"},
				{name: "v1", alpha: "quicksilver=v1"},
				{name: "missing"},
			} {
				t.Run(tc.name, func(t *testing.T) {
					env := newSubscriptionTestEnv(t, func(w http.ResponseWriter, r *http.Request) {
						if r.Header.Get("OpenAI-Alpha") != tc.alpha {
							http.Error(w, "invalid_quicksilver_alpha_header", http.StatusBadRequest)
							return
						}
						w.WriteHeader(http.StatusCreated)
					}, mode)

					const sdp = "v=0\r\n"
					resp := env.request(http.MethodPost, "realtime/calls", func(r *http.Request) {
						r.Header.Set("Authorization", "Bearer client-token")
						r.Header.Set("Content-Type", "application/sdp")
						if tc.alpha != "" {
							r.Header.Set("OpenAI-Alpha", tc.alpha)
						}
						r.Body = io.NopCloser(strings.NewReader(sdp))
						r.ContentLength = int64(len(sdp))
					})
					defer resp.Body.Close()

					if resp.StatusCode != http.StatusCreated {
						t.Fatalf("语音建连失败: status=%d body=%s", resp.StatusCode, readBody(t, resp))
					}
					requests := env.upstreamRequests()
					if len(requests) != 1 {
						t.Fatalf("上游请求数不符: %d", len(requests))
					}
					if got := requests[0].Header.Get("OpenAI-Alpha"); got != tc.alpha {
						t.Fatalf("语音协议头未原样透传: got=%q want=%q", got, tc.alpha)
					}
					if tc.alpha == "" && requests[0].Header.Values("OpenAI-Alpha") != nil {
						t.Fatal("客户端未发送语音协议头时不应自动补头")
					}
				})
			}
		})
	}
}
