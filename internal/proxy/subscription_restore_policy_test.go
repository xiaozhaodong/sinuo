package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"sinuo/internal/privacy"
	"sinuo/internal/proxy/handlers"
	"sinuo/internal/proxy/subscriptionws"

	"github.com/tidwall/gjson"
)

// 测试同步执行读侧与写侧，终态只有确认交付后才结算。
func policyWSDeliver(t testing.TB, i *subscriptionWSInterceptor, direction subscriptionws.Direction, raw []byte, beforeSend ...func()) ([]byte, error) {
	t.Helper()
	r, err := i.Intercept(context.Background(), direction, raw)
	if err != nil {
		return nil, err
	}
	if r.Reply != nil {
		return r.Reply, nil
	}
	for _, prepare := range beforeSend {
		prepare()
	}
	var delivered []byte
	send := func(b []byte) error { delivered = append(delivered, b...); return nil }
	if r.Deliver != nil {
		err = r.Deliver(send, func(b []byte, after func()) error {
			delivered = append(delivered, b...)
			if after != nil {
				after()
			}
			return nil
		})
	} else if r.Forward != nil {
		err = send(r.Forward)
	}
	if err == nil {
		if r.Forwarded != nil {
			r.Forwarded()
		}
		if direction == subscriptionws.DirectionDownstreamToUpstream {
			i.Forwarded(direction, int64(len(raw)), time.Now())
		}
	}
	return delivered, err
}

func TestWSFailOpenResponseBypassesRestoreValidation(t *testing.T) {
	for _, failure := range []string{"deep", "publish", "scan-limit"} {
		t.Run(failure, func(t *testing.T) {
			state, activity := activitySubmissionState()
			defer state.close("closed")
			filter := newRestoreTestFilter(t)
			if failure == "scan-limit" {
				filter.Settings.ScanMaxBytes = 1
				filter.Settings.OnError = privacy.OnErrorFailClosed
			}
			i := &subscriptionWSInterceptor{filter: filter, submissions: state}
			deep := strings.Repeat("[", 300) + "0" + strings.Repeat("]", 300)
			raw := []byte(fmt.Sprintf(`{"type":"response.create","stream_id":"open","input":%q}`, routeTestValue))
			if failure == "deep" {
				raw = []byte(`{"type":"response.create","stream_id":"open","metadata":` + deep + `}`)
			}
			got, err := policyWSDeliver(t, i, subscriptionws.DirectionDownstreamToUpstream, raw, func() {
				if failure == "publish" {
					filter.Dict.Retire()
				}
			})
			if err != nil || !bytes.Equal(got, raw) {
				t.Fatal("未完整原文放行", err, string(got))
			}
			if state.restoring.Load() != 0 {
				t.Fatal("放行后仍有活动回填器")
			}
			for _, frame := range []string{
				`{"type":"response.created","stream_id":"open","response":{"id":"open-response"},"metadata":` + deep + `}`,
				`{"type":"response.output_text.delta","response_id":"open-response","delta":"{{SNU_0123456789abcdef0123456789abcdef}}","metadata":` + deep + `}`,
				`{"type":"response.completed","response":{"id":"open-response","usage":{"output_tokens":17}},"metadata":` + deep + `}`,
			} {
				got, err := policyWSDeliver(t, i, subscriptionws.DirectionUpstreamToDownstream, []byte(frame))
				if err != nil || string(got) != frame {
					t.Fatal("放行响应仍被拦截或改写", err, string(got))
				}
			}
			if activity.Count() != 0 || len(state.active) != 0 {
				t.Fatal("放行终态没有结算")
			}
		})
	}
}

