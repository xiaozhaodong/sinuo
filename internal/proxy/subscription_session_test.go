package proxy

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"sinuo/internal/accountauth"
	"sinuo/internal/reasoning"
	"sinuo/internal/store"
	"sinuo/internal/tracking"

	"github.com/coder/websocket"
)

func TestSubscriptionHTTPSessionFromClientAssociation(t *testing.T) {
	tracker := newSubscriptionUnitTracker(t)
	env := newSubscriptionTestEnv(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"object":"response","model":"synthetic","status":"completed","service_tier":"default","output":[],"usage":{"input_tokens":3,"output_tokens":2}}`)
	}, "subscription")
	env.handler.proxy.SetUsageTracker(tracker)
	resp := env.request(http.MethodPost, "responses", func(r *http.Request) {
		r.Header.Set("Authorization", "Bearer synthetic-client")
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("session-id", "private-http-session")
		r.Header.Set("thread-id", "child-thread")
		r.Body = io.NopCloser(strings.NewReader(`{"model":"synthetic","input":[],"service_tier":"priority"}`))
	})
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("直通状态=%d", resp.StatusCode)
	}
	rows := waitSessionDetails(t, tracker, "private-http-session", 1)
	if rows[0].RequestReasoning != "speed:priority" || rows[0].ResponseReasoning != "speed:default" {
		t.Fatalf("直通档位未落库: %+v", rows[0])
	}
	if strings.Contains(env.logs.String(), "private-http-session") {
		t.Fatal("直通日志泄漏会话")
	}
}

func TestSubscriptionPoolSessionSurvivesHeaderIsolation(t *testing.T) {
	tracker := newSubscriptionUnitTracker(t)
	env := newSubscriptionPoolTestEnv(t, []*store.UpstreamAccountRecord{{
		ID: 81, AccountName: "synthetic-pool", ProviderType: accountauth.ProviderAPIKey,
		CredentialRaw: "synthetic-key", BaseURL: "http://synthetic.invalid", Enabled: true,
	}}, true)
	env.handler.proxy.SetUsageTracker(tracker)
	rec := env.serve(t, http.MethodPost, "responses", func(r *http.Request) {
		applyClientIdentityHeaders(r)
		r.Header.Set("session-id", "pool-client-original")
		r.Header.Set("thread-id", "child-thread")
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("池模式状态=%d body=%s", rec.Code, rec.Body.String())
	}
	rows := waitSessionDetails(t, tracker, "pool-client-original", 1)
	if rows[0].RequestSource != tracking.RequestSourceCodexSubscriptionProxy || rows[0].UpstreamType != "account" {
		t.Fatal("会话记录未走账号池")
	}
	requests, _ := env.transport.captured()
	if len(requests) != 1 || requests[0].Header.Get("session-id") != "" || requests[0].Header.Get("thread-id") != "" {
		t.Fatal("会话采集改变了池模式头隔离")
	}
}

func TestSubscriptionWSSessionInheritedBySuccessor(t *testing.T) {
	tracker := newSubscriptionUnitTracker(t)
	env := newSubscriptionTestEnv(t, serveSessionWSSuccessor, "subscription")
	env.handler.proxy.SetUsageTracker(tracker)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	url := "ws" + strings.TrimPrefix(env.server.URL, "http") + "/subscription/" + subscriptionTestSecret + "/backend-api/codex/responses"
	conn, _, err := websocket.Dial(ctx, url, &websocket.DialOptions{HTTPHeader: http.Header{
		"Authorization": {"Bearer synthetic-client"}, "Session-Id": {"private-ws-session"}, "Thread-Id": {"child-thread"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
	if err := conn.Write(ctx, websocket.MessageText, []byte(`{"type":"response.create","model":"synthetic","reasoning":{"effort":"high"},"service_tier":"priority","input":[]}`)); err != nil {
		t.Fatal(err)
	}
	for range 4 {
		if _, _, err := conn.Read(ctx); err != nil {
			t.Fatal(err)
		}
	}
	_ = conn.Close(websocket.StatusNormalClosure, "done")
	rows := waitSessionDetails(t, tracker, "private-ws-session", 2)
	for _, row := range rows {
		if row.RequestModel == "" {
			if row.RequestReasoning != "" || reasoning.Parse(row.ResponseReasoning)["speed"] != "ultrafast" {
				t.Fatalf("successor 应保留未知请求档位与自身终态: %+v", row)
			}
		} else if row.RequestReasoning != "effort:high speed:priority" || reasoning.Parse(row.ResponseReasoning)["speed"] != "default" {
			t.Fatalf("WS create 档位未分别落库: %+v", row)
		}
	}
	if strings.Contains(env.logs.String(), "private-ws-session") {
		t.Fatal("WS 日志泄漏会话")
	}
}

func serveSessionWSSuccessor(w http.ResponseWriter, r *http.Request) {
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
	if err != nil {
		return
	}
	defer conn.CloseNow()
	if _, _, err := conn.Read(r.Context()); err != nil {
		return
	}
	for _, payload := range []string{
		`{"type":"response.created","response":{"id":"generated","model":"synthetic","service_tier":"priority"}}`,
		`{"type":"response.completed","response":{"id":"generated","service_tier":"default","usage":{"input_tokens":3,"output_tokens":2}}}`,
		`{"type":"response.created","response":{"id":"successor","model":"synthetic","service_tier":"priority"}}`,
		`{"type":"response.completed","response":{"id":"successor","service_tier":"ultrafast","usage":{"input_tokens":1,"output_tokens":1}}}`,
	} {
		if conn.Write(r.Context(), websocket.MessageText, []byte(payload)) != nil {
			return
		}
	}
	_, _, _ = conn.Read(r.Context())
}

func waitSessionDetails(t *testing.T, tracker *tracking.UsageTracker, sessionID string, count int) []tracking.RequestDetail {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		rows, _, err := tracker.QueryRequestDetailsWithHotPool(context.Background(), &tracking.QueryOptions{SessionID: sessionID, Limit: 10})
		if err != nil {
			t.Fatal(err)
		}
		ready := len(rows) == count
		for _, row := range rows {
			ready = ready && row.SessionID == sessionID && row.Status == "completed"
		}
		if ready {
			return rows
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("会话记录未完整落库")
	return nil
}

func TestSubscriptionCreateSpeedBothShapes(t *testing.T) {
	for _, tc := range []struct{ body, want string }{
		{`{"type":"response.create","service_tier":"priority"}`, "speed:priority"},
		{`{"type":"response.create","response":{"service_tier":"ultrafast"}}`, "speed:ultrafast"},
		{`{"type":"response.create","reasoning":{"effort":"high"},"response":{"service_tier":"flex"}}`, "effort:high speed:flex"},
		{`{"type":"response.create","service_tier":"priority","response":{"reasoning":{"effort":"low"}}}`, "effort:low speed:priority"},
		{`{"type":"response.create","service_tier":"priority","response":{"service_tier":"default"}}`, "speed:default"},
		{`{"type":"response.create"}`, ""},
	} {
		create, ok := parseSubscriptionCreate([]byte(tc.body))
		if !ok || create.Reasoning != tc.want {
			t.Errorf("WS 请求思考串=%q, want %q (ok=%v)", create.Reasoning, tc.want, ok)
		}
	}
}
