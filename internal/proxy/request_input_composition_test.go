package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"time"

	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"sinuo/internal/inputcomposition"
	"sinuo/internal/middleware"
	"sinuo/internal/store"
	"sinuo/internal/tracking"
)

func TestInputCompositionThroughProxy(t *testing.T) {
	tracker := newLifecyclePanelTestTracker(t)
	tracker.InputWindows.Replace(map[string]int64{"gpt-5.4": 30000, "gpt-5.5": 120000})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp_test","model":"gpt-5.5","usage":{"input_tokens":42,"output_tokens":1}}`))
	}))
	defer upstream.Close()
	pool := &mockAccountPoolService{accounts: []*store.UpstreamAccountRecord{{ID: 1, AccountName: "test", ProviderType: "api_key", CredentialRaw: "test-fixture", BaseURL: upstream.URL, ModelRewriteRules: testGPT54ModelRewriteRules, Enabled: true}}}
	h := newAccountPipelineTestHandler(t, upstream.URL, pool)
	h.SetMonitoringMiddleware(middleware.NewMonitoringMiddleware(h.endpointManager))
	h.SetUsageTracker(tracker)
	const id = "composition-end-to-end"
	body := mustEncodeZstd(t, []byte(`{"model":"gpt-5.4","input":"private user body","tools":[{"name":"Read","type":"function"}]}`))
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
	req = req.WithContext(context.WithValue(req.Context(), "conn_id", id))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Content-Encoding", "zstd")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatal(w.Code, w.Body.String())
	}
	if err := tracker.ForceFlush(); err != nil {
		t.Fatal(err)
	}
	row := timingLifecycleData(t, tracker, id)
	var s inputcomposition.Summary
	if err := json.Unmarshal([]byte(row.InputCompositionJSON), &s); err != nil {
		t.Fatal(err)
	}
	if len(s.Items) != 2 || s.Window != 120000 || s.WindowModel != "gpt-5.5" || s.WindowSource != "codex_catalog" || row.Detail.InputTokens != 42 ||
		strings.Contains(row.InputCompositionJSON, "private user body") {
		t.Fatalf("归档后摘要与实际模型不匹配: %+v", s)
	}
	tracker.InputWindows.Replace(nil)
	if timingLifecycleData(t, tracker, id).InputCompositionJSON != row.InputCompositionJSON {
		t.Fatal("后续目录变更影响了历史摘要")
	}
}

func TestInputCompositionSubscriptionCreate(t *testing.T) {
	for _, hot := range []bool{true, false} {
		t.Run(fmt.Sprintf("hot=%t", hot), func(t *testing.T) {
			tracker := newTimingTestTracker(t, hot)
			observer := newSubscriptionTrackingObserver(tracker, nil)
			state := newTestSubscriptionSubmissionState(context.Background(), "composition-ws", observer)
			payload := []byte(`{"type":"response.create","response":{"model":"demo","previous_response_id":"hidden","input":"hello"}}`)
			sub, _ := beginTestSubscriptionCreate(t, state, payload)
			defer state.close("subscription_interrupted")
			if err := tracker.ForceFlush(); err != nil {
				t.Fatal(err)
			}
			read := func() string {
				data, err := tracker.GetRequestLifecycleData(context.Background(), sub.RequestID)
				if err != nil {
					t.Fatal(err)
				}
				if data == nil {
					return ""
				}
				return data.InputCompositionJSON
			}
			require.Eventually(t, func() bool { return read() != "" }, 3*time.Second, 10*time.Millisecond)
			raw := read()
			var summary inputcomposition.Summary
			if err := json.Unmarshal([]byte(raw), &summary); err != nil || !summary.Partial || len(summary.Items) != 1 || summary.Items[0].Estimate != 2 {
				t.Fatal(raw, err)
			}
			state.handleUpstream([]byte(`{"type":"response.created","response":{"id":"resp-input"}}`))
			state.handleUpstream([]byte(`{"type":"response.completed","response":{"id":"resp-input","usage":{"input_tokens":120,"output_tokens":2}}}`))
			require.Eventually(t, func() bool {
				data, err := tracker.GetRequestLifecycleData(context.Background(), sub.RequestID)
				return err == nil && data != nil && data.Source == "database" && data.InputCompositionJSON == raw && data.Detail.InputTokens == 120
			}, 3*time.Second, 10*time.Millisecond)
		})
	}
}