func TestWSSuccessorRestoreFailureRespectsOnError(t *testing.T) {
	for _, mode := range []string{privacy.OnErrorFailOpen, privacy.OnErrorFailClosed} {
		for _, unavailable := range []string{"retired", "missing"} {
			t.Run(mode+"/"+unavailable, func(t *testing.T) {
				state, _ := activitySubmissionState()
				defer state.close("closed")
				filter := newRestoreTestFilter(t)
				filter.Settings.OnError = mode
				if unavailable == "retired" {
					filter.Dict.Retire()
				} else {
					filter.Dict = nil
				}
				i := &subscriptionWSInterceptor{filter: filter, submissions: state}
				frame := []byte(`{"type":"response.created","response":{"id":"successor"}}`)
				got, err := policyWSDeliver(t, i, subscriptionws.DirectionUpstreamToDownstream, frame)
				if mode == privacy.OnErrorFailClosed {
					if err == nil || gjson.GetBytes(got, "error.code").Str != "privacy_restore_failed" {
						t.Fatal("后继响应未按配置拒绝", err, string(got))
					}
					return
				}
				if err != nil || !bytes.Equal(got, frame) {
					t.Fatal("后继响应忽略出错放行", err, string(got))
				}
				sub := state.byResponseID["successor"]
				if sub == nil || sub.restore != nil || sub.successorView != nil || state.restoring.Load() != 0 {
					t.Fatal("后继放行残留回填引用")
				}
				deep := strings.Repeat("[", 300) + "0" + strings.Repeat("]", 300)
				frame = []byte(`{"type":"response.completed","response":{"id":"successor","output":[],"usage":{"output_tokens":3}},"metadata":` + deep + `}`)
				got, err = policyWSDeliver(t, i, subscriptionws.DirectionUpstreamToDownstream, frame)
				if err != nil || !bytes.Equal(got, frame) {
					t.Fatal("后继放行状态未保留到终态", err, string(got))
				}
			})
		}
	}
}

func TestWSBypassDoesNotDisableInterleavedRestore(t *testing.T) {
	state, _ := activitySubmissionState()
	defer state.close("closed")
	filter := newRestoreTestFilter(t)
	i := &subscriptionWSInterceptor{filter: filter, submissions: state}
	raw := []byte(fmt.Sprintf(`{"type":"response.create","stream_id":"protected","input":%q}`, routeTestValue))
	masked, err := policyWSDeliver(t, i, subscriptionws.DirectionDownstreamToUpstream, raw)
	if err != nil {
		t.Fatal(err)
	}
	token := string(wireToken.Find(masked))
	if token == "" {
		t.Fatal("缺少受保护的占位符")
	}
	deep := strings.Repeat("[", 300) + "0" + strings.Repeat("]", 300)
	opened := []byte(`{"type":"response.create","stream_id":"open","metadata":` + deep + `}`)
	got, err := policyWSDeliver(t, i, subscriptionws.DirectionDownstreamToUpstream, opened)
	if err != nil || !bytes.Equal(got, opened) {
		t.Fatal("未原文放行", err)
	}
	for _, lane := range []string{"open", "protected"} {
		frame := []byte(fmt.Sprintf(`{"type":"response.created","stream_id":%q,"response":{"id":%q}}`, lane, lane))
		if _, err := policyWSDeliver(t, i, subscriptionws.DirectionUpstreamToDownstream, frame); err != nil {
			t.Fatal(err)
		}
	}
	// 相同占位符在放行响应中必须原样保留，另一条响应仍正常回填。
	for _, lane := range []string{"open", "protected"} {
		frame := []byte(fmt.Sprintf(`{"type":"response.completed","response":{"id":%q,"output":[{"type":"message","id":"m","content":[{"type":"output_text","text":%q}]}]}}`, lane, token))
		got, err := policyWSDeliver(t, i, subscriptionws.DirectionUpstreamToDownstream, frame)
		if err != nil {
			t.Fatal(err)
		}
		if lane == "open" && !bytes.Equal(got, frame) {
			t.Fatal("放行响应被其他 lane 的映射改写")
		}
		if lane == "protected" && !strings.Contains(string(got), "private-quote") {
			t.Fatal("放行影响了其他响应回填")
		}
	}
	if state.restoring.Load() != 0 {
		t.Fatal("回填引用没有释放")
	}
}

