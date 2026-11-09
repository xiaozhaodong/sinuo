package handlers

import (
	"context"
	"io"
	"net/http"

	"sinuo/internal/privacy"
)

type privacyAttemptKey struct{}
type privacyAttempt struct {
	sse       bool
	websocket bool
	view      *privacy.RestoreView
}

// WithPrivacyAttempt 将暂存映射绑定到本次出站尝试。
func WithPrivacyAttempt(r *http.Request, sse bool) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), privacyAttemptKey{}, &privacyAttempt{sse: sse}))
}

// WithWebSocketPrivacyAttempt 绑定单条客户端提交，下一条提交可使用不同的字典代际。
func WithWebSocketPrivacyAttempt(r *http.Request) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), privacyAttemptKey{}, &privacyAttempt{websocket: true}))
}

// PrivacyAttemptView 供原生 WebSocket 在发送前取得当前提交的视图。
func PrivacyAttemptView(r *http.Request) *privacy.RestoreView {
	if a := privacyAttemptFrom(r); a != nil {
		return a.view
	}
	return nil
}

func privacyAttemptFrom(r *http.Request) *privacyAttempt {
	if r == nil {
		return nil
	}
	a, _ := r.Context().Value(privacyAttemptKey{}).(*privacyAttempt)
	return a
}

// PublishPrivacyAttempt 在正文、身份和传输均就绪后原子发布。本地拒绝不能触发换号。
func PublishPrivacyAttempt(r *http.Request) error {
	a := privacyAttemptFrom(r)
	if a == nil || a.view == nil {
		return nil
	}
	if err := a.view.Publish(); err != nil {
		if r.URL.Path == "/v1/responses" || r.URL.Path == "/v1/responses/compact" {
			err.StatusCode = http.StatusBadRequest
		}
		state := PrivacyRequestStateFromContext(r.Context())
		if state != nil && state.summary != nil {
			state.summary.rejectPublish(err)
		}
		return err
	}
	return nil
}

// SendPrivacyRequest 是发布边界。请求构造、压缩或准备失败时尚未改变进程字典。
func SendPrivacyRequest(client *http.Client, r, upstream *http.Request) (*http.Response, error) {
	a := privacyAttemptFrom(r)
	if a == nil || a.view == nil {
		return client.Do(upstream)
	}
	if err := r.Context().Err(); err != nil {
		return nil, err
	}
	if err := PublishPrivacyAttempt(r); err != nil {
		return nil, err
	}
	resp, err := client.Do(upstream)
	state := PrivacyRequestStateFromContext(r.Context())
	if err != nil {
		if generatedPrivacyResponse(r.URL.Path) {
			state.RecordRestore(RestoreSummary{Status: "interrupted"})
		}
		if state == nil {
			a.view.Release()
		}
		return resp, err
	}
	BindPrivacyResponse(r, resp)
	if state == nil {
		// 独立使用 forwarder 时由响应所有者释放视图，包含 4xx 和仅计数接口。
		if _, ok := resp.Body.(*privacyResponseBody); !ok {
			a.view.Release()
		}
	}
	return resp, nil
}

func generatedPrivacyResponse(path string) bool {
	return path == "/v1/responses" || path == "/v1/messages"
}

// ReleaseRestore 释放请求持有的每个视图（包括无法复用缓存的尝试）。
func (s *PrivacyRequestState) ReleaseRestore() {
	if s.summary != nil {
		s.summary.finishPending()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for v := range s.views {
		v.Release()
	}
	s.views = nil
}

type privacyResponseBody struct {
	io.ReadCloser
	view  *privacy.RestoreView
	path  string
	state *PrivacyRequestState
}

func (b *privacyResponseBody) Close() error {
	if b.state == nil {
		b.view.Release()
	}
	return b.ReadCloser.Close()
}

func BindPrivacyResponse(r *http.Request, resp *http.Response) {
	a := privacyAttemptFrom(r)
	if a == nil || a.view == nil || resp.Body == nil || r.URL.Path == "/v1/messages/count_tokens" {
		return
	}
	state := PrivacyRequestStateFromContext(r.Context())
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		if generatedPrivacyResponse(r.URL.Path) {
			state.RecordRestore(RestoreSummary{Status: "upstream_error"})
		}
		return
	}
	if generatedPrivacyResponse(r.URL.Path) {
		state.RecordRestore(RestoreSummary{Status: "pending"})
	}
	resp.Body = &privacyResponseBody{resp.Body, a.view, r.URL.Path, state}
}
func ResponseRestore(resp *http.Response) (string, *privacy.RestoreView) {
	if b, ok := resp.Body.(*privacyResponseBody); ok {
		return b.path, b.view
	}
	return "", nil
}
func RecordRestoreDelivery(resp *http.Response, result RestoreSummary) {
	if b, ok := resp.Body.(*privacyResponseBody); ok {
		b.state.RecordRestore(result)
	}
}
