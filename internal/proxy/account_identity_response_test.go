package proxy

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"sinuo/internal/codexidentity"
	"sinuo/internal/store"
)

type firstWriteCallback struct {
	*httptest.ResponseRecorder
	onFirst func()
	called  bool
}

func (w *firstWriteCallback) fire() {
	if !w.called {
		w.called = true
		w.onFirst()
	}
}
func (w *firstWriteCallback) WriteHeader(status int) {
	w.ResponseRecorder.WriteHeader(status)
	w.fire()
}
func (w *firstWriteCallback) Write(b []byte) (int, error) {
	if !w.called {
		w.WriteHeader(200)
	}
	return w.ResponseRecorder.Write(b)
}
func (w *firstWriteCallback) Flush() { w.fire(); w.ResponseRecorder.Flush() }

func TestAccountIdentityRegistersOwnerBeforeFirstWrite(t *testing.T) {
	for _, subscription := range []bool{false, true} {
		for _, kind := range []string{"json", "sse", "error"} {
			t.Run(fmt.Sprintf("%v/%s", subscription, kind), func(t *testing.T) {
				env := newSubscriptionPoolTestEnv(t, []*store.UpstreamAccountRecord{identityAccount(t, 1)}, true)
				p := enabledIdentityPolicy(t)
				env.handler.proxy.SetCodexIdentityProvider(identityProviderFunc(func(context.Context) (codexidentity.Policy, error) { return p, nil }))
				env.transport.responder = func(r *http.Request) (*http.Response, error) {
					status, contentType, body := 200, "application/json", `{"id":"resp_1","status":"completed","output":[]}`
					if kind == "sse" {
						contentType = "text/event-stream"
						body = "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"status\":\"completed\",\"output\":[]}}\n\n"
					}
					if kind == "error" {
						status = 400
						body = `{"error":{"type":"invalid_request_error","message":"fixture"}}`
					}
					h := make(http.Header)
					h.Set("Content-Type", contentType)
					h.Set("x-codex-turn-state", "fixture-state")
					h.Set("Set-Cookie", "private=1")
					h["connection"] = []string{"X-Private", "Keep-Alive"}
					h.Set("X-Private", "secret")
					return &http.Response{StatusCode: status, Header: h, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
				}
				body := `{"model":"gpt-5","input":"hello","stream":` + fmt.Sprint(kind == "sse") + `}`
				w := &firstWriteCallback{ResponseRecorder: httptest.NewRecorder()}
				w.onFirst = func() {
					if w.Header().Get("x-codex-turn-state") != "fixture-state" {
						t.Fatal("状态未传回下游")
					}
					for _, key := range []string{"Set-Cookie", "Connection", "X-Private", "Keep-Alive"} {
						if w.Header().Get(key) != "" {
							t.Fatal("上游 hop/cookie 泄漏", key)
						}
					}
					next := identityRequestFixture(subscription, body)
					next.Header.Set("x-codex-turn-state", "fixture-state")
					rec := httptest.NewRecorder()
					identityServe(env, subscription, rec, next)
					requests, _ := env.transport.captured()
					if len(requests) != 2 || requests[1].Header.Get("x-codex-turn-state") != "fixture-state" {
						t.Fatal("首次下游写入前 owner 未登记")
					}
				}
				identityServe(env, subscription, w, identityRequestFixture(subscription, body))
				if !w.called {
					t.Fatal("未到达下游写入")
				}
			})
		}
	}
}

func TestAccountIdentityResponseConnectionDropsStateBeforeRegistration(t *testing.T) {
	env := newSubscriptionPoolTestEnv(t, []*store.UpstreamAccountRecord{identityAccount(t, 1)}, true)
	h := env.handler.proxy
	p := enabledIdentityPolicy(t)
	h.SetCodexIdentityProvider(identityProviderFunc(func(context.Context) (codexidentity.Policy, error) { return p, nil }))
	r := identityRequestFixture(false, `{}`)
	_, err := h.attachCodexIdentityRequest(r.Context(), r)
	if err != nil {
		t.Fatal(err)
	}
	_, _, prepared, err := h.prepareAccountIdentity(r.Context(), r, []byte(`{}`), identityAccount(t, 1), "https://chatgpt.com/backend-api/codex/responses")
	if err != nil {
		t.Fatal(err)
	}
	resp := &http.Response{Header: make(http.Header)}
	resp.Header.Set("x-codex-turn-state", "must-drop")
	resp.Header.Set("Connection", "X-Codex-Turn-State")
	h.observeAccountIdentityResponse(resp, prepared)
	if resp.Header.Get("x-codex-turn-state") != "" {
		t.Fatal("动态 hop 未删除")
	}
	probe := identityRequestFixture(false, `{}`)
	probe.Header.Set("x-codex-turn-state", "must-drop")
	_, err = h.attachCodexIdentityRequest(probe.Context(), probe)
	if err != nil {
		t.Fatal(err)
	}
	_, _, out, err := h.prepareAccountIdentity(probe.Context(), probe, []byte(`{}`), identityAccount(t, 1), "https://chatgpt.com/backend-api/codex/responses")
	if err != nil || out.Headers.Get("x-codex-turn-state") != "" {
		t.Fatal("动态 hop 状态被登记")
	}
}

func TestAccountIdentityRedirectOriginBoundary(t *testing.T) {
	for _, cross := range []bool{false, true} {
		t.Run(fmt.Sprint(cross), func(t *testing.T) {
			hits := 0
			target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits++; w.WriteHeader(200) }))
			defer target.Close()
			origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/done" {
					hits++
					w.WriteHeader(200)
					return
				}
				location := "/done"
				if cross {
					location = target.URL + "/done"
				}
				http.Redirect(w, r, location, http.StatusTemporaryRedirect)
			}))
			defer origin.Close()
			req, _ := http.NewRequest(http.MethodPost, origin.URL, strings.NewReader(`{"client_metadata":{"session_id":"mapped"}}`))
			resp, err := identityBoundClient(origin.Client()).Do(req)
			if resp != nil {
				_ = resp.Body.Close()
			}
			if cross {
				if err == nil || codexIdentityError(err) == nil || hits != 0 {
					t.Fatalf("跨源请求泄漏: hits=%d err=%v", hits, err)
				}
			} else if err != nil || hits != 1 {
				t.Fatalf("同源跳转被破坏: hits=%d err=%v", hits, err)
			}
		})
	}
}