// 没有客户端扫描的后继响应只记录交付状态，不制造扫描次数。
func TestWSSuccessorBypassSummary(t *testing.T) {
	state, _ := activitySubmissionState()
	defer state.close("closed")
	filter := newRestoreTestFilter(t)
	filter.Dict.Retire()
	i := &subscriptionWSInterceptor{filter: filter, submissions: state}
	var summary string
	state.observer = &policySummaryObserver{write: func(sub *subscriptionSubmission) { sub.PrivacyState.SetSummaryRecorder(func(s string) { summary = s }) }}
	_, err := policyWSDeliver(t, i, subscriptionws.DirectionUpstreamToDownstream, []byte(`{"type":"response.created","response":{"id":"s"}}`))
	if err != nil || gjson.Get(summary, "restore.status").Str != "bypassed" || gjson.Get(summary, "scan_count").Exists() {
		t.Fatal("后继放行摘要不正确", err, summary)
	}
}

type policySummaryObserver struct{ write func(*subscriptionSubmission) }

func (o *policySummaryObserver) BindPrivacyState(_ *handlers.PrivacyRequestState, s *subscriptionSubmission) {
	o.write(s)
}
func (*policySummaryObserver) SubmissionStarted(*subscriptionSubmission)                        {}
func (*policySummaryObserver) SubmissionBound(*subscriptionSubmission, *ParseResult)            {}
func (*policySummaryObserver) SubmissionTerminal(*subscriptionSubmission, *ParseResult)         {}
func (*policySummaryObserver) SubmissionRejected(*subscriptionSubmission, *privacy.PolicyError) {}
func (*policySummaryObserver) SubmissionInterrupted(*subscriptionSubmission, string)            {}
func (*policySummaryObserver) SubmissionWarning(string, string)                                 {}

func TestWSBypassKeepsProtocolAndOtherLaneValidation(t *testing.T) {
	for _, bad := range []string{
		`{"type":"response.created","stream_id":"open","response":{"id":"r"},"response":{"id":"other"}}`,
		`{"type":"response.created","Type":"response.created","stream_id":"open","response":{"id":"r"}}`,
		`{"type":"response.created","stream_id":"open","Stream_ID":"protected","response":{"id":"r"}}`,
		`{"type":"response.created","stream_id":"open","response":{"id":"r","Id":"other"}}`,
		`{"type":"response.created","stream_id":"open","response":{"id":"r"}`,
		`{"type":"response.created","stream_id":"protected","response":{"id":"protected"},"deep":` + strings.Repeat("[", 300) + "0" + strings.Repeat("]", 300) + `}`,
	} {
		t.Run(fmt.Sprint(len(bad)), func(t *testing.T) {
			state, _ := activitySubmissionState()
			defer state.close("closed")
			filter := newRestoreTestFilter(t)
			i := &subscriptionWSInterceptor{filter: filter, submissions: state}
			deep := strings.Repeat("[", 300) + "0" + strings.Repeat("]", 300)
			_, err := policyWSDeliver(t, i, subscriptionws.DirectionDownstreamToUpstream, []byte(`{"type":"response.create","stream_id":"open","metadata":`+deep+`}`))
			if err != nil {
				t.Fatal(err)
			}
			_, err = policyWSDeliver(t, i, subscriptionws.DirectionDownstreamToUpstream, []byte(`{"type":"response.create","stream_id":"protected","input":"ordinary"}`))
			if err != nil {
				t.Fatal(err)
			}
			// 非规范路由不能取得放行授权。浅层别名本身可能是原兼容逻辑接受的字段，
			// 这里直接检查它没有切换到绕过回填约束的路径。
			if state.responseBypassesRestore([]byte(bad)) {
				t.Fatal("非法或另一 lane 的消息取得放行授权")
			}
			if strings.Contains(bad, `"deep"`) || !json.Valid([]byte(bad)) || strings.Count(bad, `"response":`) > 1 {
				got, err := policyWSDeliver(t, i, subscriptionws.DirectionUpstreamToDownstream, []byte(bad))
				if err == nil || gjson.GetBytes(got, "error.code").Str != "privacy_restore_failed" {
					t.Fatal("全局放行了非法或受保护的响应", err, string(got))
				}
			}
		})
	}
}