func TestMessagesInputWindow(t *testing.T) {
	tracker := newLifecyclePanelTestTracker(t)
	lifecycle := NewRequestLifecycleManager(tracker, nil, "messages-window", nil)
	lifecycle.SetRequestProfile("claude-haiku-4-5", "")
	lifecycle.inputComposition = captureInput(tracker, "/v1/messages", []byte(`{"messages":[]}`), "claude-haiku-4-5")
	check := func(model string, window int64) {
		t.Helper()
		s := lifecycle.inputComposition.summary
		require.Equal(t, model, s.WindowModel)
		require.Equal(t, window, s.Window)
		if window > 0 {
			require.Equal(t, "model_spec", s.WindowSource)
		}
	}
	check("claude-haiku-4-5", 200_000)
	lifecycle.SetUpstreamModel("claude-opus-4-8")
	check("claude-opus-4-8", 1_000_000)
	lifecycle.SetUpstreamModel("claude-opus-4-6-20260401")
	check("claude-opus-4-6-20260401", 1_000_000)
	lifecycle.SetResponseModel("claude-opus-4-6-20260401")
	check("claude-opus-4-6-20260401", 1_000_000)
	lifecycle.SetResponseModel("deepseek-flash")
	check("deepseek-flash", 1_048_576)
	lifecycle.SetResponseModel("custom-flash")
	check("", 0)
	require.Zero(t, inputcomposition.MessagesModelWindow("custom-flash[1m]"))
	require.Zero(t, inputcomposition.MessagesModelWindow("custom-flash-20260401"))
	require.EqualValues(t, 1_048_576, inputcomposition.MessagesModelWindow("deepseek-flash[1m]"))
	require.EqualValues(t, 1_000_000, inputcomposition.MessagesModelWindow("claude-opus-4-6-20260401"))
	require.EqualValues(t, 1_000_000, inputcomposition.MessagesModelWindow("claude-sonnet-4-6-2026-02-17[1m]"))
}

