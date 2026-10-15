package proxy

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"sinuo/internal/endpoint"
)

// entryProbe 记录请求是否被转交，以及转交时是否带入口标记
type entryProbe struct {
	called bool
	marked bool
}

func (p *entryProbe) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	p.called = true
	p.marked = endpoint.IsClaudeCodeEntry(r.Context())
	w.WriteHeader(http.StatusNoContent)
}

func TestClaudeCodeEntryHandler_Boundaries(t *testing.T) {
	cases := []struct {
		name       string
		method     string
		path       string
		host       string
		origin     string
		wantStatus int
		wantPass   bool
	}{
		{"messages 放行", http.MethodPost, "/v1/messages?beta=true", "127.0.0.1:18088", "", http.StatusNoContent, true},
		{"count_tokens 放行", http.MethodPost, "/v1/messages/count_tokens", "127.0.0.1:18088", "", http.StatusNoContent, true},
		{"localhost 放行", http.MethodPost, "/v1/messages", "LOCALHOST:18088", "", http.StatusNoContent, true},
		{"端口不符拒绝", http.MethodPost, "/v1/messages", "127.0.0.1:9999", "", http.StatusForbidden, false},
		{"外部域名拒绝（DNS rebinding）", http.MethodPost, "/v1/messages", "evil.example.com:18088", "", http.StatusForbidden, false},
		{"带 Origin 拒绝", http.MethodPost, "/v1/messages", "127.0.0.1:18088", "https://evil.example.com", http.StatusForbidden, false},
		{"预热探测 404", http.MethodHead, "/api/hello", "127.0.0.1:18088", "", http.StatusNotFound, false},
		{"其他路径 404", http.MethodPost, "/v1/responses", "127.0.0.1:18088", "", http.StatusNotFound, false},
		{"非 POST 405", http.MethodGet, "/v1/messages", "127.0.0.1:18088", "", http.StatusMethodNotAllowed, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			probe := &entryProbe{}
			handler := &claudeCodeEntryHandler{next: probe, port: "18088", policy: func() ClaudeCodeEntryPolicy { return ClaudeCodeEntryPolicy{} }}
			req := httptest.NewRequest(tc.method, "http://"+tc.host+tc.path, strings.NewReader("{}"))
			req.Host = tc.host
			if tc.origin != "" {
				req.Header.Set("Origin", tc.origin)
			}
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			if rec.Code != tc.wantStatus || probe.called != tc.wantPass {
				t.Fatalf("status=%d called=%v, want status=%d called=%v", rec.Code, probe.called, tc.wantStatus, tc.wantPass)
			}
			if tc.wantPass && !probe.marked {
				t.Fatal("转交的请求必须带 Claude Code 入口标记")
			}
			if !tc.wantPass && !strings.Contains(rec.Body.String(), `"type":"error"`) {
				t.Fatalf("拒绝时应返回 Anthropic 错误格式，got %s", rec.Body.String())
			}
		})
	}
}

func TestClaudeCodeEntryRuntime_ApplyStopAndPortConflict(t *testing.T) {
	probe := &entryProbe{}
	rt := NewClaudeCodeEntryRuntime(probe, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// 先占一个端口，拿到确定可用的地址后释放，再交给运行态绑定
	holder, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve port failed: %v", err)
	}
	addr := holder.Addr().String()
	_ = holder.Close()

	if err := rt.Apply(ctx, true, addr, ClaudeCodeEntryPolicy{}); err != nil {
		t.Fatalf("apply enable failed: %v", err)
	}
	if status := rt.Status(); !status.Running || status.ListenAddr != addr {
		t.Fatalf("启用后应在监听 %s，got %+v", addr, status)
	}
	resp, err := http.Post("http://"+addr+"/v1/messages", "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatalf("request entry failed: %v", err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent || !probe.marked {
		t.Fatalf("真实监听下请求应被转交并带标记: status=%d marked=%v", resp.StatusCode, probe.marked)
	}

	// 同地址重复启用是幂等的
	if err := rt.Apply(ctx, true, addr, ClaudeCodeEntryPolicy{}); err != nil {
		t.Fatalf("idempotent apply failed: %v", err)
	}

	// 另一个运行态抢同一端口：启动失败并记录原因，不漂移到其他端口
	other := NewClaudeCodeEntryRuntime(probe, nil)
	if err := other.Apply(ctx, true, addr, ClaudeCodeEntryPolicy{}); err == nil {
		t.Fatal("端口被占用时应启动失败")
	}
	if status := other.Status(); status.Running || status.Error == "" {
		t.Fatalf("启动失败应保留错误且不在监听，got %+v", status)
	}

	if err := rt.Apply(ctx, false, addr, ClaudeCodeEntryPolicy{}); err != nil {
		t.Fatalf("apply disable failed: %v", err)
	}
	if status := rt.Status(); status.Running {
		t.Fatalf("停用后不应在监听，got %+v", status)
	}
	if conn, err := net.DialTimeout("tcp", addr, 200*time.Millisecond); err == nil {
		_ = conn.Close()
		t.Fatal("停用后端口应已释放")
	}
}