func TestWSBypassTerminalWaitsForDeliveryAndDeduplicates(t *testing.T) {
	state, activity := activitySubmissionState()
	defer state.close("closed")
	filter := newRestoreTestFilter(t)
	i := &subscriptionWSInterceptor{filter: filter, submissions: state}
	deep := strings.Repeat("[", 300) + "0" + strings.Repeat("]", 300)
	_, err := policyWSDeliver(t, i, subscriptionws.DirectionDownstreamToUpstream, []byte(`{"type":"response.create","metadata":`+deep+`}`))
	if err != nil {
		t.Fatal(err)
	}
	_, err = policyWSDeliver(t, i, subscriptionws.DirectionUpstreamToDownstream, []byte(`{"type":"response.created","response":{"id":"queued"}}`))
	if err != nil {
		t.Fatal(err)
	}
	raw := []byte(`{"type":"response.completed","response":{"id":"queued","usage":{"output_tokens":3}},"metadata":` + deep + `}`)
	terminal, err := i.Intercept(context.Background(), subscriptionws.DirectionUpstreamToDownstream, raw)
	if err != nil || terminal.Forwarded == nil || activity.Count() != 1 || len(state.active) != 1 {
		t.Fatal("终态读到就提前结算", err)
	}
	assertDuplicate := func() {
		t.Helper()
		r, err := i.Intercept(context.Background(), subscriptionws.DirectionUpstreamToDownstream, raw)
		if err != nil || r.Forward != nil || r.Deliver != nil || r.Reply != nil {
			t.Fatal("放行响应的重复终态被回填门禁拦截或重复交付", err)
		}
	}
	assertDuplicate()
	terminal.Forwarded()
	if activity.Count() != 0 || len(state.active) != 0 {
		t.Fatal("终态交付后没有结算")
	}
	assertDuplicate()
	state.close("closed")
	if state.bypassed.Load() != 0 {
		t.Fatal("关闭连接后放行状态未释放")
	}
}

// 后继首帧本身超出回填深度时，也必须遵守本地失败策略。
func TestWSDeepSuccessorFirstFramePolicy(t *testing.T) {
	for _, mode := range []string{privacy.OnErrorFailOpen, privacy.OnErrorFailClosed} {
		for _, dict := range []string{"ready", "missing", "retired"} {
			t.Run(mode+"/"+dict, func(t *testing.T) {
				state, activity := activitySubmissionState()
				defer state.close("closed")
				filter := newRestoreTestFilter(t)
				filter.Settings.OnError = mode
				if dict == "missing" {
					filter.Dict = nil
				} else if dict == "retired" {
					filter.Dict.Retire()
				}
				i := &subscriptionWSInterceptor{filter: filter, submissions: state}
				deep := strings.Repeat("[", 300) + "0" + strings.Repeat("]", 300)
				raw := []byte(`{"type":"response.created","response":{"id":"successor"},"metadata":` + deep + `}`)
				got, err := policyWSDeliver(t, i, subscriptionws.DirectionUpstreamToDownstream, raw)
				if mode == privacy.OnErrorFailClosed {
					if err == nil || gjson.GetBytes(got, "error.code").Str != "privacy_restore_failed" {
						t.Fatal("后继首帧未按出错拒绝处理", err, string(got))
					}
					return
				}
				if err != nil || !bytes.Equal(got, raw) {
					t.Fatal("深层后继首帧未原文放行", err, string(got))
				}
				sub := state.byResponseID["successor"]
				if sub == nil || !sub.restoreBypassed || sub.restore != nil || sub.successorView != nil || state.restoring.Load() != 0 {
					t.Fatal("后继放行后残留回填器")
				}
				for _, frame := range []string{
					`{"type":"response.output_text.delta","response_id":"successor","delta":"{{SNU_0123456789abcdef0123456789abcdef}}","metadata":` + deep + `}`,
					`{"type":"response.completed","response":{"id":"successor"},"metadata":` + deep + `}`,
				} {
					got, err = policyWSDeliver(t, i, subscriptionws.DirectionUpstreamToDownstream, []byte(frame))
					if err != nil || string(got) != frame {
						t.Fatal("后继放行状态没有保留", err, string(got))
					}
				}
				if activity.Count() != 0 {
					t.Fatal("后继终态交付后未结算")
				}
			})
		}
	}
}