func TestCodexCatalogInputWindowFollowsResponseModel(t *testing.T) {
	t.Run("websocket_and_tool_echo", func(t *testing.T) {
		for _, hot := range []bool{true, false} {
			t.Run(fmt.Sprintf("hot=%t", hot), func(t *testing.T) {
				tracker := newTimingTestTracker(t, hot)
				observer := newSubscriptionTrackingObserver(tracker, nil)
				state := newTestSubscriptionSubmissionState(context.Background(), "ws-window", observer)
				defer state.close("end")

				readArchived := func(requestID string) inputcomposition.Summary {
					t.Helper()
					var s inputcomposition.Summary
					require.Eventually(t, func() bool {
						row, err := tracker.GetRequestLifecycleData(context.Background(), requestID)
						return err == nil && row != nil && row.Source == "database" && row.Detail.Status == "completed" &&
							json.Unmarshal([]byte(row.InputCompositionJSON), &s) == nil
					}, 3*time.Second, 10*time.Millisecond)
					return s
				}

				for _, tc := range []struct {
					name          string
					reqModel      string
					createdModel  string
					finalModel    string
					createdTools  string
					finalTools    string
					wantWindow    int64
					wantModel     string
					wantSource    string
					wantToolEcho  string
					wantEchoCount int
				}{
					{
						name:     "created_resolves_alias_with_tool_echo",
						reqModel: "alias-120k", createdModel: "gpt-5.4", finalModel: "gpt-5.4",
						createdTools: `,"tools":[{"type":"function","name":"Start"}]`,
						finalTools:   `,"tools":[{"type":"function","name":"Final"}]`,
						wantWindow:   400_000, wantModel: "gpt-5.4", wantSource: "codex_catalog",
						wantToolEcho: "reported", wantEchoCount: 1,
					},
					{
						name:     "completed_only_resolves_alias",
						reqModel: "alias-120k", createdModel: "", finalModel: "gpt-5.4",
						wantWindow: 400_000, wantModel: "gpt-5.4", wantSource: "codex_catalog",
						wantToolEcho: "unreported",
					},
					{
						name:     "date_snapshot_preserves_catalog_window",
						reqModel: "gpt-5.4", createdModel: "gpt-5.4-2026-03-05", finalModel: "gpt-5.4-2026-03-05",
						wantWindow: 400_000, wantModel: "gpt-5.4", wantSource: "codex_catalog",
						wantToolEcho: "unreported",
					},
					{
						name:     "unrelated_unlisted_model_clears_alias_window",
						reqModel: "alias-120k", createdModel: "custom-unlisted", finalModel: "custom-unlisted",
						wantWindow: 0, wantModel: "", wantSource: "",
						wantToolEcho: "unreported",
					},
				} {
					tracker.InputWindows.Replace(map[string]int64{"alias-120k": 120_000, "gpt-5.4": 400_000})
					sub, _ := beginTestSubscriptionCreate(t, state, []byte(fmt.Sprintf(
						`{"type":"response.create","model":%q,"previous_response_id":"outside","input":"hello"}`, tc.reqModel,
					)))
					// 模拟请求开始后全局目录变更，验证仍使用请求当时的目录快照。
					tracker.InputWindows.Replace(nil)

					createdModelField := ""
					if tc.createdModel != "" {
						createdModelField = fmt.Sprintf(`,"model":%q`, tc.createdModel)
					}
					finalModelField := ""
					if tc.finalModel != "" {
						finalModelField = fmt.Sprintf(`,"model":%q`, tc.finalModel)
					}
					respID := "resp-" + tc.name
					state.handleUpstream([]byte(fmt.Sprintf(`{"type":"response.created","response":{"id":%q%s%s}}`, respID, createdModelField, tc.createdTools)))
					state.handleUpstream([]byte(fmt.Sprintf(`{"type":"response.completed","response":{"id":%q,"usage":{"input_tokens":50,"output_tokens":1}%s%s}}`, respID, finalModelField, tc.finalTools)))

					got := readArchived(sub.RequestID)
					require.Equal(t, tc.wantWindow, got.Window, tc.name)
					require.Equal(t, tc.wantModel, got.WindowModel, tc.name)
					require.Equal(t, tc.wantSource, got.WindowSource, tc.name)
					require.Equal(t, tc.wantToolEcho, got.ToolEcho, tc.name)
					require.Len(t, got.EchoItems, tc.wantEchoCount, tc.name)
				}
			})
		}
	})

	t.Run("subscription_http", func(t *testing.T) {
		tracker := newTimingTestTracker(t, true)
		tracker.InputWindows.Replace(map[string]int64{"alias-120k": 120_000, "gpt-5.4": 400_000})
		observer := newSubscriptionTrackingObserver(tracker, nil)
		track := observer.StartHTTP(SubscriptionHTTPStart{
			RequestID: "sub-http-window", Method: http.MethodPost, Path: "/v1/responses",
			RequestKind: tracking.RequestKindGeneration, RequestModel: "alias-120k",
		})
		track.setInputComposition(captureInput(tracker, "/v1/responses", []byte(`{"model":"alias-120k","input":"hello"}`), "alias-120k"))
		tracker.InputWindows.Replace(nil)
		track.ApplyParseResult(&ParseResult{
			ModelName: "gpt-5.4", ResponseModel: "gpt-5.4", IsCompleted: true,
			HasTokenUsage: true, TokenUsage: &tracking.TokenUsage{InputTokens: 64, OutputTokens: 8},
		})
		var got inputcomposition.Summary
		require.Eventually(t, func() bool {
			row, err := tracker.GetRequestLifecycleData(context.Background(), "sub-http-window")
			return err == nil && row != nil && row.Source == "database" && row.Detail.Status == "completed" &&
				json.Unmarshal([]byte(row.InputCompositionJSON), &got) == nil
		}, 3*time.Second, 10*time.Millisecond)
		require.EqualValues(t, 400_000, got.Window)
		require.Equal(t, "gpt-5.4", got.WindowModel)
		require.Equal(t, "codex_catalog", got.WindowSource)
	})

	t.Run("account_pool_lifecycle", func(t *testing.T) {
		tracker := newLifecyclePanelTestTracker(t)
		tracker.InputWindows.Replace(map[string]int64{"alias-120k": 120_000, "gpt-5.4": 400_000})
		lifecycle := NewRequestLifecycleManager(tracker, nil, "codex-window", nil)
		lifecycle.SetRequestProfile("alias-120k", "")
		lifecycle.inputComposition = captureInput(tracker, "/v1/responses", []byte(`{"input":"hello"}`), "alias-120k")
		tracker.InputWindows.Replace(nil)

		require.EqualValues(t, 120_000, lifecycle.inputComposition.summary.Window)
		lifecycle.SetResponseModel("gpt-5.4")
		require.EqualValues(t, 400_000, lifecycle.inputComposition.summary.Window)
		require.Equal(t, "gpt-5.4", lifecycle.inputComposition.summary.WindowModel)
		lifecycle.SetResponseModel("gpt-5.4-2026-03-05")
		require.EqualValues(t, 400_000, lifecycle.inputComposition.summary.Window)
		require.Equal(t, "gpt-5.4", lifecycle.inputComposition.summary.WindowModel)
		lifecycle.SetResponseModel("custom-unlisted")
		require.Zero(t, lifecycle.inputComposition.summary.Window)
		require.Empty(t, lifecycle.inputComposition.summary.WindowModel)
	})
}

