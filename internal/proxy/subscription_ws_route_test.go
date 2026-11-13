package proxy

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"

	"sinuo/internal/privacy"
	"sinuo/internal/proxy/subscriptionws"

	"github.com/tidwall/gjson"
)

func routePolicyPair(t *testing.T) (*subscriptionWSInterceptor, string) {
	t.Helper()
	state, _ := activitySubmissionState()
	t.Cleanup(func() { state.close("closed") })
	i := &subscriptionWSInterceptor{filter: newRestoreTestFilter(t), submissions: state}
	masked, err := policyWSDeliver(t, i, subscriptionws.DirectionDownstreamToUpstream, []byte(fmt.Sprintf(`{"type":"response.create","stream_id":"protected","input":%q}`, routeTestValue)))
	if err != nil {
		t.Fatal(err)
	}
	token := string(wireToken.Find(masked))
	if token == "" {
		t.Fatal("受保护请求缺少映射")
	}
	deep := strings.Repeat("[", 300) + "0" + strings.Repeat("]", 300)
	raw := []byte(`{"type":"response.create","stream_id":"open","metadata":` + deep + `}`)
	if got, err := policyWSDeliver(t, i, subscriptionws.DirectionDownstreamToUpstream, raw); err != nil || !bytes.Equal(got, raw) {
		t.Fatal("未完整原文放行", err)
	}
	for _, lane := range []string{"protected", "open"} {
		if _, err := policyWSDeliver(t, i, subscriptionws.DirectionUpstreamToDownstream, []byte(fmt.Sprintf(`{"type":"response.created","stream_id":%q,"response":{"id":%q}}`, lane, lane))); err != nil {
			t.Fatal(err)
		}
	}
	return i, token
}

func TestWSBypassControlRoutesRemainOriginal(t *testing.T) {
	for _, routing := range []string{
		`"steer":{"previous_response_id":"open"}`,
		`"response":{"id":"open"}`,
		`"response_id":"open"`,
		`"response":{"id":"open"},"response_id":"open","steer":{"previous_response_id":"open"}`,
		`"stream_id":"open"`,
	} {
		for _, completed := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/completed=%t", routing, completed), func(t *testing.T) {
				i, _ := routePolicyPair(t)
				if completed {
					if _, err := policyWSDeliver(t, i, subscriptionws.DirectionUpstreamToDownstream, []byte(`{"type":"response.completed","response":{"id":"open"}}`)); err != nil {
						t.Fatal(err)
					}
				}
				deep := strings.Repeat("[", 300) + "0" + strings.Repeat("]", 300)
				raw := []byte(fmt.Sprintf(`{"type":"response.steer.pending",%s,"sequence_number":7,"metadata":%s}`, routing, deep))
				got, err := policyWSDeliver(t, i, subscriptionws.DirectionUpstreamToDownstream, raw)
				if err != nil || !bytes.Equal(got, raw) {
					t.Fatal("放行回执仍被改写或拒绝", err, string(got))
				}
				if i.submissions.restoring.Load() != 1 {
					t.Fatal("放行回执影响了保护响应")
				}
			})
		}
	}
}

func TestWSConflictingRoutesNeverAuthorizeBypass(t *testing.T) {
	for _, typ := range []string{"response.created", "response.output_text.delta", " response.output_text.delta ", "response.completed", "response.steer.pending"} {
		for _, routing := range []string{
			`"response":{"id":"open"},"response_id":"protected"`,
			`"response":{"id":"protected"},"response_id":"open"`,
		} {
			t.Run(typ+"/"+routing, func(t *testing.T) {
				i, token := routePolicyPair(t)
				raw := []byte(fmt.Sprintf(`{"type":%q,%s,"sequence_number":7,"delta":%q}`, typ, routing, token))
				if i.submissions.responseBypassesRestore(raw) {
					t.Fatal("冲突 ID 取得放行授权")
				}
				got, err := policyWSDeliver(t, i, subscriptionws.DirectionUpstreamToDownstream, raw)
				if err == nil || gjson.GetBytes(got, "error.code").Str != "privacy_restore_failed" || len(i.submissions.active) != 2 {
					t.Fatal("冲突 ID 未拒绝或错误结算", err, string(got))
				}
			})
		}
	}
}

func TestWSRoutingConflictSurvivesObservationFallback(t *testing.T) {
	for _, raw := range []string{
		`{"type":"response.output_text.delta","response_id":"protected","response":{"id":"open"},"extra":1e400}`,
		`{"Type":"response.output_text.delta","response_id":"protected","response":{"id":"open"}}`,
	} {
		t.Run(raw, func(t *testing.T) {
			i, _ := routePolicyPair(t)
			got, err := policyWSDeliver(t, i, subscriptionws.DirectionUpstreamToDownstream, []byte(raw))
			if err == nil || gjson.GetBytes(got, "error.code").Str != "privacy_restore_failed" || len(i.submissions.active) != 2 {
				t.Fatal("观测兼容路径跳过归属校验", err, string(got))
			}
		})
	}
}