// TestClaudeCodeEntry_EndToEndModes
// 端到端：经真实入口监听进来的请求，订阅直通模式直连官方并透传客户端 token；
// 切到端点模式后同样的请求走第三方端点；主端口（直接进代理处理器）始终走第三方端点，且客户端 token 不外泄。
func TestClaudeCodeEntry_EndToEndModes(t *testing.T) {
	type hit struct{ authorization, apiKey string }
	subscriptionHits := make(chan hit, 4)
	vendorHits := make(chan hit, 4)
	upstream := func(hits chan hit) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			hits <- hit{authorization: r.Header.Get("Authorization"), apiKey: r.Header.Get("X-Api-Key")}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"msg_1","type":"message","role":"assistant","model":"claude-test","content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":1,"output_tokens":1}}`))
		}))
	}
	subscription := upstream(subscriptionHits)
	defer subscription.Close()
	vendor := upstream(vendorHits)
	defer vendor.Close()

	vendorCfg := endpointPipelineConfig("vendor", vendor.URL, 1)
	vendorCfg.ApiKey = "vendor-key"
	handler, _ := newEndpointPipelineTestHandler(t, vendorCfg)
	handler.claudeSubscriptionBaseURL = subscription.URL

	rt := NewClaudeCodeEntryRuntime(handler, nil)
	holder, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve port failed: %v", err)
	}
	addr := holder.Addr().String()
	_ = holder.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := rt.Apply(ctx, true, addr, ClaudeCodeEntryPolicy{Mode: endpoint.ClaudeCodeEntryModeSubscription}); err != nil {
		t.Fatalf("start entry failed: %v", err)
	}
	defer func() { _ = rt.Stop(ctx) }()

	const body = `{"model":"claude-test","messages":[{"role":"user","content":"hi"}]}`
	send := func(target string) {
		req, _ := http.NewRequest(http.MethodPost, target+"/v1/messages?beta=true", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer sk-ant-oat01-client")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("request %s failed: %v", target, err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("request %s status = %d", target, resp.StatusCode)
		}
	}
	expectHit := func(hits chan hit, want hit, label string) {
		t.Helper()
		select {
		case got := <-hits:
			if got != want {
				t.Fatalf("%s: 上游收到的凭据不符，got %+v want %+v", label, got, want)
			}
		default:
			t.Fatalf("%s: 未命中预期上游", label)
		}
	}
	clientToken := hit{authorization: "Bearer sk-ant-oat01-client"}
	vendorKey := hit{apiKey: "vendor-key"}

	send("http://" + addr)
	expectHit(subscriptionHits, clientToken, "订阅直通模式")

	if err := rt.Apply(ctx, true, addr, ClaudeCodeEntryPolicy{Mode: endpoint.ClaudeCodeEntryModeEndpoint}); err != nil {
		t.Fatalf("switch mode failed: %v", err)
	}
	send("http://" + addr)
	expectHit(vendorHits, vendorKey, "端点模式")

	mainPort := httptest.NewServer(handler)
	defer mainPort.Close()
	send(mainPort.URL)
	expectHit(vendorHits, vendorKey, "主端口")
	if len(subscriptionHits) != 0 {
		t.Fatal("只有订阅直通模式的入口请求可以直连官方")
	}
}

// modeProbe 记录转交请求携带的生效模式
type modeProbe struct{ mode string }

func (p *modeProbe) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	entry, _ := endpoint.ClaudeCodeEntryFromContext(r.Context())
	p.mode = entry.Mode
	w.WriteHeader(http.StatusNoContent)
}

func TestClaudeCodeEntryRuntime_PolicyHotSwapWithoutRestart(t *testing.T) {
	probe := &modeProbe{}
	rt := NewClaudeCodeEntryRuntime(probe, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	holder, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve port failed: %v", err)
	}
	addr := holder.Addr().String()
	_ = holder.Close()
	defer func() { _ = rt.Stop(ctx) }()

	send := func() string {
		resp, err := http.Post("http://"+addr+"/v1/messages", "application/json", strings.NewReader("{}"))
		if err != nil {
			t.Fatalf("request failed: %v", err)
		}
		_ = resp.Body.Close()
		return probe.mode
	}

	if err := rt.Apply(ctx, true, addr, ClaudeCodeEntryPolicy{Mode: "subscription"}); err != nil {
		t.Fatalf("apply failed: %v", err)
	}
	if got := send(); got != endpoint.ClaudeCodeEntryModeSubscription {
		t.Fatalf("请求应带上当前模式，got %q", got)
	}
	server := rt.server
	if err := rt.Apply(ctx, true, addr, ClaudeCodeEntryPolicy{Mode: endpoint.ClaudeCodeEntryModeEndpoint}); err != nil {
		t.Fatalf("hot swap failed: %v", err)
	}
	if rt.server != server {
		t.Fatal("只改模式不应重启 listener")
	}
	if got := send(); got != endpoint.ClaudeCodeEntryModeEndpoint {
		t.Fatalf("切换后下一个请求应使用新模式，got %q", got)
	}
}