func TestHistoricalMessagesWindow(t *testing.T) {
	const original = `{"version":1,"items":[],"extra_field":"preserved"}`
	raw, err := inputcomposition.WithHistoricalMessagesWindow(original, "", "deepseek-flash", "claude-haiku-4-5")
	require.NoError(t, err)
	require.JSONEq(t, `{"version":1,"items":[],"extra_field":"preserved","window":1048576,"window_source":"historical_model_spec","window_model":"deepseek-flash"}`, raw)
	dated, err := inputcomposition.WithHistoricalMessagesWindow(original, "claude-opus-4-6-20260401", "claude-opus-4-6")
	require.NoError(t, err)
	require.JSONEq(t, `{"version":1,"items":[],"extra_field":"preserved","window":1000000,"window_source":"historical_model_spec","window_model":"claude-opus-4-6-20260401"}`, dated)
	for _, tc := range []struct{ raw, model string }{
		{raw, "claude-opus-4-8"}, {original, "custom-model"}, {"", "deepseek-flash"},
		{`{"version":2,"items":[]}`, "deepseek-flash"},
	} {
		got, err := inputcomposition.WithHistoricalMessagesWindow(tc.raw, tc.model, "deepseek-flash")
		require.NoError(t, err)
		require.Equal(t, tc.raw, got)
	}
}

