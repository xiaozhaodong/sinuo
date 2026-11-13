package proxy

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"testing"

	"sinuo/internal/privacy"
	"sinuo/internal/proxy/restorestream"
	"sinuo/internal/proxy/subscriptionws"

	"github.com/tidwall/gjson"
)

// 同一批歧义数据帧分别验证普通透传与回填保护边界。
var subscriptionNonCanonicalFrames = []struct{ name, raw string }{
	{"case-alias", `{"Type":"response.output_text.delta","response_id":"r","delta":"synthetic"}`},
	{"duplicate-key", `{"type":"response.output_text.delta","response_id":"r","delta":"one","delta":"two"}`},
	{"non-object-response", `{"type":"response.output_text.delta","response_id":"r","response":[],"delta":"synthetic"}`},
	{"non-json", `synthetic non-JSON text`},
	{"conflicting-terminal", `{"type":"response.completed","response_id":"r","response":{"id":"other"}}`},
	{"aliased-control", `{"type":"response.steer.pending","Response_ID":"r","sequence_number":7}`},
}

func TestSubscriptionWSUnprotectedFramesRemainOriginal(t *testing.T) {
	cases := append([]struct{ name, raw string }{}, subscriptionNonCanonicalFrames...)
	cases = append(cases, struct{ name, raw string }{
		"ambiguous-error", `{"type":"error","response_id":"r","response":{"id":"other"},"error":{"message":"original diagnostic"}}`,
	})
	for _, mode := range []string{privacy.ModeDisabled, privacy.ModeDetect, privacy.ModeRedact} {
		for _, tc := range cases {
			t.Run(mode+"/"+tc.name, func(t *testing.T) {
				state, activity := activitySubmissionState()
				defer state.close("closed")
				filter := newRestoreTestFilter(t)
				filter.Settings.Mode, filter.Settings.ResponseRestoreEnabled = mode, false
				i := &subscriptionWSInterceptor{filter: filter, submissions: state}
				if _, err := policyWSDeliver(t, i, subscriptionws.DirectionDownstreamToUpstream, []byte(`{"type":"response.create","input":"ordinary"}`)); err != nil {
					t.Fatal(err)
				}
				if _, err := policyWSDeliver(t, i, subscriptionws.DirectionUpstreamToDownstream, []byte(`{"type":"response.created","response":{"id":"r"}}`)); err != nil {
					t.Fatal(err)
				}
				sub := state.byResponseID["r"]
				if sub == nil || state.restoring.Load() != 0 || state.bypassed.Load() != 0 {
					t.Fatal("测试意外进入严格保护状态")
				}
				raw := []byte(tc.raw)
				got, err := policyWSDeliver(t, i, subscriptionws.DirectionUpstreamToDownstream, raw)
				if err != nil || !bytes.Equal(got, raw) {
					t.Fatalf("无回填帧未原样透传: %v %s", err, got)
				}
				if sub.settled || sub.terminalRead != nil || activity.Count() != 1 {
					t.Fatal("歧义帧改变了请求生命周期")
				}
				// 异常帧不能破坏后续规范终态的交付和结算。
				terminal := []byte(`{"type":"response.completed","response":{"id":"r","usage":{"output_tokens":3}}}`)
				got, err = policyWSDeliver(t, i, subscriptionws.DirectionUpstreamToDownstream, terminal)
				if err != nil || !bytes.Equal(got, terminal) || !sub.settled || activity.Count() != 0 {
					t.Fatalf("后续规范终态无法正常交付和结算: %v", err)
				}
			})
		}
	}
}

func TestSubscriptionWSStrictFramesRemainRejected(t *testing.T) {
	for _, scenario := range []string{"enabled", "restoring-after-disable", "bypassed-after-disable", "completed-bypass-after-disable"} {
		for _, tc := range subscriptionNonCanonicalFrames {
			t.Run(scenario+"/"+tc.name, func(t *testing.T) {
				state, _ := activitySubmissionState()
				defer state.close("closed")
				filter := newRestoreTestFilter(t)
				i := &subscriptionWSInterceptor{filter: filter, submissions: state}
				create := []byte(fmt.Sprintf(`{"type":"response.create","input":%q}`, routeTestValue))
				bypass := strings.Contains(scenario, "bypass")
				if bypass {
					deep := strings.Repeat("[", 300) + "0" + strings.Repeat("]", 300)
					create = []byte(`{"type":"response.create","metadata":` + deep + `}`)
				}
				if _, err := policyWSDeliver(t, i, subscriptionws.DirectionDownstreamToUpstream, create); err != nil {
					t.Fatal(err)
				}
				if _, err := policyWSDeliver(t, i, subscriptionws.DirectionUpstreamToDownstream, []byte(`{"type":"response.created","response":{"id":"r"}}`)); err != nil {
					t.Fatal(err)
				}
				if scenario == "completed-bypass-after-disable" {
					if _, err := policyWSDeliver(t, i, subscriptionws.DirectionUpstreamToDownstream, []byte(`{"type":"response.completed","response":{"id":"r"}}`)); err != nil {
						t.Fatal(err)
					}
				}
				if scenario != "enabled" {
					filter.Settings.Mode, filter.Settings.ResponseRestoreEnabled = privacy.ModeDisabled, false
				}
				if bypass {
					if state.restoring.Load() != 0 || state.bypassed.Load() == 0 {
						t.Fatal("未保留放行归属记录")
					}
				} else if state.restoring.Load() != 1 {
					t.Fatal("未保留受保护响应")
				}
				got, err := policyWSDeliver(t, i, subscriptionws.DirectionUpstreamToDownstream, []byte(tc.raw))
				if !errors.Is(err, restorestream.ErrDelivery) || gjson.GetBytes(got, "status").Int() != 502 || gjson.GetBytes(got, "error.code").Str != "privacy_restore_failed" {
					t.Fatalf("严格状态放行了歧义帧: %v %s", err, got)
				}
			})
		}
	}
}

func TestSubscriptionWSEnabledWithoutSubmissionStillRejectsInvalidFrames(t *testing.T) {
	for _, tc := range subscriptionNonCanonicalFrames {
		t.Run(tc.name, func(t *testing.T) {
			state, _ := activitySubmissionState()
			defer state.close("closed")
			i := &subscriptionWSInterceptor{filter: newRestoreTestFilter(t), submissions: state}
			got, err := policyWSDeliver(t, i, subscriptionws.DirectionUpstreamToDownstream, []byte(tc.raw))
			if !errors.Is(err, restorestream.ErrDelivery) || gjson.GetBytes(got, "error.code").Str != "privacy_restore_failed" {
				t.Fatalf("回填已启用时放行了无归属的歧义帧: %v %s", err, got)
			}
			if len(state.active) != 0 {
				t.Fatal("歧义帧创建了后继响应")
			}
		})
	}
}
