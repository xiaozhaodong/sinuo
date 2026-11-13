package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"sinuo/internal/privacy"
	"sinuo/internal/proxy/handlers"
	"sinuo/internal/proxy/subscriptionws"
	"sinuo/internal/tracking"
)

// 追加输入/预热 × 扫描/发布失败 × 放行/拒绝，另用普通生成请求作对照。
// 同时覆盖关闭追踪和真实数据库，通知不能依赖生成记录，也不能污染父响应。
func TestWSAuxiliaryPrivacyNotifications(t *testing.T) {
	for _, tracked := range []bool{false, true} {
		for _, kind := range []string{"generation", "warmup", "steer"} {
			for _, failure := range []string{"scan", "publish"} {
				for _, mode := range []string{privacy.OnErrorFailOpen, privacy.OnErrorFailClosed} {
					t.Run(fmt.Sprintf("tracked=%t/%s/%s/%s", tracked, kind, failure, mode), func(t *testing.T) {
						var tracker *tracking.UsageTracker
						if tracked {
							tracker = newSubscriptionUnitTracker(t)
						}
						state, activity := activitySubmissionState()
						defer state.close("test_finished")
						bus := &privacyNoticeTestBus{}
						observer := newSubscriptionTrackingObserver(tracker, bus)
						state.observer = observer
						filter := newRestoreTestFilter(t)
						filter.Settings.OnError = mode
						i := &subscriptionWSInterceptor{filter: filter, submissions: state}
						for _, frame := range []struct {
							direction subscriptionws.Direction
							body      string
						}{
							{subscriptionws.DirectionDownstreamToUpstream, `{"type":"response.create","stream_id":"parent-lane","input":"ordinary"}`},
							{subscriptionws.DirectionUpstreamToDownstream, `{"type":"response.created","stream_id":"parent-lane","response":{"id":"parent"}}`},
						} {
							if _, err := policyWSDeliver(t, i, frame.direction, []byte(frame.body)); err != nil {
								t.Fatal(err)
							}
						}
						parent := state.byResponseID["parent"]
						parentRequestID := parent.RequestID
						raw := []byte(fmt.Sprintf(`{"type":"response.create","stream_id":"child-lane","input":%q}`, routeTestValue))
						if kind == "warmup" {
							raw = []byte(fmt.Sprintf(`{"type":"response.create","stream_id":"child-lane","generate":false,"input":%q}`, routeTestValue))
						} else if kind == "steer" {
							raw = []byte(fmt.Sprintf(`{"type":"response.steer","previous_response_id":"parent","input":%q}`, routeTestValue))
						}
						if failure == "scan" {
							filter.Dict = nil
						}
						result, err := i.Intercept(context.Background(), subscriptionws.DirectionDownstreamToUpstream, raw)
						if err != nil {
							t.Fatal(err)
						}
						if failure == "publish" {
							if len(bus.notices) != 0 {
								t.Fatal("发布前不应有异常通知")
							}
							filter.Dict.Retire()
						}
						var sent, reply []byte
						send := func(b []byte) error {
							sent = bytes.Clone(b)
							i.Forwarded(subscriptionws.DirectionDownstreamToUpstream, int64(len(b)), time.Now())
							return nil
						}
						respond := func(b []byte, after func()) error {
							reply = bytes.Clone(b)
							if after != nil {
								after()
							}
							return nil
						}
						if result.Deliver != nil {
							err = result.Deliver(send, respond)
						} else if result.Reply != nil {
							err = respond(result.Reply, result.ReplyForwarded)
						} else if result.Forward != nil {
							err = send(result.Forward)
						}
						if err != nil {
							t.Fatal(err)
						}
						wantCode := privacy.WarningRequestFailed
						if mode == privacy.OnErrorFailOpen {
							wantCode = privacy.WarningRequestFailOpen
							if !bytes.Equal(sent, raw) || len(reply) != 0 {
								t.Fatal("未完整原文放行")
							}
						} else if len(sent) != 0 || !bytes.Contains(reply, []byte("privacy_restore_rejected")) {
							t.Fatal("未零出站拒绝")
						}
						if len(bus.notices) != 1 {
							t.Errorf("隐私处理失败通知数=%d，期望1", len(bus.notices))
						} else {
							notice := bus.notices[0].Data
							id, _ := notice["request_id"].(string)
							encoded, _ := json.Marshal(notice)
							if id == "" || id == parentRequestID || !reflect.DeepEqual(notice["warning_codes"], []string{wantCode}) || len(notice) != 3 || strings.Contains(string(encoded), "private-quote") {
								t.Fatalf("通知归属、分类或安全字段错误: %s", encoded)
							}
						}
						if parent.settled || parent.restore == nil || state.byResponseID["parent"] != parent {
							t.Fatal("辅助提交改变了父响应")
						}
						if kind != "steer" && mode == privacy.OnErrorFailOpen {
							for _, typ := range []string{"response.created", "response.completed"} {
								frame := []byte(fmt.Sprintf(`{"type":%q,"stream_id":"child-lane","response":{"id":"child","output":[]}}`, typ))
								if _, err := policyWSDeliver(t, i, subscriptionws.DirectionUpstreamToDownstream, frame); err != nil {
									t.Fatal(err)
								}
							}
						}
						if len(observer.submissions) != 1 || activity.Count() != 1 || len(i.pendingWrites) != 0 {
							t.Fatal("辅助提交残留追踪句柄、生成活动或写回调")
						}
						if _, err := policyWSDeliver(t, i, subscriptionws.DirectionUpstreamToDownstream, []byte(`{"type":"response.completed","response":{"id":"parent","output":[]}}`)); err != nil {
							t.Fatal(err)
						}
						state.close("test_finished")
						if len(bus.notices) != 1 || len(observer.submissions) != 0 || activity.Count() != 0 {
							t.Errorf("收尾重复通知或残留活动: notices=%d", len(bus.notices))
						}
						if tracked {
							if err := tracker.ForceFlush(); err != nil {
								t.Fatal(err)
							}
							wantRows := 1
							if kind == "generation" {
								wantRows++
							}
							// ForceFlush 只把刷新事件入队；等待父响应终态落库，确保前面的辅助操作也已处理。
							var rows []tracking.RequestDetail
							parentSettled := false
							for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); {
								rows, err = tracker.QueryRequestDetails(context.Background(), &tracking.QueryOptions{UpstreamType: "all"})
								parentSettled = false
								for _, row := range rows {
									parentSettled = parentSettled || row.RequestID == parentRequestID && row.Status == "completed"
								}
								if err == nil && parentSettled {
									break
								}
								time.Sleep(10 * time.Millisecond)
							}
							if err != nil || len(rows) != wantRows || !parentSettled {
								t.Fatalf("生成记录数=%d，期望%d，父响应已结算=%t: %v", len(rows), wantRows, parentSettled, err)
							}
							for _, row := range rows {
								if row.RequestID == parentRequestID && (len(row.PrivacyWarnings) != 0 || row.Status != "completed") {
									t.Fatalf("父响应状态=%s，异常=%v，失败原因=%s", row.Status, row.PrivacyWarnings, row.FailureReason)
								}
								if row.RequestID != parentRequestID && !reflect.DeepEqual(row.PrivacyWarnings, []string{wantCode}) {
									t.Fatal("正常生成请求没有持久化异常")
								}
							}
						}
					})
				}
			}
		}
	}
}

