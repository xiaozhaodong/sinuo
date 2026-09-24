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

	"cc-forwarder/internal/endpoint"
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
			handler := &claudeCodeEntryHandler{next: probe, port: "18088"}
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

	if err := rt.Apply(ctx, true, addr); err != nil {
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
	if err := rt.Apply(ctx, true, addr); err != nil {
		t.Fatalf("idempotent apply failed: %v", err)
	}

	// 另一个运行态抢同一端口：启动失败并记录原因，不漂移到其他端口
	other := NewClaudeCodeEntryRuntime(probe, nil)
	if err := other.Apply(ctx, true, addr); err == nil {
		t.Fatal("端口被占用时应启动失败")
	}
	if status := other.Status(); status.Running || status.Error == "" {
		t.Fatalf("启动失败应保留错误且不在监听，got %+v", status)
	}

	if err := rt.Apply(ctx, false, addr); err != nil {
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
