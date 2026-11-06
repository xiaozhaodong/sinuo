package proxy

import (
	"bytes"
	"context"
	"testing"
	"time"

	"sinuo/internal/privacy"
	"sinuo/internal/proxy/subscriptionws"
)

// 帧形状来自 Codex responses_websocket.rs；追加输入与子代理邮箱抢占共用此控制事件。
const subscriptionTestInterrupt = `{"type":"response.interrupt","response_id":"parent","mode":"discard_partial_items"}`

func TestSubscriptionWSInterruptForwardAndDrain(t *testing.T) {
	for _, drain := range []bool{false, true} {
		state, activity := activitySubmissionState()
		defer state.close("done")
		active, _ := beginTestSubscriptionCreate(t, state, []byte(`{"type":"response.create","input":[]}`))
		state.handleUpstream([]byte(`{"type":"response.created","response":{"id":"parent"}}`))
		filter := &p2aPrivacyFilter{version: 1}
		interceptor := &subscriptionWSInterceptor{submissions: state, filter: filter}
		if drain {
			activity.BeginDrain("quit")
		}
		result, err := interceptor.Intercept(context.Background(), subscriptionws.DirectionDownstreamToUpstream, []byte(subscriptionTestInterrupt))
		if err != nil || string(result.Forward) != subscriptionTestInterrupt || len(result.Reply) != 0 || filter.calls != 1 {
			t.Fatalf("中断未经过隐私边界原样转发 drain=%t: %+v %v calls=%d", drain, result, err, filter.calls)
		}
		if active.settled || activity.Count() != 1 || len(state.active) != 1 || len(state.pending) != 0 || state.sequence != 1 {
			t.Fatal("发送中断提前结算或新建了生成请求")
		}
		if len(interceptor.pendingWrites) != 1 || interceptor.pendingWrites[0] != nil {
			t.Fatal("中断没有保留空写回调位置")
		}
	}
}

func TestSubscriptionWSInterruptPrivacyAndJSONRejection(t *testing.T) {
	state := newTestSubscriptionSubmissionState(context.Background(), "ws", nil)
	defer state.close("done")
	active, _ := beginTestSubscriptionCreate(t, state, []byte(`{"type":"response.create"}`))
	filter := &p2aPrivacyFilter{version: 1, err: &privacy.PolicyError{StatusCode: 422, Code: privacy.CodeScanFailed}}
	interceptor := &subscriptionWSInterceptor{submissions: state, filter: filter}
	for _, body := range []string{
		subscriptionTestInterrupt,
		`{"type":"response.interrupt","type":"response.create"}`,
		`{"type":"response.interrupt","response_id":"one","response_id":"two"}`,
	} {
		result, err := interceptor.Intercept(context.Background(), subscriptionws.DirectionDownstreamToUpstream, []byte(body))
		want := subscriptionInvalidJSONCode
		if body == subscriptionTestInterrupt {
			want = privacy.CodeScanFailed
		}
		if err != nil || result.Forward != nil || !bytes.Contains(result.Reply, []byte(want)) {
			t.Fatalf("中断隐私/歧义拒绝失效：%+v %v", result, err)
		}
	}
	if filter.calls != 1 || active.settled || len(interceptor.pendingWrites) != 0 {
		t.Fatal("拒绝影响了生成状态或仍有出站消息")
	}
}

func TestSubscriptionWSInterruptPreservesWriteAccounting(t *testing.T) {
	observer := &steerOutboundObserver{}
	state := newTestSubscriptionSubmissionState(context.Background(), "ws", observer)
	defer state.close("done")
	interceptor := &subscriptionWSInterceptor{submissions: state}
	bodies := []string{
		`{"type":"response.create","input":[]}`,
		subscriptionTestInterrupt,
		`{"type":"response.steer","previous_response_id":"parent","input":"synthetic"}`,
		`{"type":"response.create","previous_response_id":"parent","input":"follow-up"}`,
	}
	for _, body := range bodies {
		result, err := interceptor.Intercept(context.Background(), subscriptionws.DirectionDownstreamToUpstream, []byte(body))
		if err != nil || string(result.Forward) != body || len(result.Reply) != 0 {
			t.Fatalf("混合事件未转发：%+v %v", result, err)
		}
	}
	for _, body := range bodies {
		interceptor.Forwarded(subscriptionws.DirectionDownstreamToUpstream, int64(len(body)), time.Now())
	}
	if len(observer.writes) != 2 || len(state.active) != 2 || len(interceptor.pendingWrites) != 0 ||
		observer.writes[0].requestID != state.active[0].RequestID || observer.writes[0].size != int64(len(bodies[0])) ||
		observer.writes[1].requestID != state.active[1].RequestID || observer.writes[1].size != int64(len(bodies[3])) {
		t.Fatalf("中断/追加输入造成请求字节错位：%+v", observer.writes)
	}
}