func TestSubscriptionCompositionFollowsOnlyItsOwnCompletedParent(t *testing.T) {
	tracker := newTimingTestTracker(t, true)
	observer := newSubscriptionTrackingObserver(tracker, nil)
	state := newTestSubscriptionSubmissionState(context.Background(), "history-one", observer)
	defer state.close("end")
	open := func(state *subscriptionSubmissionState, id, previous, input string) *subscriptionSubmission {
		t.Helper()
		sub, _ := beginTestSubscriptionCreate(t, state, []byte(fmt.Sprintf(`{"type":"response.create","previous_response_id":%q,"input":%s}`, previous, input)))
		state.handleUpstream([]byte(fmt.Sprintf(`{"type":"response.created","response":{"id":%q}}`, id)))
		return sub
	}
	finish := func(id string) {
		state.handleUpstream([]byte(fmt.Sprintf(`{"type":"response.completed","response":{"id":%q,"usage":{"input_tokens":1000,"output_tokens":10}}}`, id)))
	}
	read := func(sub *subscriptionSubmission) inputcomposition.Summary {
		t.Helper()
		var summary inputcomposition.Summary
		require.Eventually(t, func() bool {
			row, err := tracker.GetRequestLifecycleData(context.Background(), sub.RequestID)
			return err == nil && row != nil && json.Unmarshal([]byte(row.InputCompositionJSON), &summary) == nil
		}, 3*time.Second, 10*time.Millisecond)
		return summary
	}
	open(state, "r", "", `"question"`)
	item := []byte(`{"type":"response.output_item.done","response_id":"r","output_index":0,"item":{"type":"function_call","name":"Read","call_id":"x","arguments":{"path":"/private/main.go"}}}`)
	state.handleUpstream(item)
	state.handleUpstream(item)
	finish("r")
	const output = `[{"type":"function_call_output","call_id":"x","output":"abcdefgh"}]`
	for _, id := range []string{"left", "right"} {
		sub := open(state, id, "r", output)
		finish(id)
		s := read(sub)
		require.Contains(t, s.Items, inputcomposition.Item{Group: "files", Name: "main.go", Estimate: 2})
		require.Contains(t, s.HistoryItems, inputcomposition.Item{Group: "chat", Name: "第 1 轮对话", Estimate: 2})
		require.Len(t, s.HistoryItems, 2)
	}
	open(state, "failed", "", "[]")
	state.handleUpstream([]byte(`{"type":"response.failed","response":{"id":"failed"}}`))
	for _, id := range []string{"failed", "missing"} {
		sub := open(state, "child-"+id, id, output)
		finish("child-" + id)
		require.Empty(t, read(sub).HistoryItems)
	}
	newConnection := newTestSubscriptionSubmissionState(context.Background(), "history-two", observer)
	defer newConnection.close("end")
	other := open(newConnection, "other", "r", output)
	require.Empty(t, read(other).HistoryItems)
	for i := range subscriptionHistoryLimit {
		id := fmt.Sprintf("recent-%d", i)
		open(state, id, "", "[]")
		finish(id)
	}
	require.Nil(t, state.recentIDs["r"].history)
	require.NotNil(t, state.recentIDs[fmt.Sprintf("recent-%d", subscriptionHistoryLimit-1)].history)
}

func TestSubscriptionArchivesOnlyCurrentToolEcho(t *testing.T) {
	for _, hot := range []bool{true, false} {
		t.Run(fmt.Sprintf("hot=%t", hot), func(t *testing.T) {
			tracker := newTimingTestTracker(t, hot)
			state := newTestSubscriptionSubmissionState(context.Background(), "echo", newSubscriptionTrackingObserver(tracker, nil))
			defer state.close("end")
			for _, tc := range []struct{ id, previous, createdTools, finalTools, state, name string }{
				{"parent", "outside", `,"tools":[{"type":"function","name":"Start"}]`, `,"tools":[{"type":"function","name":"Final"}]`, "reported", "Final"},
				{"child", "parent", "", "", "unreported", ""},
				{"sibling", "parent", `,"tools":[{"type":"function","name":"Own"}]`, "", "reported", "Own"},
				{"empty", "parent", `,"tools":[{"type":"function","name":"Own"}]`, `,"tools":[]`, "empty", ""},
			} {
				sub, _ := beginTestSubscriptionCreate(t, state, []byte(fmt.Sprintf(`{"type":"response.create","previous_response_id":%q,"input":"hello"}`, tc.previous)))
				state.handleUpstream([]byte(fmt.Sprintf(`{"type":"response.created","response":{"id":%q%s}}`, tc.id, tc.createdTools)))
				state.handleUpstream([]byte(fmt.Sprintf(`{"type":"response.completed","response":{"id":%q,"usage":{"input_tokens":50,"output_tokens":1}%s}}`, tc.id, tc.finalTools)))
				require.Eventually(t, func() bool {
					row, err := tracker.GetRequestLifecycleData(context.Background(), sub.RequestID)
					if err != nil || row == nil || row.Source != "database" {
						return false
					}
					var s inputcomposition.Summary
					if json.Unmarshal([]byte(row.InputCompositionJSON), &s) != nil || s.ToolEcho != tc.state {
						return false
					}
					for _, i := range s.HistoryItems {
						if i.Group == "tools" {
							return false
						}
					}
					if tc.name == "" {
						return len(s.EchoItems) == 0
					}
					return len(s.EchoItems) == 1 && s.EchoItems[0].Name == tc.name
				}, 3*time.Second, 10*time.Millisecond)
			}
		})
	}
}

