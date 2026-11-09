package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/tidwall/gjson"

	"sinuo/internal/privacy"
	"sinuo/internal/proxy/subscriptionws"
	"sinuo/internal/tracking"
)

func (f liveRestoreFilter) CurrentSnapshot() *privacy.Snapshot { return f.Snapshot }

// 同一帧不论是否回填，读侧必须看到相同的归属、用量、思考和内容阶段。
func TestSubscriptionWSReadMatchesOriginalObservation(t *testing.T) {
	for _, raw := range []string{
		`{"type":"response.output_text.delta","response_id":"r","delta":"visible"}`,
		`{"type":"response.function_call_arguments.delta","delta":"{\"path\":\"a\\\\b\"}"}`,
		`{"type":"response.completed","response_id":"outer","response":{"id":"inner","model":"synthetic","usage":{"input_tokens":0,"output_tokens":42,"output_tokens_details":{"reasoning_tokens":12}}}}`,
		`{"type":"response.incomplete","response":{"id":"r","incomplete_details":{"reason":"interrupted"}}}`,
		`{"type":"error","error":{"type":"upstream_error","message":"synthetic"}}`,
		`{"type":"response.steer.accepted","stream_id":"a"}`,
		`{"type":"codex.metadata","extra":{"nested":[1,2]}}`,
		`{"type":"response.completed","response_id":"outer","response":{"id":null}}`,
		`{"type":"response.completed","response_id":42,"response":{"id":"inner"}}`,
		`{"type":"response.created","Type":"response.completed","response":{"Id":"one","id":"two"}}`,
		`{"type":"response.completed","response_id":"first","RESPONSE_ID":"last"}`,
		`{"type":"response.completed","Response":{"ID":"upper"},"response":{}}`,
		`{"type":"response.completed","response":["untyped"]}`,
		`{"type":"response.completed","response":{"id":"r"},"untracked":1e400}`,
	} {
		on, off := readSubscriptionWSEvent([]byte(raw), true), readSubscriptionWSEvent([]byte(raw), false)
		if !reflect.DeepEqual(on, off) {
			t.Fatalf("读侧观测不一致: %s\n开 %+v %+v\n关 %+v %+v", raw, on, on.result, off, off.result)
		}
	}
}

// 跟踪单测没有网络写队列，由调用方决定何时确认交付。
func (s *subscriptionSubmissionState) handleUpstreamForForward(payload []byte) (*ParseResult, func(), error) {
	ev, err := s.observeUpstream(payload, false)
	if err != nil || !ev.terminal {
		return ev.result, nil, err
	}
	return ev.result, s.finishTerminal(ev.submission, ev.responseID, ev.result), nil
}

