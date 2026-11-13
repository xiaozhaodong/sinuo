package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/tidwall/gjson"

	"sinuo/internal/privacy"
	"sinuo/internal/proxy/subscriptionws"
)

func boundaryRead(t *testing.T, i *subscriptionWSInterceptor, raw []byte) subscriptionws.InterceptResult {
	t.Helper()
	r, err := i.Intercept(context.Background(), subscriptionws.DirectionUpstreamToDownstream, raw)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func boundaryWrite(r subscriptionws.InterceptResult, send func([]byte) error) error {
	var err error
	if r.Deliver != nil {
		err = r.Deliver(send, nil)
	} else if r.Forward != nil {
		err = send(r.Forward)
	}
	if err == nil && r.Forwarded != nil {
		r.Forwarded()
	}
	return err
}

func boundaryCreate(t *testing.T, i *subscriptionWSInterceptor, lane, id string, open bool) string {
	t.Helper()
	input := fmt.Sprintf(`"input":%q`, routeTestValue)
	if open {
		input = `"metadata":` + strings.Repeat("[", 300) + "0" + strings.Repeat("]", 300)
	}
	raw := []byte(fmt.Sprintf(`{"type":"response.create","stream_id":%q,%s}`, lane, input))
	masked, err := policyWSDeliver(t, i, subscriptionws.DirectionDownstreamToUpstream, raw)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = policyWSDeliver(t, i, subscriptionws.DirectionUpstreamToDownstream,
		[]byte(fmt.Sprintf(`{"type":"response.created","stream_id":%q,"response":{"id":%q}}`, lane, id))); err != nil {
		t.Fatal(err)
	}
	return string(wireToken.Find(masked))
}

// 同一个 Unicode 字面量在旧快速解码中会成为另一条响应的 ID。
// 交叉覆盖保护/放行、顶层/嵌套/命名流、普通/观测溢出路径，并在排队前后核对结算。
func TestWSBoundaryCanonicalOwnership(t *testing.T) {
	for _, protected := range []bool{false, true} {
		for _, routing := range []string{"response_id", "response.id", "stream_id"} {
			for _, overflow := range []bool{false, true} {
				t.Run(fmt.Sprintf("protected=%t/%s/overflow=%t", protected, routing, overflow), func(t *testing.T) {
					state, activity := activitySubmissionState()
					defer state.close("closed")
					i := &subscriptionWSInterceptor{filter: newRestoreTestFilter(t), submissions: state}
					token := boundaryCreate(t, i, "�A", "�A", !protected)
					other := boundaryCreate(t, i, "�", "�", protected)
					if token == "" {
						token = other
					}
					output := fmt.Sprintf(`"output":[{"type":"message","id":"m","content":[{"type":"output_text","text":%q}]}]`, token)
					fields := `"response_id":"\ud800\u0041","response":{` + output + `}`
					if routing == "response.id" {
						fields = `"response":{"id":"\ud800\u0041",` + output + `}`
					} else if routing == "stream_id" {
						fields = `"stream_id":"\ud800\u0041","response":{` + output + `}`
					}
					if overflow {
						fields += `,"extra":1e400`
					}
					raw := []byte(`{"type":"response.completed",` + fields + `}`)
					r := boundaryRead(t, i, raw)
					if activity.Count() != 2 || state.byResponseID["�A"].terminalRead == nil || state.byResponseID["�"].terminalRead != nil {
						t.Fatal("终态归属或排队状态错误")
					}
					var got []byte
					if err := boundaryWrite(r, func(b []byte) error { got = bytes.Clone(b); return nil }); err != nil {
						t.Fatal(err)
					}
					if protected && (bytes.Contains(got, []byte(token)) || gjson.GetBytes(got, "response.output.0.content.0.text").Str != routeTestValue) {
						t.Fatal("受保护响应没有使用自己的回填器")
					}
					if !protected && !bytes.Equal(got, raw) {
						t.Fatal("放行响应发生改写")
					}
					if state.byResponseID["�A"] != nil || state.byResponseID["�"] == nil || activity.Count() != 1 {
						t.Fatal("结算了另一条响应")
					}
				})
			}
		}
	}
}

func TestWSBoundaryUnicodeErrorsAndReceipts(t *testing.T) {
	for _, protected := range []bool{false, true} {
		for _, event := range []string{"error", "response.steer.pending"} {
			t.Run(fmt.Sprintf("protected=%t/%s", protected, event), func(t *testing.T) {
				state, _ := activitySubmissionState()
				defer state.close("closed")
				i := &subscriptionWSInterceptor{filter: newRestoreTestFilter(t), submissions: state}
				boundaryCreate(t, i, "target", "�A", !protected)
				boundaryCreate(t, i, "other", "�", protected)
				raw := []byte(`{"type":"error","response_id":"\ud800\u0041","error":{"code":"upstream_error","message":"original echo"}}`)
				if event != "error" {
					raw = []byte(`{"type":"response.steer.pending","steer":{"previous_response_id":"\ud800\u0041"},"sequence_number":7}`)
				}
				got, err := policyWSDeliver(t, i, subscriptionws.DirectionUpstreamToDownstream, raw)
				if err != nil {
					t.Fatal(err)
				}
				if !protected && !bytes.Equal(got, raw) || protected && event == "error" && bytes.Contains(got, []byte("original echo")) {
					t.Fatal("错误清洗没有服从目标响应的策略")
				}
				if state.byResponseID["�"] == nil || (state.byResponseID["�A"] == nil) != (event == "error") {
					t.Fatal("错误或控制回执结算了错误的响应")
				}
			})
		}
	}
}

func boundaryOpening(kind, token string) []byte {
	items, metadata := "", ""
	switch kind {
	case "items", "calls":
		var values []string
		for n := 0; n < 257; n++ {
			item := fmt.Sprintf(`{"type":"message","id":"m%d","content":[]}`, n)
			if n == 0 {
				item = fmt.Sprintf(`{"type":"message","id":"m0","content":[{"type":"output_text","text":%q}]}`, token)
			}
			if kind == "calls" {
				item = fmt.Sprintf(`{"type":"function_call","call_id":"c%d","arguments":""}`, n)
			}
			values = append(values, item)
		}
		items = strings.Join(values, ",")
	case "fields":
		items = `{"type":"message","content":[` + strings.TrimSuffix(strings.Repeat(`{"type":"output_text","text":"a"},`, 257), ",") + `]}`
	case "arguments-depth":
		items = fmt.Sprintf(`{"type":"function_call","arguments":%q}`, strings.Repeat("[", 257)+"0"+strings.Repeat("]", 257))
	case "marshal":
		items = fmt.Sprintf(`{"type":"message","content":[{"type":"output_text","text":%q}]}`, token)
		metadata = `,"metadata":"` + strings.Repeat("&", 710000) + `"`
	case "lookups":
		var text strings.Builder
		text.WriteString(token)
		for n := 0; n < 50001; n++ {
			fmt.Fprintf(&text, "{{SNU_%032x}}", n)
		}
		items = fmt.Sprintf(`{"type":"message","content":[{"type":"output_text","text":%q}]}`, text.String())
	case "raw":
		metadata = `,"metadata":"` + strings.Repeat("x", 4<<20) + `"`
	case "depth":
		metadata = `,"metadata":` + strings.Repeat("[", 300) + "0" + strings.Repeat("]", 300)
	}
	return []byte(`{"type":"response.created","stream_id":"successor","response":{"id":"successor","output":[` + items + `]}` + metadata + `}`)
}

// 每种能力错误同时验证配置、撤销、排队后续帧、重复终态和另一条受保护响应。
func TestWSBoundaryOpeningCapacityMatrix(t *testing.T) {
	for _, kind := range []string{"items", "fields", "calls", "arguments-depth", "marshal", "lookups", "raw", "depth"} {
		for _, mode := range []string{privacy.OnErrorFailOpen, privacy.OnErrorFailClosed} {
			t.Run(kind+"/"+mode, func(t *testing.T) {
				state, activity := activitySubmissionState()
				defer state.close("closed")
				filter := newRestoreTestFilter(t)
				filter.Settings.OnError = mode
				filter.Settings.OverLimitAction = privacy.OverLimitFailClosed
				i := &subscriptionWSInterceptor{filter: filter, submissions: state}
				token := boundaryCreate(t, i, "parent", "parent", false)
				raw := boundaryOpening(kind, token)
				opening := boundaryRead(t, i, raw)
				sub := state.byResponseID["successor"]
				if sub == nil || sub.restore != nil || sub.successorView != nil || state.restoring.Load() != 1 {
					t.Fatal("失败首帧残留回填器或影响其他响应")
				}
				page, err := filter.Dict.Page(0, 10, func(string) string { return "masked" })
				if err != nil || len(page.Entries) != 1 || page.Entries[0].TextCount != 0 || page.Entries[0].ToolCount != 0 {
					t.Fatal("尚未交付就记录了回填", err)
				}
				var got []byte
				send := func(b []byte) error { got = bytes.Clone(b); return nil }
				if mode == privacy.OnErrorFailClosed {
					if err := boundaryWrite(opening, send); err == nil || sub.restoreBypassed || gjson.GetBytes(got, "error.code").Str != "privacy_restore_failed" {
						t.Fatal("未遵守出错拒绝", err)
					}
					filter.Settings.ResponseRestoreEnabled = false
					later := boundaryRead(t, i, []byte(`{"type":"response.completed","response":{"id":"successor"}}`))
					if err := boundaryWrite(later, send); err == nil || sub.terminalRead != nil || sub.settled {
						t.Fatal("首帧拒绝后的排队帧被当作普通透传", err)
					}
					return
				}
				if !sub.restoreBypassed {
					t.Fatal("放行状态没有在首帧入队前固定")
				}
				// 后续帧先读入，再改变全局配置，然后才按顺序写出。
				deep := strings.Repeat("[", 300) + "0" + strings.Repeat("]", 300)
				terminalRaw := []byte(`{"type":"response.completed","response":{"id":"successor"},"metadata":` + deep + `}`)
				terminal := boundaryRead(t, i, terminalRaw)
				duplicate := boundaryRead(t, i, terminalRaw)
				filter.Settings.ResponseRestoreEnabled = false
				if activity.Count() != 2 || duplicate.Forward != nil || duplicate.Deliver != nil {
					t.Fatal("提前结算或没有抑制排队重复终态")
				}
				if err := boundaryWrite(opening, send); err != nil || !bytes.Equal(got, raw) {
					t.Fatal("首帧未完整原文放行", err)
				}
				if err := boundaryWrite(terminal, send); err != nil || !bytes.Equal(got, terminalRaw) || activity.Count() != 1 {
					t.Fatal("后续终态未完整原文放行", err)
				}
				parent := []byte(fmt.Sprintf(`{"type":"response.completed","response":{"id":"parent","output":[{"type":"message","content":[{"type":"output_text","text":%q}]}]}}`, token))
				if got, err = policyWSDeliver(t, i, subscriptionws.DirectionUpstreamToDownstream, parent); err != nil || bytes.Contains(got, []byte(token)) || activity.Count() != 0 {
					t.Fatal("同连接旧响应的保护被关闭", err)
				}
			})
		}
	}
}

// 新旧请求 × 客户端/服务端创建 × 开关变化 × 首帧/重复首帧，策略固定到响应结束。
func TestWSBoundarySettingsMatrix(t *testing.T) {
	for _, successor := range []bool{false, true} {
		for _, initial := range []bool{false, true} {
			for _, change := range []string{"restore", "mode"} {
				t.Run(fmt.Sprintf("successor=%t/initial=%t/%s", successor, initial, change), func(t *testing.T) {
					state, activity := activitySubmissionState()
					defer state.close("closed")
					filter := newRestoreTestFilter(t)
					i := &subscriptionWSInterceptor{filter: filter, submissions: state}
					token := boundaryCreate(t, i, "old", "old", false)
					set := func(on bool) {
						if change == "restore" {
							filter.Settings.ResponseRestoreEnabled = on
						} else if on {
							filter.Settings.Mode = privacy.ModeRedact
						} else {
							filter.Settings.Mode = privacy.ModeDisabled
						}
					}
					set(initial)
					if !successor {
						if _, err := policyWSDeliver(t, i, subscriptionws.DirectionDownstreamToUpstream, []byte(`{"type":"response.create","stream_id":"new","input":"ordinary"}`)); err != nil {
							t.Fatal(err)
						}
						set(!initial) // 请求发出后、created 到达前切换。
					}
					created := []byte(`{"type":"response.created","stream_id":"new","response":{"id":"new"}}`)
					opening := boundaryRead(t, i, created)
					set(!initial)
					if err := boundaryWrite(opening, func([]byte) error { return nil }); err != nil {
						t.Fatal(err)
					}
					if _, err := policyWSDeliver(t, i, subscriptionws.DirectionUpstreamToDownstream, created); err != nil {
						t.Fatal(err)
					}
					if state.submissionRestores(state.byResponseID["new"]) != initial {
						t.Fatal("重复 created 或配置变化改变了已发布策略")
					}
					metadata := "0"
					if !initial {
						metadata = strings.Repeat("[", 300) + "0" + strings.Repeat("]", 300)
					}
					raw := []byte(fmt.Sprintf(`{"type":"response.completed","response":{"id":"new","output":[{"type":"message","content":[{"type":"output_text","text":%q}]}]},"metadata":%s}`, token, metadata))
					got, err := policyWSDeliver(t, i, subscriptionws.DirectionUpstreamToDownstream, raw)
					if err != nil || !initial && !bytes.Equal(got, raw) || initial && bytes.Contains(got, []byte(token)) || activity.Count() != 1 {
						t.Fatal("新响应没有保持自己的交付策略", err)
					}
				})
			}
		}
	}
}

func TestWSBoundaryOpeningDeliveryFailure(t *testing.T) {
	state, activity := activitySubmissionState()
	defer state.close("closed")
	filter := newRestoreTestFilter(t)
	i := &subscriptionWSInterceptor{filter: filter, submissions: state}
	token := boundaryCreate(t, i, "parent", "parent", false)
	raw := []byte(fmt.Sprintf(`{"type":"response.created","stream_id":"new","response":{"id":"new","output":[{"type":"message","content":[{"type":"output_text","text":%q}]}]}}`, token))
	r := boundaryRead(t, i, raw)
	page, _ := filter.Dict.Page(0, 10, func(string) string { return "masked" })
	if len(page.Entries) != 1 || page.Entries[0].TextCount != 0 {
		t.Fatal("准备首帧就确认了交付")
	}
	want := errors.New("synthetic downstream write failure")
	calls := 0
	err := boundaryWrite(r, func([]byte) error { calls++; return want })
	if !errors.Is(err, want) || calls != 1 || state.byResponseID["new"].restoreBypassed || activity.Count() != 2 {
		t.Fatal("网络写入错误触发了原文回退或提前结算", err)
	}
	page, _ = filter.Dict.Page(0, 10, func(string) string { return "masked" })
	if page.Entries[0].TextCount != 0 {
		t.Fatal("发送失败仍增加交付计数")
	}
}

func TestWSBoundaryProtocolErrorsNeverFallback(t *testing.T) {
	for _, raw := range []string{
		`{"type":"response.created","response":{"id":"new","output":[` + strings.Repeat(`{"type":"function_call","arguments":""},`, 257) + `42]}}`,
		`{"type":"response.created","response":{"id":"new","output":[42]}}`,
		`{"type":"response.created","response":{"id":"new"},"response_id":"other"}`,
		`{"type":"response.created","response":{"id":"new"},"stream_id":"one","Stream_ID":"two"}`,
		`{"type":"response.created","response":{"id":"new"},"response":{"id":"other"}}`,
		`{"type":"response.created","response":{"id":"new"}`,
	} {
		t.Run(fmt.Sprint(len(raw)), func(t *testing.T) {
			state, _ := activitySubmissionState()
			defer state.close("closed")
			i := &subscriptionWSInterceptor{filter: newRestoreTestFilter(t), submissions: state}
			got, err := policyWSDeliver(t, i, subscriptionws.DirectionUpstreamToDownstream, []byte(raw))
			if err == nil || gjson.GetBytes(got, "error.code").Str != "privacy_restore_failed" || state.restoring.Load() != 0 || state.bypassed.Load() != 0 {
				t.Fatal("协议错误被当成本地能力不足放行", err)
			}
		})
	}
}

func TestWSBoundaryEscapedRoutingKeys(t *testing.T) {
	i, token := routePolicyPair(t)
	raw := []byte(fmt.Sprintf(`{"\u0074ype":"response.completed","response_\u0069d":"protected","response":{"id":"protected","output":[{"type":"message","content":[{"type":"output_text","text":%q}]}]}}`, token))
	got, err := policyWSDeliver(t, i, subscriptionws.DirectionUpstreamToDownstream, raw)
	if err != nil || !json.Valid(got) || bytes.Contains(got, []byte(token)) || i.submissions.byResponseID["open"] == nil {
		t.Fatal("转义字段名导致授权或交付不一致", err)
	}
}

func TestWSBoundaryPlainTerminalDedupAfterAllProtectionEnds(t *testing.T) {
	state, activity := activitySubmissionState()
	defer state.close("closed")
	filter := newRestoreTestFilter(t)
	filter.Settings.ResponseRestoreEnabled = false
	i := &subscriptionWSInterceptor{filter: filter, submissions: state}
	boundaryCreate(t, i, "plain", "plain", false)
	raw := []byte(`{"type":"response.completed","response":{"id":"plain"}}`)
	if _, err := policyWSDeliver(t, i, subscriptionws.DirectionUpstreamToDownstream, raw); err != nil || activity.Count() != 0 {
		t.Fatal(err)
	}
	r := boundaryRead(t, i, raw)
	if r.Forward != nil || r.Deliver != nil || r.Forwarded != nil {
		t.Fatal("没有保护中的响应时，已完成的普通透传响应又被交付")
	}
}