func TestSubscriptionWSInterruptReceiptsAndItemBoundary(t *testing.T) {
	state, activity := activitySubmissionState()
	defer state.close("done")
	observer := &recordingSubmissionObserver{}
	state.observer = observer
	interceptor := &subscriptionWSInterceptor{submissions: state}
	first, _ := beginTestSubscriptionCreate(t, state, []byte(`{"type":"response.create"}`))
	state.handleUpstream([]byte(`{"type":"response.created","response":{"id":"parent"}}`))
	second, _ := beginTestSubscriptionCreate(t, state, []byte(`{"type":"response.create"}`))
	state.handleUpstream([]byte(`{"type":"response.created","response":{"id":"other"}}`))
	beforeFirst, beforeSecond := first.responseBytes, second.responseBytes
	for _, body := range []string{
		`{"type":"response.interrupt.accepted","response_id":"parent"}`,
		`{"type":"response.interrupt.failed","response_id":"parent","error":{"code":"interrupt_not_supported"}}`,
		`{"type":"response.interrupt.accepted","response_id":"already-completed"}`,
	} {
		result, err := interceptor.Intercept(context.Background(), subscriptionws.DirectionUpstreamToDownstream, []byte(body))
		if err != nil || string(result.Forward) != body || result.Forwarded != nil {
			t.Fatalf("控制回执未原样透传：%+v %v", result, err)
		}
	}
	if first.responseBytes != beforeFirst || second.responseBytes != beforeSecond || activity.Count() != 2 || observer.count("terminal") != 0 {
		t.Fatal("中断回执污染生成输出或结算")
	}
	// 单个输出项被丢弃不是整个 response 的终态，其他 lane 也必须保持在途。
	item := `{"type":"response.output_item.interrupted","response_id":"parent","item_id":"reason-1","output_index":0}`
	result, err := interceptor.Intercept(context.Background(), subscriptionws.DirectionUpstreamToDownstream, []byte(item))
	if err != nil || string(result.Forward) != item || result.Forwarded != nil || activity.Count() != 2 || first.settled || second.settled {
		t.Fatal("输出项中断提前结束了生成请求")
	}
}

func TestSubscriptionWSInterruptTerminalSemantics(t *testing.T) {
	for _, reason := range []string{"interrupted", "steered", "max_output_tokens", "content_filter"} {
		body := []byte(`{"type":"response.incomplete","response":{"id":"parent","incomplete_details":{"reason":"` + reason + `"},"usage":{"input_tokens":9,"output_tokens":2,"input_tokens_details":{"cached_tokens":4}}}}`)
		parser := NewTokenParserWithRequestID("interrupt-sse")
		parser.ParseSSELineV2("event: response.incomplete")
		parser.ParseSSELineV2("data: " + string(body))
		for _, result := range []*ParseResult{ParseResponsesEventJSON("response.incomplete", body), parser.ParseSSELineV2("")} {
			if result == nil || !result.IsCompleted || !result.HasTokenUsage || result.TokenUsage.InputTokens != 9 || result.TokenUsage.OutputTokens != 2 || result.TokenUsage.CacheReadTokens != 4 {
				t.Fatalf("中断尾部用量丢失：%+v", result)
			}
			switch reason {
			case "interrupted":
				if !result.IsCancelled || result.ErrorInfo != nil || result.Status != "cancelled" || result.StopReason != "incomplete:interrupted" {
					t.Fatalf("主动中断被当作上游故障或成功：%+v", result)
				}
			case "steered":
				if result.IsCancelled || result.ErrorInfo != nil || result.Status != "steered" {
					t.Fatalf("steer 终态发生退化：%+v", result)
				}
			default:
				if result.IsCancelled || result.ErrorInfo == nil || result.Status != StatusErrorAPI {
					t.Fatalf("真实失败被中断分支吞掉：%+v", result)
				}
			}
		}
	}
}

func TestSubscriptionWSInterruptTerminalWaitsForDelivery(t *testing.T) {
	state, activity := activitySubmissionState()
	observer := &recordingSubmissionObserver{}
	state.observer = observer
	defer state.close("done")
	beginTestSubscriptionCreate(t, state, []byte(`{"type":"response.create"}`))
	state.handleUpstream([]byte(`{"type":"response.created","response":{"id":"parent"}}`))
	beginTestSubscriptionCreate(t, state, []byte(`{"type":"response.create"}`))
	state.handleUpstream([]byte(`{"type":"response.created","response":{"id":"other"}}`))
	body := []byte(`{"type":"response.incomplete","response":{"id":"parent","incomplete_details":{"reason":"interrupted"}}}`)
	result, forwarded, err := state.handleUpstreamForForward(body)
	if err != nil || result == nil || !result.IsCancelled || forwarded == nil || activity.Count() != 2 || observer.count("terminal") != 1 {
		t.Fatal("中断终态未等待写出或结算了其他 lane")
	}
	state.handleUpstream(body) // 重复终态不能释放原尾帧尚未完成的租约。
	state.handleUpstream([]byte(`{"type":"response.interrupt.accepted","response_id":"parent"}`))
	if activity.Count() != 2 || observer.count("terminal") != 1 {
		t.Fatal("重复终态/迟到回执重复结算")
	}
	forwarded()
	forwarded()
	if activity.Count() != 1 || len(state.active) != 1 || state.active[0].ResponseID != "other" {
		t.Fatal("中断尾帧释放了错误请求或重复释放")
	}
	state.close("subscription_client_disconnected")
	if activity.Count() != 0 || observer.count("terminal") != 1 || observer.count("interrupted") != 1 {
		t.Fatal("终态后断连覆盖了已结算请求")
	}
}