// 使用真实双跳连接，客户端发送原生 JSON 消息，两个响应复用相同 output_index。
func TestSubscriptionWSRestoreInterleaved(t *testing.T) {
	const other = "synthetic-second-value"
	const timing = `{"type":"responsesapi.websocket_timing","timing_metrics":{"duration_ms":12}}`
	filter := newRestoreTestFilter(t)
	rules, err := privacy.CompileRules([]privacy.Rule{
		{ID: 1, Name: "first", Enabled: true, MatchType: privacy.MatchTypeLiteral, Pattern: routeTestValue, Placeholder: "[mask]", Action: privacy.ActionRedact},
		{ID: 2, Name: "second", Enabled: true, MatchType: privacy.MatchTypeLiteral, Pattern: other, Placeholder: "[mask]", Action: privacy.ActionRedact},
	})
	if err != nil {
		t.Fatal(err)
	}
	filter.Rules = rules
	tracker := newLifecyclePanelTestTracker(t)
	env := newSubscriptionTestEnv(t, func(w http.ResponseWriter, r *http.Request) {
		up, err := websocket.Accept(w, r, &websocket.AcceptOptions{})
		if err != nil {
			t.Error(err)
			return
		}
		defer up.CloseNow()
		read := func() []byte {
			_, b, e := up.Read(r.Context())
			if e != nil {
				t.Error(e)
			}
			return b
		}
		write := func(raw string) {
			if err := up.Write(r.Context(), websocket.MessageText, []byte(raw)); err != nil {
				t.Error(err)
			}
		}
		text, tool := read(), read()
		a, b := string(wireToken.Find(text)), string(wireToken.Find(tool))
		if a == "" || b == "" || a == b || bytes.Contains(text, []byte("private-quote")) || bytes.Contains(tool, []byte(other)) || r.Header.Get("Authorization") != "Bearer synthetic-client" {
			t.Error("上游两个请求未分别脱敏")
			return
		}
		args := fmt.Sprintf(`{"value":%q}`, b)
		if got := gjson.GetBytes(tool, "response.input.0.arguments").Str; got != args {
			t.Error("嵌套工具入参的字符串叶子未脱敏")
			return
		}
		for _, lane := range []string{"B", "A"} {
			write(fmt.Sprintf(`{"type":"response.created","stream_id":%q,"response":{"id":%q}}`, lane, lane))
		}
		write(timing)
		for n := 0; n < 2; n++ {
			textDelta := []string{a[:7], a[7:]}[n]
			argDelta := []string{args[:15], args[15:]}[n]
			write(fmt.Sprintf(`{"type":"response.output_text.delta","stream_id":"A","output_index":0,"content_index":0,"item_id":"msg","delta":%q}`, textDelta))
			write(fmt.Sprintf(`{"type":"response.function_call_arguments.delta","response_id":"B","output_index":0,"item_id":"fc","delta":%q}`, argDelta))
		}
		write(fmt.Sprintf(`{"type":"response.function_call_arguments.done","response_id":"B","output_index":0,"item_id":"fc","arguments":%q}`, args))
		write(fmt.Sprintf(`{"type":"response.completed","response":{"id":"B","output":[{"type":"function_call","id":"fc","arguments":%q}],"usage":{"input_tokens":3,"output_tokens":22}}}`, args))
		write(`{"type":"response.completed","response":{"id":"B","usage":{"input_tokens":999,"output_tokens":999}}}`)
		write(fmt.Sprintf(`{"type":"response.output_text.done","response_id":"A","output_index":0,"content_index":0,"item_id":"msg","text":%q}`, a))
		write(fmt.Sprintf(`{"type":"response.completed","response":{"id":"A","output":[{"type":"message","id":"msg","content":[{"type":"output_text","text":%q}]}],"usage":{"input_tokens":3,"output_tokens":11}}}`, a))
		_, _, _ = up.Read(r.Context())
	}, "subscription")
	env.handler.proxy.SetPrivacyFilter(filter)
	env.handler.proxy.SetUsageTracker(tracker)
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(env.server.URL, "http")+"/subscription/"+subscriptionTestSecret+"/backend-api/codex/responses", &websocket.DialOptions{HTTPHeader: http.Header{"Authorization": {"Bearer synthetic-client"}}})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
	args := fmt.Sprintf(`{"value":%q}`, other)
	for _, create := range []string{fmt.Sprintf(`{"type":"response.create","stream_id":"A","model":"gpt-A","input":%q}`, routeTestValue), fmt.Sprintf(`{"type":"response.create","stream_id":"B","response":{"model":"gpt-B","input":[{"type":"function_call","arguments":%q}]}}`, args)} {
		if err := conn.Write(ctx, websocket.MessageText, []byte(create)); err != nil {
			t.Fatal(err)
		}
	}
	var text, tool strings.Builder
	var sawTiming bool
	for done := 0; done < 2; {
		_, p, err := conn.Read(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if !json.Valid(p) || wireToken.Match(p) {
			t.Fatal("下游未收到原生回填消息", string(p))
		}
		switch gjson.GetBytes(p, "type").Str {
		case "responsesapi.websocket_timing":
			sawTiming = string(p) == timing
		case "response.output_text.delta":
			text.WriteString(gjson.GetBytes(p, "delta").Str)
		case "response.function_call_arguments.delta":
			tool.WriteString(gjson.GetBytes(p, "delta").Str)
		case "response.completed":
			done++
			if gjson.GetBytes(p, "response.id").Str == "A" {
				if gjson.GetBytes(p, "response.output.0.content.0.text").Str != routeTestValue {
					t.Fatal("文本快照不一致")
				}
			} else if gjson.GetBytes(p, "response.output.0.arguments").Str != args {
				t.Fatal("工具快照不一致")
			}
		case "error":
			t.Fatal(string(p))
		}
	}
	if !sawTiming || text.String() != routeTestValue || tool.String() != args {
		t.Fatal("交错增量串线", text.String(), tool.String())
	}
	_ = conn.Close(websocket.StatusNormalClosure, "done")
	rows, _, err := tracker.QueryRequestDetailsWithHotPool(ctx, &tracking.QueryOptions{Limit: 10})
	if err != nil || len(rows) != 2 {
		t.Fatal(len(rows), err)
	}
	for _, row := range rows {
		data := timingLifecycleData(t, tracker, row.RequestID)
		r := gjson.Get(data.PrivacyScanJSON, "restore")
		expectedCount, expectedField := int64(11), "text"
		if data.Detail.RequestModel == "gpt-B" {
			expectedCount, expectedField = 22, "tool"
		}
		if int64(data.Detail.OutputTokens) != expectedCount || r.Get(expectedField).Int() != 1 || r.Get("status").Str != "completed" {
			t.Fatalf("回填与原始用量关联错误: %v %s %d", data.Detail.RequestModel, r.Raw, data.Detail.OutputTokens)
		}
	}
}