// 创建失败错误只考虑 pending；归属预判、观测和真正交付必须采用同一规则。
func TestWSBypassPendingErrorsRemainOriginal(t *testing.T) {
	for _, code := range []string{"invalid_stream_id", "websocket_stream_limit_reached", "previous_response_not_found", "previous_response_id_not_found"} {
		for _, flat := range []bool{false, true} {
			for _, depth := range []int{0, 300} {
				t.Run(fmt.Sprintf("%s/flat=%t/depth=%d", code, flat, depth), func(t *testing.T) {
					state, activity := activitySubmissionState()
					defer state.close("closed")
					filter := newRestoreTestFilter(t)
					i := &subscriptionWSInterceptor{filter: filter, submissions: state}
					masked, err := policyWSDeliver(t, i, subscriptionws.DirectionDownstreamToUpstream, []byte(fmt.Sprintf(`{"type":"response.create","input":%q}`, routeTestValue)))
					if err != nil {
						t.Fatal(err)
					}
					token := string(wireToken.Find(masked))
					if token == "" {
						t.Fatal("受保护响应没有映射")
					}
					if _, err = policyWSDeliver(t, i, subscriptionws.DirectionUpstreamToDownstream, []byte(`{"type":"response.created","response":{"id":"protected"}}`)); err != nil {
						t.Fatal(err)
					}
					deep := strings.Repeat("[", 300) + "0" + strings.Repeat("]", 300)
					opened := []byte(`{"type":"response.create","metadata":` + deep + `}`)
					if got, err := policyWSDeliver(t, i, subscriptionws.DirectionDownstreamToUpstream, opened); err != nil || !bytes.Equal(got, opened) {
						t.Fatal("请求未完整原文放行", err)
					}
					metadata := "0"
					if depth > 0 {
						metadata = deep
					}
					var raw []byte
					if flat {
						raw = []byte(fmt.Sprintf(`{"type":" error ","code":%q,"message":%q,"echo":"keep","metadata":%s}`, code, token, metadata))
					} else {
						raw = []byte(fmt.Sprintf(`{"type":"error","error":{"code":%q,"message":%q},"echo":"keep","metadata":%s}`, code, token, metadata))
					}
					got, err := policyWSDeliver(t, i, subscriptionws.DirectionUpstreamToDownstream, raw)
					if err != nil || !bytes.Equal(got, raw) {
						t.Fatal("放行错误帧仍被改写或拒绝", err, string(got))
					}
					if activity.Count() != 1 || len(state.pending) != 0 || state.restoring.Load() != 1 {
						t.Fatal("没有仅结算放行的 pending 请求")
					}
					terminal := []byte(fmt.Sprintf(`{"type":"response.completed","response":{"id":"protected","output":[{"type":"message","id":"m","content":[{"type":"output_text","text":%q}]}]}}`, token))
					got, err = policyWSDeliver(t, i, subscriptionws.DirectionUpstreamToDownstream, terminal)
					if err != nil || !bytes.Contains(got, []byte("private-quote")) || activity.Count() != 0 {
						t.Fatal("放行错误影响了另一条响应回填", err, string(got))
					}
				})
			}
		}
	}
}