func TestSubscriptionBeginCreateWaitsForInFlightParentHistory(t *testing.T) {
	t.Run("waits_outside_lock_and_inherits_completed_parent", func(t *testing.T) {
		tracker := newTimingTestTracker(t, true)
		observer := newSubscriptionTrackingObserver(tracker, nil)
		state := newTestSubscriptionSubmissionState(context.Background(), "pipeline-wait", observer)
		defer state.close("end")

		parentSub, _ := beginTestSubscriptionCreate(t, state, []byte(`{"type":"response.create","input":"first turn"}`))
		state.handleUpstream([]byte(`{"type":"response.created","response":{"id":"resp-parent"}}`))
		state.handleUpstream([]byte(`{"type":"response.output_item.done","response_id":"resp-parent","output_index":0,"item":{"type":"function_call","name":"Read","call_id":"call-1","arguments":{"path":"/private/main.go"}}}`))

		// 模拟 completeInputHistory 已读入终态帧并正在锁外计算快照。
		ready := make(chan struct{})
		state.mu.Lock()
		input, done := parentSub.inputHistory, parentSub.outputItems
		parentSub.outputItems = nil
		parentSub.inputHistoryReady = ready
		state.mu.Unlock()

		childDone := make(chan *subscriptionSubmission, 1)
		go func() {
			sub, _ := beginTestSubscriptionCreate(t, state, []byte(`{"type":"response.create","previous_response_id":"resp-parent","input":[{"type":"function_call_output","call_id":"call-1","output":"abcdefgh"}]}`))
			childDone <- sub
		}()

		select {
		case <-childDone:
			t.Fatal("子请求未等待正在计算的父历史快照")
		case <-time.After(30 * time.Millisecond):
		}

		// 若 beginCreate 持锁等待，这里获取 state.mu 将发生死锁；顺利加锁证明其是在锁外等待。
		completed := input.Completed([]byte(`{"response":{"status":"completed","usage":{"input_tokens":1000}}}`), 1000, done)
		state.mu.Lock()
		parentSub.terminalRead = &ParseResult{IsCompleted: true}
		parentSub.inputHistory = completed
		parentSub.inputHistoryReady = nil
		close(ready)
		state.mu.Unlock()

		var childSub *subscriptionSubmission
		select {
		case childSub = <-childDone:
		case <-time.After(3 * time.Second):
			t.Fatal("父历史快照完成后子请求未及时唤醒")
		}

		var summary inputcomposition.Summary
		require.Eventually(t, func() bool {
			row, err := tracker.GetRequestLifecycleData(context.Background(), childSub.RequestID)
			return err == nil && row != nil && json.Unmarshal([]byte(row.InputCompositionJSON), &summary) == nil
		}, 3*time.Second, 10*time.Millisecond)
		require.Contains(t, summary.Items, inputcomposition.Item{Group: "files", Name: "main.go", Estimate: 2})
		require.Contains(t, summary.HistoryItems, inputcomposition.Item{Group: "chat", Name: "第 1 轮对话", Estimate: 3})
	})

	t.Run("unblocks_when_connection_context_canceled", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		tracker := newTimingTestTracker(t, true)
		state := newTestSubscriptionSubmissionState(ctx, "pipeline-cancel", newSubscriptionTrackingObserver(tracker, nil))
		defer state.close("end")

		parentSub, _ := beginTestSubscriptionCreate(t, state, []byte(`{"type":"response.create","input":"first"}`))
		state.handleUpstream([]byte(`{"type":"response.created","response":{"id":"resp-parent"}}`))

		ready := make(chan struct{})
		state.mu.Lock()
		parentSub.inputHistoryReady = ready
		state.mu.Unlock()

		done := make(chan struct{})
		go func() {
			defer close(done)
			_, _, _ = state.beginCreate([]byte(`{"type":"response.create","previous_response_id":"resp-parent","input":"next"}`))
		}()

		cancel()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Fatal("连接上下文取消后等待父历史的 beginCreate 未退出")
		}
		close(ready)
	})
}