// 显式分开上游读取和下游写入，覆盖终态排队期间的重复帧，以及终态前后迟到的控制回执。
func TestSubscriptionWSRestoreReceiptsAndDuplicateTerminal(t *testing.T) {
	state, activity := activitySubmissionState()
	defer state.close("closed")
	filter := newRestoreTestFilter(t)
	i := &subscriptionWSInterceptor{filter: filter, submissions: state}
	ctx := context.Background()
	create, err := i.Intercept(ctx, subscriptionws.DirectionDownstreamToUpstream, []byte(`{"type":"response.create","stream_id":"lane","input":[]}`))
	if err != nil || create.Deliver == nil {
		t.Fatal("提交未入队", err)
	}
	if err = create.Deliver(func([]byte) error { return nil }, nil); err != nil {
		t.Fatal(err)
	}
	read := func(raw string) subscriptionws.InterceptResult {
		t.Helper()
		r, err := i.Intercept(ctx, subscriptionws.DirectionUpstreamToDownstream, []byte(raw))
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	var sequences []int64
	write := func(r subscriptionws.InterceptResult) {
		t.Helper()
		send := func(b []byte) error {
			sequences = append(sequences, gjson.GetBytes(b, "sequence_number").Int())
			return nil
		}
		if r.Deliver != nil {
			if err := r.Deliver(send, nil); err != nil {
				t.Fatal(err)
			}
		} else if r.Forward != nil {
			_ = send(r.Forward)
		}
		if r.Forwarded != nil {
			r.Forwarded()
		}
	}
	const terminal = `{"type":"response.completed","response":{"id":"A"},"sequence_number":4}`
	frames := []string{
		`{"type":"response.created","response":{"id":"A"},"stream_id":"lane","sequence_number":0}`,
		`{"type":"response.output_text.delta","output_index":0,"content_index":0,"delta":"literal {","sequence_number":1}`,
		`{"type":"response.output_text.done","output_index":0,"content_index":0,"text":"literal {","sequence_number":2}`,
		`{"type":"response.interrupt.accepted","response_id":"A","sequence_number":3}`,
		terminal,
		terminal,
		`{"type":"response.steer.pending","steer":{"previous_response_id":"A"},"sequence_number":5}`,
	}
	var queued []subscriptionws.InterceptResult
	for _, frame := range frames {
		queued = append(queued, read(frame))
	}
	for _, r := range queued {
		write(r)
	}
	// 此后的旧响应仅剩有界的序号记录。开关切换也不能重交付已完成的终态。
	filter.Settings.ResponseRestoreEnabled = false
	write(read(terminal))
	write(read(`{"type":"response.interrupt.failed","response_id":"A","sequence_number":6}`))
	write(read(`{"type":"response.steer.pending","stream_id":"lane","sequence_number":7}`))
	if !slices.Equal(sequences, []int64{0, 1, 2, 3, 4, 5, 6, 7, 8}) || activity.Count() != 0 {
		t.Fatalf("下游序号 %v，在途请求 %d", sequences, activity.Count())
	}
}

func TestSubscriptionWSRestorePublishAndSuccessor(t *testing.T) {
	state, _ := activitySubmissionState()
	defer state.close("closed")
	filter := newRestoreTestFilter(t)
	i := &subscriptionWSInterceptor{filter: filter, submissions: state}
	input := func(frame string) (sent, reply []byte) {
		t.Helper()
		m, err := i.Intercept(context.Background(), subscriptionws.DirectionDownstreamToUpstream, []byte(frame))
		if err != nil {
			t.Fatal(err)
		}
		if m.Reply != nil {
			return nil, m.Reply
		}
		if m.Deliver == nil {
			t.Fatal("回填请求缺少实际写入回调")
		}
		err = m.Deliver(func(b []byte) error {
			sent = bytes.Clone(b)
			i.Forwarded(subscriptionws.DirectionDownstreamToUpstream, int64(len(b)), time.Now())
			return nil
		}, func(b []byte, after func()) error {
			reply = b
			if after != nil {
				after()
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		return sent, reply
	}
	output := func(frame string) []byte {
		t.Helper()
		m, err := i.Intercept(context.Background(), subscriptionws.DirectionUpstreamToDownstream, []byte(frame))
		if err != nil {
			t.Fatal(err)
		}
		last := m.Forward
		if m.Deliver != nil {
			err = m.Deliver(func(b []byte) error { last = bytes.Clone(b); return nil }, nil)
			if err != nil {
				t.Fatal(err, string(last))
			}
		}
		if m.Forwarded != nil {
			m.Forwarded()
		}
		return last
	}
	// 追加输入发布的映射在服务端主动生成的后继响应上也可回填。
	input(`{"type":"response.create","stream_id":"lane","input":"clean"}`)
	output(`{"type":"response.created","stream_id":"lane","response":{"id":"parent"}}`)
	sent, _ := input(fmt.Sprintf(`{"type":"response.steer","previous_response_id":"parent","input":%q}`, routeTestValue))
	token := string(wireToken.Find(sent))
	if token == "" {
		t.Fatal("steer 原文未遮蔽")
	}
	output(`{"type":"response.steer.accepted","response_id":"parent"}`)
	output(`{"type":"response.function_call_arguments.delta","response_id":"parent","item_id":"fc","output_index":0,"delta":"{"}`)
	interrupt := `{"type":"response.interrupt","response_id":"parent"}`
	if sent, _ := input(interrupt); string(sent) != interrupt {
		t.Fatal("interrupt 帧发生变化")
	}
	output(`{"type":"response.output_item.interrupted","response_id":"parent","output_index":0,"item_id":"fc"}`)
	output(`{"type":"response.incomplete","response":{"id":"parent","status":"incomplete","incomplete_details":{"reason":"interrupted"}}}`)
	output(`{"type":"response.created","stream_id":"lane","response":{"id":"successor"}}`)
	last := output(fmt.Sprintf(`{"type":"response.completed","response":{"id":"successor","output":[{"type":"message","id":"m","content":[{"type":"output_text","text":%q}]}]}}`, token))
	if gjson.GetBytes(last, "response.output.0.content.0.text").Str != routeTestValue {
		t.Fatal("后继响应缺少回填")
	}
	res, _ := i.Intercept(context.Background(), subscriptionws.DirectionDownstreamToUpstream, []byte(`{"type":"response.create","input":"queued"}`))
	filter.Dict.Retire()
	if err := res.Deliver(func([]byte) error { t.Fatal("旧字典请求已出站"); return nil }, func(b []byte, after func()) error {
		if !bytes.Contains(b, []byte("privacy_restore_rejected")) {
			t.Fatal("没有返回策略错误", string(b))
		}
		if after != nil {
			after()
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// 首个连接已从上游读到带 usage 的终态，下游在写入前断开；重连只带历史引用。
func TestSubscriptionWSRestoreDisconnectAndResume(t *testing.T) {
	filter := newRestoreTestFilter(t)
	ctx := context.Background()
	token := ""
	for round := 0; round < 2; round++ {
		tracker := newLifecyclePanelTestTracker(t)
		state := newTestSubscriptionSubmissionState(ctx, "connection", newSubscriptionTrackingObserver(tracker, nil))
		i := &subscriptionWSInterceptor{filter: filter, submissions: state}
		create := fmt.Sprintf(`{"type":"response.create","model":"gpt-test","input":%q}`, routeTestValue)
		if round == 1 {
			create = `{"type":"response.create","model":"gpt-test","previous_response_id":"prior","input":"continue"}`
		}
		request, err := i.Intercept(ctx, subscriptionws.DirectionDownstreamToUpstream, []byte(create))
		if err != nil || request.Deliver == nil {
			t.Fatal(err)
		}
		if err := request.Deliver(func(raw []byte) error {
			if round == 0 {
				token = string(wireToken.Find(raw))
			} else if wireToken.Match(raw) {
				t.Fatal("续接请求意外引入新占位符")
			}
			return nil
		}, nil); err != nil {
			t.Fatal(err)
		}
		if token == "" {
			t.Fatal("没有发布初始映射")
		}
		created, err := i.Intercept(ctx, subscriptionws.DirectionUpstreamToDownstream, []byte(`{"type":"response.created","response":{"id":"r"}}`))
		if err != nil || created.Deliver == nil {
			t.Fatal(err)
		}
		if err := created.Deliver(func([]byte) error { return nil }, nil); err != nil {
			t.Fatal(err)
		}
		raw := fmt.Sprintf(`{"type":"response.completed","response":{"id":"r","output":[{"type":"message","content":[{"type":"output_text","text":%q}]}],"usage":{"input_tokens":7,"output_tokens":8}}}`, token)
		end, err := i.Intercept(ctx, subscriptionws.DirectionUpstreamToDownstream, []byte(raw))
		if err != nil || end.Deliver == nil || end.Forwarded == nil {
			t.Fatal("终态缺少交付或结算", err)
		}
		if round == 0 {
			if err := end.Deliver(func([]byte) error { return io.ErrClosedPipe }, nil); !errors.Is(err, io.ErrClosedPipe) {
				t.Fatal("丢失写失败", err)
			}
		} else {
			if err := end.Deliver(func(b []byte) error {
				if gjson.GetBytes(b, "response.output.0.content.0.text").Str != routeTestValue {
					t.Fatal("续接快照回填失败")
				}
				return nil
			}, nil); err != nil {
				t.Fatal(err)
			}
			end.Forwarded()
		}
		state.close(tracking.SubscriptionClientDisconnected)
		row := onlyPhaseRequest(t, tracker)
		wantRestore, wantStatus := "interrupted", "cancelled"
		if round == 1 {
			wantRestore, wantStatus = "completed", "completed"
		}
		if row.Detail.Status != wantStatus || row.Detail.OutputTokens != 8 || gjson.Get(row.PrivacyScanJSON, "restore.status").Str != wantRestore || gjson.Get(row.PrivacyScanJSON, "restore.text").Int() != int64(round) {
			t.Fatalf("结算或交付计数错误: %s %d %s", row.Detail.Status, row.Detail.OutputTokens, row.PrivacyScanJSON)
		}
	}
}

func TestSubscriptionWSRestoreRejectsAmbiguousLane(t *testing.T) {
	state, _ := activitySubmissionState()
	defer state.close("closed")
	i := &subscriptionWSInterceptor{filter: newRestoreTestFilter(t), submissions: state}
	for _, id := range []string{"a", "b"} {
		r, err := i.Intercept(context.Background(), subscriptionws.DirectionDownstreamToUpstream, []byte(`{"type":"response.create","input":[]}`))
		if err != nil {
			t.Fatal(err)
		}
		if err = r.Deliver(func([]byte) error { return nil }, nil); err != nil {
			t.Fatal(err)
		}
		r, err = i.Intercept(context.Background(), subscriptionws.DirectionUpstreamToDownstream, []byte(fmt.Sprintf(`{"type":"response.created","response":{"id":%q}}`, id)))
		if err != nil {
			t.Fatal(err)
		}
		if err = r.Deliver(func([]byte) error { return nil }, nil); err != nil {
			t.Fatal(err)
		}
	}
	r, err := i.Intercept(context.Background(), subscriptionws.DirectionUpstreamToDownstream, []byte(`{"type":"response.output_text.delta","content_index":0,"output_index":0,"delta":"ambiguous"}`))
	if err != nil {
		t.Fatal(err)
	}
	var reported []byte
	if err := r.Deliver(func(b []byte) error { reported = b; return nil }, nil); err == nil || !bytes.Contains(reported, []byte("privacy_restore_failed")) || bytes.Contains(reported, []byte("ambiguous")) {
		t.Fatal("上游增量归属不明仍被交付")
	}
}