func TestSubscriptionIdentityPassthroughAndOriginalZstd(t *testing.T) {
	for _, mode := range []string{"subscription", "pool"} {
		t.Run(mode, func(t *testing.T) {
			env := newSubscriptionTestEnv(t, nil, mode)
			env.handler.proxy.SetCodexIdentityProvider(identityProviderFunc(func(context.Context) (codexidentity.Policy, error) {
				t.Error("直通路径不应读取账号池身份策略")
				return enabledIdentityPolicy(t), nil
			}))
			body := []byte(` {"model":"gpt-5","client_metadata":{"session_id":"source","workspace":"/private"},"prompt_cache_key":"source","input":"hello"} `)
			compressed := zstdCompress(t, body)
			operation := "responses"
			if mode == "pool" {
				operation = "responses/compact"
			}
			resp := env.request(http.MethodPost, operation, func(r *http.Request) {
				r.Header = identityRequestFixture(false, string(body)).Header
				r.Header.Set("Content-Encoding", "zstd")
				r.Header.Set("x-codex-turn-state", "client-state")
				r.Body = io.NopCloser(bytes.NewReader(compressed))
				r.ContentLength = int64(len(compressed))
			})
			_ = resp.Body.Close()
			requests := env.upstreamRequests()
			if resp.StatusCode != 200 || len(requests) != 1 {
				t.Fatalf("直通请求失败 %d", resp.StatusCode)
			}
			got := requests[0]
			if !bytes.Equal(got.Body, compressed) || got.Header.Get("session-id") != "source" || got.Header.Get("x-codex-turn-state") != "client-state" || got.Header.Get("Authorization") != "Bearer source-secret" || got.Header.Get("x-oai-attestation") != "source-proof" {
				t.Fatal("订阅直通身份或压缩帧发生变化")
			}
		})
	}
}

func TestAccountIdentityZstdAfterMappingAndNo415Fallback(t *testing.T) {
	for _, subscription := range []bool{false, true} {
		for _, reject := range []bool{false, true} {
			t.Run(fmt.Sprintf("%v/%v", subscription, reject), func(t *testing.T) {
				acc := identityAccount(t, 1)
				acc.EnableRequestCompression = true
				env := newSubscriptionPoolTestEnv(t, []*store.UpstreamAccountRecord{acc}, true)
				policy := enabledIdentityPolicy(t)
				env.handler.proxy.SetCodexIdentityProvider(identityProviderFunc(func(context.Context) (codexidentity.Policy, error) { return policy, nil }))
				if reject {
					env.transport.responder = func(r *http.Request) (*http.Response, error) {
						return &http.Response{StatusCode: 415, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"error":"unsupported encoding"}`)), Request: r}, nil
					}
				}
				body := `{"model":"gpt-5","prompt_cache_key":"source","client_metadata":{"session_id":"source"},"input":"hello"}`
				compressed := zstdCompress(t, []byte(body))
				r := identityRequestFixture(subscription, body)
				r.Header.Set("Content-Encoding", "zstd")
				r.Body = io.NopCloser(bytes.NewReader(compressed))
				r.ContentLength = int64(len(compressed))
				rec := httptest.NewRecorder()
				identityServe(env, subscription, rec, r)
				requests, bodies := env.transport.captured()
				if len(requests) != 1 {
					t.Fatal("415 被静默重试或丢弃", len(requests))
				}
				got := requests[0]
				if got.Header.Get("Content-Encoding") != "zstd" || got.ContentLength != int64(len(bodies[0])) || bytes.Equal(compressed, bodies[0]) {
					t.Fatal("映射后的 wire body/长度错误")
				}
				plain := zstdDecompress(t, bodies[0])
				if bytes.Contains(plain, []byte(`"session_id":"source"`)) || !bytes.Contains(plain, []byte(`"input":"hello"`)) {
					t.Fatal("未按映射后的正文压缩")
				}
				if reject && rec.Code != 415 {
					t.Fatal("未透传415", rec.Code)
				}
			})
		}
	}
}