func TestWSConflictingControlRoutesNeverRewriteSequence(t *testing.T) {
	for _, routing := range []string{
		`"response_id":"protected","steer":{"previous_response_id":"open"}`,
		`"response":{"id":"open"},"steer":{"previous_response_id":"protected"}`,
		`"steer":{"previous_response_id":"open","Previous_Response_ID":"protected"}`,
		`"steer":{"previous_response_id":42}`,
		`"Steer":{"previous_response_id":"open"}`,
	} {
		t.Run(routing, func(t *testing.T) {
			i, _ := routePolicyPair(t)
			raw := []byte(fmt.Sprintf(`{"type":"response.steer.accepted",%s,"sequence_number":7}`, routing))
			if i.submissions.responseBypassesRestore(raw) {
				t.Fatal("冲突或别名控制路由取得授权")
			}
			got, err := policyWSDeliver(t, i, subscriptionws.DirectionUpstreamToDownstream, raw)
			if err == nil || gjson.GetBytes(got, "error.code").Str != "privacy_restore_failed" {
				t.Fatal("控制路由未拒绝", err, string(got))
			}
		})
	}
}

func TestWSLargeSuccessorFirstFramePolicy(t *testing.T) {
	for _, mode := range []string{privacy.OnErrorFailOpen, privacy.OnErrorFailClosed} {
		for _, extra := range []int{-1, 0, 1} {
			t.Run(fmt.Sprintf("%s/limit%+d", mode, extra), func(t *testing.T) {
				state, activity := activitySubmissionState()
				defer state.close("closed")
				filter := newRestoreTestFilter(t)
				filter.Settings.OnError = mode
				// 出站扫描预算不控制上游响应的本地回填能力错误。
				filter.Settings.OverLimitAction = privacy.OverLimitFailClosed
				i := &subscriptionWSInterceptor{filter: filter, submissions: state}
				var summary string
				state.observer = &policySummaryObserver{write: func(s *subscriptionSubmission) { s.PrivacyState.SetSummaryRecorder(func(v string) { summary = v }) }}
				prefix := `{"type":"response.created","response":{"id":"successor"},"metadata":"`
				raw := []byte(prefix + strings.Repeat("x", (4<<20)+extra-len(prefix)-2) + `"}`)
				got, err := policyWSDeliver(t, i, subscriptionws.DirectionUpstreamToDownstream, raw)
				if extra > 0 && mode == privacy.OnErrorFailClosed {
					if err == nil || gjson.GetBytes(got, "error.code").Str != "privacy_restore_failed" {
						t.Fatal("超限首帧未遵守出错拒绝", err)
					}
					return
				}
				if err != nil || !bytes.Equal(got, raw) {
					t.Fatal("首帧被误拒绝或改写", err, len(got))
				}
				sub := state.byResponseID["successor"]
				if extra > 0 && (sub == nil || !sub.restoreBypassed || sub.restore != nil || sub.successorView != nil || state.restoring.Load() != 0) {
					t.Fatal("超限原文放行后残留回填器")
				}
				if extra > 0 && (gjson.Get(summary, "restore.status").Str != "bypassed" || gjson.Get(summary, "scan_count").Exists()) {
					t.Fatal("放行提示缺失或制造扫描次数", summary)
				}
				frame := []byte(`{"type":"response.completed","response":{"id":"successor"}}`)
				if got, err = policyWSDeliver(t, i, subscriptionws.DirectionUpstreamToDownstream, frame); err != nil || !bytes.Equal(got, frame) || activity.Count() != 0 {
					t.Fatal("后继终态交付错误", err)
				}
			})
		}
	}
}

// 已交付首帧后不能通过回退原文重发整个响应。
func TestWSProtectedResponseDoesNotFallbackAfterFirstFrame(t *testing.T) {
	i, _ := routePolicyPair(t)
	raw := []byte(`{"type":"response.output_text.delta","response_id":"protected","delta":"` + strings.Repeat("x", 4<<20) + `"}`)
	got, err := policyWSDeliver(t, i, subscriptionws.DirectionUpstreamToDownstream, raw)
	if err == nil || gjson.GetBytes(got, "error.code").Str != "privacy_restore_failed" || i.submissions.byResponseID["protected"].restoreBypassed {
		t.Fatal("已交付的保护响应不应回退原文", err)
	}
}

func TestWSBypassControlReceiptBeforeTerminalDelivery(t *testing.T) {
	i, _ := routePolicyPair(t)
	terminal, err := i.Intercept(context.Background(), subscriptionws.DirectionUpstreamToDownstream, []byte(`{"type":"response.completed","response":{"id":"open"}}`))
	if err != nil || terminal.Forwarded == nil {
		t.Fatal(err)
	}
	deep := strings.Repeat("[", 300) + "0" + strings.Repeat("]", 300)
	raw := []byte(`{"type":"response.steer.pending","steer":{"previous_response_id":"open"},"sequence_number":7,"metadata":` + deep + `}`)
	if got, err := policyWSDeliver(t, i, subscriptionws.DirectionUpstreamToDownstream, raw); err != nil || !bytes.Equal(got, raw) {
		t.Fatal("终态排队期间的回执未原文放行", err)
	}
	terminal.Forwarded()
}