func TestWSBypassKeepsDeliveryStateAfterSettingsChange(t *testing.T) {
	for _, source := range []string{"request", "successor"} {
		for _, change := range []string{"restore-off", "mode-off"} {
			t.Run(source+"/"+change, func(t *testing.T) {
				state, activity := activitySubmissionState()
				defer state.close("closed")
				filter := newRestoreTestFilter(t)
				i := &subscriptionWSInterceptor{filter: filter, submissions: state}
				deep := strings.Repeat("[", 300) + "0" + strings.Repeat("]", 300)
				if source == "request" {
					if _, err := policyWSDeliver(t, i, subscriptionws.DirectionDownstreamToUpstream, []byte(`{"type":"response.create","metadata":`+deep+`}`)); err != nil {
						t.Fatal(err)
					}
				} else {
					filter.Dict.Retire()
				}
				if _, err := policyWSDeliver(t, i, subscriptionws.DirectionUpstreamToDownstream, []byte(`{"type":"response.created","response":{"id":"open"}}`)); err != nil {
					t.Fatal(err)
				}
				if change == "restore-off" {
					filter.Settings.ResponseRestoreEnabled = false
				} else {
					filter.Settings.Mode = privacy.ModeDisabled
				}
				for _, frame := range []string{
					`{"type":"response.output_text.delta","response_id":"open","delta":"{{SNU_0123456789abcdef0123456789abcdef}}","metadata":` + deep + `}`,
					`{"type":"response.steer.accepted","response_id":"open","sequence_number":7,"metadata":` + deep + `}`,
				} {
					if got, err := policyWSDeliver(t, i, subscriptionws.DirectionUpstreamToDownstream, []byte(frame)); err != nil || string(got) != frame {
						t.Fatal("设置变化影响了原文放行", err, string(got))
					}
				}
				raw := []byte(`{"type":"response.completed","response":{"id":"open"},"metadata":` + deep + `}`)
				terminal, err := i.Intercept(context.Background(), subscriptionws.DirectionUpstreamToDownstream, raw)
				if err != nil || terminal.Forwarded == nil || !bytes.Equal(terminal.Forward, raw) || activity.Count() != 1 || len(state.active) != 1 {
					t.Fatal("终态在交付前被结算或改写", err)
				}
				checkDuplicate := func() {
					dup, err := i.Intercept(context.Background(), subscriptionws.DirectionUpstreamToDownstream, raw)
					if err != nil || dup.Forward != nil || dup.Deliver != nil || dup.Reply != nil {
						t.Fatal("重复终态再次交付", err)
					}
				}
				checkDuplicate()
				terminal.Forwarded()
				if activity.Count() != 0 || len(state.active) != 0 {
					t.Fatal("终态交付后未结算")
				}
				checkDuplicate()
				filter.Settings.ResponseRestoreEnabled = true
				filter.Settings.Mode = privacy.ModeRedact
				checkDuplicate()
			})
		}
	}
}

func TestWSDeepSuccessorKeepsProtocolAndProtectedTargets(t *testing.T) {
	deep := strings.Repeat("[", 300) + "0" + strings.Repeat("]", 300)
	for _, target := range []string{"new", "pending", "active", "completed"} {
		for _, malformed := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/malformed=%t", target, malformed), func(t *testing.T) {
				state, _ := activitySubmissionState()
				defer state.close("closed")
				i := &subscriptionWSInterceptor{filter: newRestoreTestFilter(t), submissions: state}
				if target != "new" {
					if _, err := policyWSDeliver(t, i, subscriptionws.DirectionDownstreamToUpstream, []byte(`{"type":"response.create","input":"ordinary"}`)); err != nil {
						t.Fatal(err)
					}
				}
				if target == "active" || target == "completed" {
					if _, err := policyWSDeliver(t, i, subscriptionws.DirectionUpstreamToDownstream, []byte(`{"type":"response.created","response":{"id":"protected"}}`)); err != nil {
						t.Fatal(err)
					}
				}
				if target == "completed" {
					if _, err := policyWSDeliver(t, i, subscriptionws.DirectionUpstreamToDownstream, []byte(`{"type":"response.completed","response":{"id":"protected"}}`)); err != nil {
						t.Fatal(err)
					}
				}
				raw := []byte(`{"type":"response.created","response":{"id":"protected"},"metadata":` + deep + `}`)
				if malformed {
					raw = append(raw[:len(raw)-1], []byte(`,"response":{"id":"other"}}`)...)
				}
				got, err := policyWSDeliver(t, i, subscriptionws.DirectionUpstreamToDownstream, raw)
				if target == "new" && !malformed {
					if err != nil || !bytes.Equal(got, raw) {
						t.Fatal("合法的新后继没有放行", err)
					}
				} else if err == nil || gjson.GetBytes(got, "error.code").Str != "privacy_restore_failed" {
					t.Fatal("非法首帧或受保护归属取得放行", err, string(got))
				}
			})
		}
	}
}

