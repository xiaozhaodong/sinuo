package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
	"time"

	"sinuo/internal/privacy"
	"sinuo/internal/proxy/subscriptionws"
)

func TestSubscriptionWSSteerPrivacy(t *testing.T) {
	rules, err := privacy.CompileRules([]privacy.Rule{{ID: 1, Enabled: true, Name: "synthetic", MatchType: privacy.MatchTypeRegex,
		Pattern: "SYNTHETIC-SECRET", Placeholder: "[masked]", Action: privacy.ActionRedact,
		Scope: privacy.Scope{UpstreamTypes: []string{privacy.UpstreamTypeSubscription}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	for _, input := range []string{
		`"SYNTHETIC-SECRET"`,
		`[{"role":"user","content":"SYNTHETIC-SECRET"}]`,
		`[{"role":"user","content":[{"type":"input_text","text":"SYNTHETIC-SECRET"},{"type":"input_image","image_url":"data:image/png;base64,synthetic"},{"type":"input_file","file_id":"file_synthetic"}]}]`,
	} {
		for _, mode := range []string{privacy.ModeDisabled, privacy.ModeDetect, privacy.ModeRedact, "fail_closed"} {
			t.Run(mode+"/"+input[:8], func(t *testing.T) {
				settings := privacy.DefaultSettings()
				settings.Mode = mode
				if mode == "fail_closed" {
					settings.Mode, settings.ScanMaxBytes, settings.OverLimitAction = privacy.ModeRedact, 1, privacy.OverLimitFailClosed
				}
				state, activity := activitySubmissionState()
				defer state.close("done")
				var logs bytes.Buffer
				interceptor := &subscriptionWSInterceptor{
					filter: subscriptionSnapshotFilter{snapshot: &privacy.Snapshot{Version: 1, Settings: settings, Rules: rules}},
					logger: slog.New(slog.NewJSONHandler(&logs, nil)), submissions: state,
				}
				body := []byte(`{"type":"response.steer","previous_response_id":"resp_synthetic","input":` + input + `}`)
				result, err := interceptor.Intercept(context.Background(), subscriptionws.DirectionDownstreamToUpstream, body)
				if err != nil {
					t.Fatal(err)
				}
				if mode == "fail_closed" {
					if result.Forward != nil || !bytes.Contains(result.Reply, []byte(privacy.CodeScanBodyTooLarge)) || len(interceptor.pendingWrites) != 0 {
						t.Fatalf("隐私拒绝必须零出站：%+v", result)
					}
				} else {
					want := body
					if mode == privacy.ModeRedact {
						want = bytes.ReplaceAll(body, []byte("SYNTHETIC-SECRET"), []byte("[masked]"))
					}
					if !bytes.Equal(result.Forward, want) || len(result.Reply) != 0 {
						t.Fatalf("追加输入未按隐私策略转发：forward=%s reply=%s", result.Forward, result.Reply)
					}
				}
				if len(state.active) != 0 || len(state.pending) != 0 || state.sequence != 0 || activity.Count() != 0 {
					t.Fatal("追加输入被误建为生成请求")
				}
				if strings.Contains(logs.String(), "SYNTHETIC-SECRET") {
					t.Fatal("日志泄露追加输入")
				}
			})
		}
	}
}

func TestSubscriptionWSSteerDrainAndResume(t *testing.T) {
	state, activity := activitySubmissionState()
	defer state.close("done")
	state.beginCreate([]byte(`{"type":"response.create","input":[]}`))
	interceptor := &subscriptionWSInterceptor{submissions: state}
	body := []byte(`{"type":"response.steer","previous_response_id":"parent","input":"update"}`)
	activity.BeginDrain("quit")
	rejected, err := interceptor.Intercept(context.Background(), subscriptionws.DirectionDownstreamToUpstream, body)
	if err != nil || rejected.Forward != nil || !bytes.Contains(rejected.Reply, []byte("codex_subscription_proxy_draining")) || len(interceptor.pendingWrites) != 0 {
		t.Fatalf("排空期间继续接收追加输入：%+v %v", rejected, err)
	}
	activity.CancelDrain("quit")
	allowed, err := interceptor.Intercept(context.Background(), subscriptionws.DirectionDownstreamToUpstream, body)
	if err != nil || !bytes.Equal(allowed.Forward, body) || activity.Count() != 1 || len(state.active) != 1 {
		t.Fatalf("取消退出后未恢复追加输入或改变了在途计数：%+v %v", allowed, err)
	}
}

func TestSubscriptionWSSteerIndependentPrivacyState(t *testing.T) {
	state, activity := activitySubmissionState()
	defer state.close("done")
	active, _ := state.beginCreate([]byte(`{"type":"response.create","input":[]}`))
	state.handleUpstream([]byte(`{"type":"response.created","response":{"id":"parent"}}`))
	filter := &p2aPrivacyFilter{version: 1, err: &privacy.PolicyError{StatusCode: 422, Code: privacy.CodeScanFailed}}
	interceptor := &subscriptionWSInterceptor{submissions: state, filter: filter}
	body := []byte(`{"type":"response.steer","previous_response_id":"parent","input":"update"}`)
	rejected, err := interceptor.Intercept(context.Background(), subscriptionws.DirectionDownstreamToUpstream, body)
	if err != nil || rejected.Forward != nil || !bytes.Contains(rejected.Reply, []byte(privacy.CodeScanFailed)) {
		t.Fatalf("追加输入隐私失败未明确拒绝：%+v %v", rejected, err)
	}
	filter.err = nil
	allowed, err := interceptor.Intercept(context.Background(), subscriptionws.DirectionDownstreamToUpstream, body)
	if err != nil || !bytes.Equal(allowed.Forward, body) || filter.calls != 2 {
		t.Fatalf("相同追加输入复用了上一帧的隐私结果：%+v %v calls=%d", allowed, err, filter.calls)
	}
	if active.settled || len(state.active) != 1 || activity.Count() != 1 || len(interceptor.pendingWrites) != 1 {
		t.Fatal("追加输入拒绝或重试改变了已有生成请求")
	}
}

type steerOutboundObserver struct {
	recordingSubmissionObserver
	writes []struct {
		requestID string
		size      int64
	}
}

func (o *steerOutboundObserver) SubmissionOutbound(s *subscriptionSubmission, size int64, _ time.Time) {
	o.writes = append(o.writes, struct {
		requestID string
		size      int64
	}{s.RequestID, size})
}

func TestSubscriptionWSSteerPreservesWriteAccounting(t *testing.T) {
	observer := &steerOutboundObserver{}
	state := newSubscriptionSubmissionState(context.Background(), "ws", observer)
	defer state.close("done")
	interceptor := &subscriptionWSInterceptor{submissions: state}
	bodies := []string{
		`{"type":"response.create","input":[]}`,
		`{"type":"response.steer","previous_response_id":"resp_one","input":"update"}`,
		`{"type":"response.create","input":"second request"}`,
	}
	for _, body := range bodies {
		result, err := interceptor.Intercept(context.Background(), subscriptionws.DirectionDownstreamToUpstream, []byte(body))
		if err != nil || !bytes.Equal(result.Forward, []byte(body)) || len(result.Reply) != 0 {
			t.Fatalf("合法事件未转发：%+v %v", result, err)
		}
	}
	if len(state.active) != 2 || len(state.pending) != 2 {
		t.Fatal("追加输入污染生成请求队列")
	}
	first, second := state.active[0].RequestID, state.active[1].RequestID
	for _, body := range bodies {
		interceptor.Forwarded(subscriptionws.DirectionDownstreamToUpstream, int64(len(body)), time.Now())
	}
	if len(observer.writes) != 2 || observer.writes[0].requestID != first || observer.writes[0].size != int64(len(bodies[0])) ||
		observer.writes[1].requestID != second || observer.writes[1].size != int64(len(bodies[2])) || len(interceptor.pendingWrites) != 0 {
		t.Fatalf("追加输入导致后续请求出站字节错位：%+v", observer.writes)
	}
}

func TestSubscriptionWSSteerReceiptsDoNotSettleGeneration(t *testing.T) {
	state, activity := activitySubmissionState()
	defer state.close("done")
	observer := &recordingSubmissionObserver{}
	state.observer = observer
	interceptor := &subscriptionWSInterceptor{submissions: state}
	active, _ := state.beginCreate([]byte(`{"type":"response.create","input":[]}`))
	state.handleUpstream([]byte(`{"type":"response.created","response":{"id":"parent"}}`))
	beforeBytes := active.responseBytes
	for _, body := range []string{
		`{"type":"response.steer.accepted","sequence_number":2,"steer":{"id":"s1","previous_response_id":"parent"}}`,
		`{"type":"response.steer.pending","sequence_number":3,"steer":{"id":"s1","previous_response_id":"parent"},"reason":"waiting_for_required_input","required_input":[{"type":"function_call_output","call_id":"call_1"}]}`,
		`{"type":"response.steer.failed","sequence_number":4,"steer":{"previous_response_id":"parent","input":"update"},"error":{"type":"invalid_request_error","code":"steering_not_supported","message":"unsupported"}}`,
	} {
		result, err := interceptor.Intercept(context.Background(), subscriptionws.DirectionUpstreamToDownstream, []byte(body))
		if err != nil || !bytes.Equal(result.Forward, []byte(body)) || result.Forwarded != nil {
			t.Fatalf("追加输入回执被篡改或误触发终态：%+v %v", result, err)
		}
	}
	if active.responseBytes != beforeBytes || active.settled || activity.Count() != 1 || observer.count("terminal") != 0 {
		t.Fatal("控制回执改变了生成统计或状态")
	}
	state.handleUpstream([]byte(`{"type":"response.incomplete","response":{"id":"parent","incomplete_details":{"reason":"steered"}}}`))
	state.handleUpstream([]byte(`{"type":"response.created","response":{"id":"successor","model":"synthetic"}}`))
	if len(state.active) != 1 || !state.active[0].ServerInitiated || state.active[0].ResponseID != "successor" || activity.Count() != 1 {
		t.Fatal("追加输入的自动续答未独立追踪")
	}
	state.handleUpstream([]byte(`{"type":"response.completed","response":{"id":"successor","usage":{"input_tokens":3,"output_tokens":2}}}`))
	if observer.count("started") != 2 || observer.count("terminal") != 2 || activity.Count() != 0 {
		t.Fatal("追加输入续答重复或遗漏结算")
	}
}

func TestSubscriptionWSSteerRejectsAmbiguousJSON(t *testing.T) {
	state := newSubscriptionSubmissionState(context.Background(), "ws", nil)
	filter := &p2aPrivacyFilter{version: 1}
	interceptor := &subscriptionWSInterceptor{submissions: state, filter: filter}
	for _, body := range []string{
		`{"type":"response.create","type":"response.steer","input":"update"}`,
		`{"type":"response.steer","previous_response_id":"one","previous_response_id":"two","input":"update"}`,
		`{"type":"response.steer","input":"update"`,
	} {
		result, err := interceptor.Intercept(context.Background(), subscriptionws.DirectionDownstreamToUpstream, []byte(body))
		if err != nil || result.Forward != nil || !bytes.Contains(result.Reply, []byte(subscriptionInvalidJSONCode)) {
			t.Fatalf("歧义正文未被拒绝：%+v %v", result, err)
		}
	}
	if filter.calls != 0 || len(state.active) != 0 || len(interceptor.pendingWrites) != 0 {
		t.Fatal("歧义正文进入隐私处理或追踪队列")
	}
}

func TestSubscriptionWSSteerUnknownEventDiagnostics(t *testing.T) {
	for _, tc := range []struct{ eventType, logged string }{
		{"response.cancel", "response.cancel"},
		{"response.unknown", "response.unknown"},
		{"", "missing"},
		{"response.\nSYNTHETIC-SECRET", "invalid"},
		{strings.Repeat("x", 65), "invalid"},
	} {
		t.Run(tc.logged, func(t *testing.T) {
			var logs bytes.Buffer
			interceptor := &subscriptionWSInterceptor{
				submissions: newSubscriptionSubmissionState(context.Background(), "ws", nil),
				logger:      slog.New(slog.NewJSONHandler(&logs, nil)),
			}
			body, _ := json.Marshal(map[string]string{"type": tc.eventType, "input": "SYNTHETIC-SECRET"})
			result, err := interceptor.Intercept(context.Background(), subscriptionws.DirectionDownstreamToUpstream, body)
			if err != nil || result.Forward != nil || !bytes.Contains(result.Reply, []byte(subscriptionUnsupportedWSEventCode)) {
				t.Fatalf("未知事件被放行：%+v %v", result, err)
			}
			var record map[string]any
			if err := json.Unmarshal(logs.Bytes(), &record); err != nil {
				t.Fatal(err)
			}
			if record["event_type"] != tc.logged || strings.Contains(logs.String(), "SYNTHETIC-SECRET") {
				t.Fatalf("诊断未安全记录事件类型：%s", logs.String())
			}
		})
	}
}
