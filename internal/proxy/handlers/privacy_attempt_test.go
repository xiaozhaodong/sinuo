package handlers

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"sinuo/internal/privacy"

	"github.com/tidwall/gjson"
)

type attemptFilter struct {
	*privacy.Snapshot
	calls int
}

func (f *attemptFilter) SnapshotVersion() int64 { return f.Version }
func (f *attemptFilter) Apply(_ context.Context, req privacy.Request, body []byte) (privacy.ApplyResult, error) {
	f.calls++
	return f.Snapshot.Apply(req, body)
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestPublishFailurePolicyRestoresHTTPBodyAndSummary(t *testing.T) {
	if PrivacyFailureReason(&privacy.PolicyError{Code: privacy.CodeInvalidRequest}) != privacy.CodeInvalidRequest {
		t.Fatal("协议格式错误不能记录成隐私策略拦截")
	}
	for _, mode := range []string{privacy.OnErrorFailOpen, privacy.OnErrorFailClosed} {
		t.Run(mode, func(t *testing.T) {
			settings := privacy.DefaultSettings()
			settings.Mode, settings.ResponseRestoreEnabled, settings.OnError = privacy.ModeRedact, true, mode
			rules, err := privacy.CompileRules([]privacy.Rule{{ID: 1, Name: "fixture", Enabled: true, MatchType: privacy.MatchTypeLiteral, Pattern: "private-A1B2", Placeholder: "[MASK]", Action: privacy.ActionRedact}})
			if err != nil {
				t.Fatal(err)
			}
			filter := &attemptFilter{Snapshot: &privacy.Snapshot{Version: 1, Settings: settings, Rules: rules, Dict: privacy.NewRestoreDict(), RestoreKey: bytes.Repeat([]byte{1}, 32)}}
			state := NewPrivacyRequestState("publish-policy")
			defer state.ReleaseRestore()
			var summary string
			state.SetSummaryRecorder(func(payload string) { summary = payload })
			raw := []byte(`{ "model":"prepared-model","input":"private-A1B2","stream":true }`)
			req := WithPrivacyAttempt(httptest.NewRequest("POST", "/v1/responses", nil).WithContext(WithPrivacyRequestState(context.Background(), state)), true)
			metadata := privacy.Request{Path: req.URL.Path, UpstreamType: privacy.UpstreamTypeAccount}
			filtered, err := ApplyPrivacyFilter(filter, req, metadata, raw)
			if err != nil || bytes.Equal(raw, filtered) {
				t.Fatal("未准备脱敏正文", err)
			}
			// 同 scope 缓存命中必须仍保留原文和扫描时的策略。
			req = WithPrivacyAttempt(req, true)
			if _, err := ApplyPrivacyFilter(filter, req, metadata, raw); err != nil || filter.calls != 1 {
				t.Fatal("缓存没有复用", err)
			}
			filter.Dict.Retire()
			if mode == privacy.OnErrorFailOpen {
				filter.Settings.OnError = privacy.OnErrorFailClosed
			} else {
				filter.Settings.OnError = privacy.OnErrorFailOpen
			}
			upstream := httptest.NewRequest("POST", "http://upstream/v1/responses", strings.NewReader("already-compressed-bytes"))
			upstream.Header.Set("Content-Encoding", "zstd")
			upstream.Header.Set("Content-Length", "999")
			upstream.Header.Set("Authorization", "Bearer fixture")
			upstream.RequestURI = ""
			calls := 0
			client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				calls++
				got, _ := io.ReadAll(r.Body)
				if !bytes.Equal(got, raw) || r.Header.Get("Content-Encoding") != "" || r.Header.Get("Content-Length") != "" || r.ContentLength != int64(len(raw)) || r.Header.Get("Authorization") != "Bearer fixture" {
					t.Fatal("放行未恢复完整原文或传输元数据")
				}
				retryBody, _ := r.GetBody()
				defer retryBody.Close()
				retry, _ := io.ReadAll(retryBody)
				if !bytes.Equal(retry, raw) {
					t.Fatal("重发正文与原文不一致")
				}
				return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"output":[]}`))}, nil
			})}
			resp, sendErr := SendPrivacyRequest(client, req, upstream)
			if mode == privacy.OnErrorFailOpen {
				if sendErr != nil || calls != 1 || PrivacyAttemptView(req) != nil {
					t.Fatal("未遵守扫描时出错放行设置", sendErr)
				}
				defer resp.Body.Close()
				if _, view := ResponseRestore(resp); view != nil {
					t.Fatal("放行响应不应回填")
				}
				if !gjson.Get(summary, "fail_open").Bool() || gjson.Get(summary, "changed").Bool() || gjson.Get(summary, "restore.status").Str != "bypassed" || gjson.Get(summary, "blocked_code").Exists() {
					t.Fatal("放行没有明确摘要", summary)
				}
			} else if sendErr == nil || calls != 0 || gjson.Get(summary, "action").Str != "blocked" {
				t.Fatal("出错拒绝未做到零出站", sendErr)
			}
			if strings.Contains(summary, "private-A1B2") || gjson.Get(summary, "scan_count").Int() != 1 {
				t.Fatal("摘要泄露原文或重复计数", summary)
			}
			// 被撤销的视图不可被下一次尝试复用。
			req = WithPrivacyAttempt(req, true)
			_, _ = ApplyPrivacyFilter(filter, req, metadata, raw)
			if filter.calls != 2 {
				t.Fatal("重试复用了已撤销的回填视图")
			}
		})
	}
}