type privacyBindingObserver struct {
	*subscriptionTrackingObserver
	states map[*handlers.PrivacyRequestState]int
}

func (o *privacyBindingObserver) BindPrivacyState(state *handlers.PrivacyRequestState, submission *subscriptionSubmission) {
	o.states[state]++
	o.subscriptionTrackingObserver.BindPrivacyState(state, submission)
}

func TestWSPrivacyBindingLifecycle(t *testing.T) {
	state, activity := activitySubmissionState()
	defer state.close("test_finished")
	bus := &privacyNoticeTestBus{}
	observer := &privacyBindingObserver{newSubscriptionTrackingObserver(nil, bus), make(map[*handlers.PrivacyRequestState]int)}
	state.observer = observer
	i := &subscriptionWSInterceptor{filter: newRestoreTestFilter(t), submissions: state}
	client := func(raw string) {
		t.Helper()
		if _, err := policyWSDeliver(t, i, subscriptionws.DirectionDownstreamToUpstream, []byte(raw)); err != nil {
			t.Fatal(err)
		}
	}
	upstream := func(typ, id string) {
		t.Helper()
		if _, err := policyWSDeliver(t, i, subscriptionws.DirectionUpstreamToDownstream,
			[]byte(fmt.Sprintf(`{"type":%q,"response":{"id":%q,"output":[]}}`, typ, id))); err != nil {
			t.Fatal(err)
		}
	}
	client(`{"type":"response.create","input":"ordinary"}`)
	upstream("response.created", "parent")
	client(`{"type":"response.steer","previous_response_id":"parent","input":"ordinary"}`)
	client(`{"type":"response.create","generate":false,"input":"ordinary"}`)
	for _, id := range []string{"warmup", "successor"} {
		upstream("response.created", id)
		upstream("response.created", id) // 重复 created 不能重绑并清空摘要/通知去重状态。
		upstream("response.completed", id)
	}
	upstream("response.completed", "parent")
	if len(observer.states) != 4 || len(observer.submissions) != 0 || activity.Count() != 0 || len(bus.notices) != 0 {
		t.Fatalf("绑定/收尾/正常通知错误: states=%d handles=%d activity=%d notices=%d", len(observer.states), len(observer.submissions), activity.Count(), len(bus.notices))
	}
	ids := make(map[string]bool)
	for privacyState, count := range observer.states {
		if count != 1 || ids[privacyState.RequestID] {
			t.Fatal("同一状态重复绑定或不同提交共用了请求 ID")
		}
		ids[privacyState.RequestID] = true
		// 独立摘要重复上报同一异常，只能通知一次；不依赖仍有生成追踪句柄。
		for n := 0; n < 2; n++ {
			privacyState.RecordRestore(handlers.RestoreSummary{Status: "failed", Text: n})
		}
	}
	if len(bus.notices) != 4 {
		t.Fatalf("独立状态的通知没有去重: %d", len(bus.notices))
	}
	for _, notice := range bus.notices {
		id, _ := notice.Data["request_id"].(string)
		if !ids[id] || !reflect.DeepEqual(notice.Data["warning_codes"], []string{privacy.WarningResponseFailed}) {
			t.Fatal("独立状态通知归属错误")
		}
		delete(ids, id)
	}
}
