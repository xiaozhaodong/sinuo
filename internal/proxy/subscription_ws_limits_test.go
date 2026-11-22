package proxy

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"sinuo/config"
	"sinuo/internal/proxy/subscriptionws"
)

func TestCopySubscriptionWebSocketHandshakeHeadersLimitFallback(t *testing.T) {
	for _, tc := range []struct {
		name       string
		localLimit int64
		upstream   string
		want       string
	}{
		{"零本地上限回退默认值", 0, "", strconv.FormatInt(subscriptionws.DefaultReadLimit, 10)},
		{"负本地上限回退默认值", -1, "", strconv.FormatInt(subscriptionws.DefaultReadLimit, 10)},
		{"负本地上限仍取更小上游值", -1, "16777216", "16777216"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dst, src := http.Header{}, http.Header{}
			if tc.upstream != "" {
				src.Set(subscriptionWSMaxMessageHeader, tc.upstream)
			}
			copySubscriptionWebSocketHandshakeHeaders(dst, src, tc.localLimit)
			if got := dst.Get(subscriptionWSMaxMessageHeader); got != tc.want {
				t.Fatalf("握手消息上限 = %q，期望 %q", got, tc.want)
			}
		})
	}
}

func TestSubscriptionWSAdvertisedMessageLimit(t *testing.T) {
	const header = "x-codex-websocket-max-message-bytes"
	for _, tc := range []struct {
		name     string
		local    int64
		upstream []string
		want     int64
	}{
		{"默认上限", 0, nil, 128 << 20},
		{"上游更小", 0, []string{"16777216"}, 16 << 20},
		{"本地配置更小", 32 << 20, []string{"134217728"}, 32 << 20},
		{"显式扩大本地上限", 256 << 20, nil, 256 << 20},
		{"上游零字节", 0, []string{"0"}, 0},
		{"无效上游值", 0, []string{"-1", "invalid"}, 128 << 20},
		{"重复上限取较小值", 0, []string{"67108864", "16777216"}, 16 << 20},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := newSubscriptionTestEnv(t, func(w http.ResponseWriter, r *http.Request) {
				for _, value := range tc.upstream {
					w.Header().Add(header, value)
				}
				up, err := websocket.Accept(w, r, nil)
				if err != nil {
					t.Errorf("上游握手失败: %v", err)
					return
				}
				defer up.CloseNow()
				_, _, _ = up.Read(r.Context())
			}, "subscription")
			env.handler.proxy.config.RequestBodyMaxBytes = tc.local

			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			url := "ws" + strings.TrimPrefix(env.server.URL, "http") + "/subscription/" + subscriptionTestSecret + "/backend-api/codex/responses"
			conn, resp, err := websocket.Dial(ctx, url, &websocket.DialOptions{HTTPHeader: http.Header{"Authorization": {"Bearer synthetic"}}})
			if err != nil {
				t.Fatal(err)
			}
			defer conn.CloseNow()
			if got := resp.Header.Values(header); len(got) != 1 || got[0] != strconv.FormatInt(tc.want, 10) {
				t.Fatalf("握手消息上限 = %v，期望单值 %d", got, tc.want)
			}
		})
	}
}

func TestRequestBodyLimitAlignedWithWebSocketDefault(t *testing.T) {
	if defaultEncodedRequestBodyMaxBytes != subscriptionws.DefaultReadLimit {
		t.Fatalf("zstd HTTP 默认上限 = %d，与 WS 默认上限 = %d 不一致", defaultEncodedRequestBodyMaxBytes, subscriptionws.DefaultReadLimit)
	}

	defaultCfg := &config.Config{}
	if got := effectiveRequestBodyMaxBytes("", defaultCfg); got != 0 {
		t.Fatalf("默认配置明文 HTTP 上限 = %d，期望 0（不限制）", got)
	}
	if got := effectiveRequestBodyMaxBytes(requestContentEncodingIdentity, defaultCfg); got != 0 {
		t.Fatalf("默认配置 identity HTTP 上限 = %d，期望 0（不限制）", got)
	}
	if got := effectiveRequestBodyMaxBytes(requestContentEncodingZstd, defaultCfg); got != subscriptionws.DefaultReadLimit {
		t.Fatalf("默认配置 zstd HTTP 上限 = %d，期望 %d", got, subscriptionws.DefaultReadLimit)
	}
	if got := subscriptionWSBridgeOptions(nil, defaultCfg).ReadLimit; got != subscriptionws.DefaultReadLimit {
		t.Fatalf("默认配置 WS 上限 = %d，期望 %d", got, subscriptionws.DefaultReadLimit)
	}

	const customLimit int64 = 256 << 20
	customCfg := &config.Config{RequestBodyMaxBytes: customLimit}
	for _, encoding := range []string{"", requestContentEncodingIdentity, requestContentEncodingZstd} {
		if got := effectiveRequestBodyMaxBytes(encoding, customCfg); got != customLimit {
			t.Fatalf("显式配置下 HTTP(%q) 上限 = %d，期望 %d", encoding, got, customLimit)
		}
	}
	if got := subscriptionWSBridgeOptions(nil, customCfg).ReadLimit; got != customLimit {
		t.Fatalf("显式配置下 WS 上限 = %d，期望 %d", got, customLimit)
	}
}

func TestReadAndNormalizeRequestBody_DefaultZstdAllowsBetween64And128MiB(t *testing.T) {
	const overOldLimit = (64 << 20) + 1024
	plain := bytes.Repeat([]byte{'a'}, overOldLimit)
	compressed := mustEncodeZstd(t, plain)
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(compressed))
	req.Header.Set("Content-Encoding", "zstd")

	got, bodyErr := readAndNormalizeRequestBody(httptest.NewRecorder(), req, &config.Config{})
	if bodyErr != nil {
		t.Fatalf("超过旧 64 MiB 但未超过 128 MiB 的 zstd 请求不应被拒: %+v", bodyErr)
	}
	if len(got) != overOldLimit {
		t.Fatalf("解压长度 = %d，期望 %d", len(got), overOldLimit)
	}
}