func TestWSBypassPendingErrorsKeepAmbiguousAndConflictingRoutesProtected(t *testing.T) {
	for _, kind := range []string{"ambiguous", "ordinary-error", "conflicting-ids", "conflicting-lane"} {
		t.Run(kind, func(t *testing.T) {
			state, _ := activitySubmissionState()
			defer state.close("closed")
			i := &subscriptionWSInterceptor{filter: newRestoreTestFilter(t), submissions: state}
			deep := strings.Repeat("[", 300) + "0" + strings.Repeat("]", 300)
			for _, lane := range []string{"protected", "open"} {
				raw := fmt.Sprintf(`{"type":"response.create","stream_id":%q,"input":"ordinary"}`, lane)
				if lane == "open" {
					raw = fmt.Sprintf(`{"type":"response.create","stream_id":%q,"metadata":%s}`, lane, deep)
				}
				if _, err := policyWSDeliver(t, i, subscriptionws.DirectionDownstreamToUpstream, []byte(raw)); err != nil {
					t.Fatal(err)
				}
				if lane == "protected" && kind != "ambiguous" || lane == "open" && strings.HasPrefix(kind, "conflicting-") {
					if _, err := policyWSDeliver(t, i, subscriptionws.DirectionUpstreamToDownstream, []byte(fmt.Sprintf(`{"type":"response.created","stream_id":%q,"response":{"id":%q}}`, lane, lane))); err != nil {
						t.Fatal(err)
					}
				}
			}
			raw := `{"type":"error","error":{"code":"previous_response_not_found","message":"sensitive-echo"}}`
			switch kind {
			case "ordinary-error":
				raw = `{"type":"error","error":{"code":"upstream_error","message":"sensitive-echo"}}`
			case "conflicting-ids":
				raw = `{"type":"error","response":{"id":"open"},"response_id":"protected","error":{"message":"sensitive-echo"}}`
			case "conflicting-lane":
				raw = `{"type":"error","response_id":"open","stream_id":"protected","error":{"message":"sensitive-echo"}}`
			}
			if state.responseBypassesRestore([]byte(raw)) {
				t.Fatal("歧义或冲突路由取得放行授权")
			}
			got, err := policyWSDeliver(t, i, subscriptionws.DirectionUpstreamToDownstream, []byte(raw))
			if err != nil || bytes.Contains(got, []byte("sensitive-echo")) || len(state.active) != 2 {
				t.Fatal("未归属的错误影响了在途响应或暴露回显", err, string(got))
			}
		})
	}
}

func TestWSBypassConnectionExpiryKeepsOriginalAndCloses(t *testing.T) {
	state, _ := activitySubmissionState()
	defer state.close("closed")
	i := &subscriptionWSInterceptor{filter: newRestoreTestFilter(t), submissions: state}
	deep := strings.Repeat("[", 300) + "0" + strings.Repeat("]", 300)
	if _, err := policyWSDeliver(t, i, subscriptionws.DirectionDownstreamToUpstream, []byte(`{"type":"response.create","metadata":`+deep+`}`)); err != nil {
		t.Fatal(err)
	}
	raw := []byte(`{"type":"error","error":{"code":"websocket_connection_limit_reached","message":"keep original"},"metadata":` + deep + `}`)
	got, err := policyWSDeliver(t, i, subscriptionws.DirectionUpstreamToDownstream, raw)
	if err == nil || !bytes.Equal(got, raw) {
		t.Fatal("连接到期未保留原文或未关闭", err, string(got))
	}
}
