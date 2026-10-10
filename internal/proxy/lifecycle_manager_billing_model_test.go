package proxy

import (
	"net/http"
	"testing"

	"cc-forwarder/internal/tracking"
)

// 六条记录路径统一选择计费模型，同时保留客户端原始模型与 GetModelName 的语义。
func TestRequestLifecycleManager_BillingModelRecording(t *testing.T) {
	paths := []struct {
		name            string
		status          string
		wantInputTokens int64
		record          func(*RequestLifecycleManager)
	}{
		{
			name:   "CompleteRequest",
			status: "completed",
			record: func(rlm *RequestLifecycleManager) { rlm.CompleteRequest(nil) },
		},
		{
			name:   "CompleteRequestWithCost",
			status: "completed",
			record: func(rlm *RequestLifecycleManager) { rlm.CompleteRequestWithCost(0.25) },
		},
		{
			name:   "CompleteRequestWithQuality",
			status: "completed",
			record: func(rlm *RequestLifecycleManager) { rlm.CompleteRequestWithQuality(nil, "incomplete_stream") },
		},
		{
			name:   "FailRequest",
			status: "failed",
			record: func(rlm *RequestLifecycleManager) {
				rlm.FailRequest("account_pool_exhausted", "no available accounts", http.StatusServiceUnavailable)
			},
		},
		{
			name:   "CancelRequest",
			status: "cancelled",
			record: func(rlm *RequestLifecycleManager) { rlm.CancelRequest("client_cancelled", nil) },
		},
		{
			name:            "RecordTokensForFailedRequest",
			status:          "pending",
			wantInputTokens: 3,
			record: func(rlm *RequestLifecycleManager) {
				rlm.RecordTokensForFailedRequest(&tracking.TokenUsage{InputTokens: 3, OutputTokens: 2}, "partial_usage")
			},
		},
	}
	cases := []struct {
		name           string
		upstreamModels []string
		responseModel  string
		parsedModel    string
		wantModel      string
	}{
		{
			name:           "rewritten_without_response",
			upstreamModels: []string{"B"},
			wantModel:      "B",
		},
		{
			name:           "response_model_wins",
			upstreamModels: []string{"B"},
			responseModel:  "C",
			wantModel:      "C",
		},
		{
			name:      "no_rewrite",
			wantModel: "A",
		},
		{
			name:           "failover_clears_rewrite",
			upstreamModels: []string{"B", ""},
			wantModel:      "A",
		},
		{
			name:           "parsed_model_without_response_diagnostic",
			upstreamModels: []string{"B"},
			parsedModel:    "C",
			wantModel:      "C",
		},
		{
			name:           "image_same_request_and_upstream",
			upstreamModels: []string{"A"},
			wantModel:      "A",
		},
	}

	for _, path := range paths {
		t.Run(path.name, func(t *testing.T) {
			tracker := newLifecyclePanelTestTracker(t)
			for _, tc := range cases {
				t.Run(tc.name, func(t *testing.T) {
					requestID := t.Name()
					rlm := NewRequestLifecycleManager(tracker, nil, requestID, nil)
					rlm.SetRequestProfile("A", "")
					rlm.SetModel("A")
					rlm.StartRequest("127.0.0.1", "test-agent", http.MethodPost, "/v1/responses", false)
					for _, model := range tc.upstreamModels {
						rlm.SetUpstreamModel(model)
					}
					if tc.responseModel != "" {
						rlm.SetResponseModel(tc.responseModel)
						rlm.SetModelWithComparison(tc.responseModel, "response")
					}
					if tc.parsedModel != "" {
						rlm.SetModelWithComparison(tc.parsedModel, "response")
					}
					modelBeforeRecording := rlm.GetModelName()

					path.record(rlm)
					detail := waitRequestDetail(t, tracker, requestID, func(d tracking.RequestDetail) bool {
						return d.Status == path.status && d.InputTokens == path.wantInputTokens
					})
					if detail.ModelName != tc.wantModel {
						t.Errorf("recorded billing model = %q, want %q", detail.ModelName, tc.wantModel)
					}
					if detail.RequestModel != "A" {
						t.Errorf("request model = %q, want A", detail.RequestModel)
					}
					if got := rlm.GetModelName(); got != modelBeforeRecording {
						t.Errorf("GetModelName changed after recording: %q -> %q", modelBeforeRecording, got)
					}
				})
			}
		})
	}
}

// 空模型与 unknown 统一回落，解析过程不修改当前模型字段。
func TestRequestLifecycleManager_BillingModelNameFallback(t *testing.T) {
	for _, tc := range []struct {
		name          string
		modelName     string
		upstreamModel string
		responseModel string
		wantModel     string
	}{
		{name: "empty_without_upstream", wantModel: "unknown"},
		{name: "unknown_without_upstream", modelName: "unknown", wantModel: "unknown"},
		{name: "empty_with_upstream", upstreamModel: "B", wantModel: "B"},
		{name: "unknown_with_upstream", modelName: "unknown", upstreamModel: "B", wantModel: "B"},
		{name: "response_diagnostic_keeps_current_model", modelName: "A", upstreamModel: "B", responseModel: "C", wantModel: "A"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rlm := NewRequestLifecycleManager(nil, nil, t.Name(), nil)
			rlm.SetRequestProfile("A", "")
			rlm.SetModel(tc.modelName)
			rlm.SetUpstreamModel(tc.upstreamModel)
			rlm.SetResponseModel(tc.responseModel)
			if got := rlm.billingModelName(); got != tc.wantModel {
				t.Errorf("billing model = %q, want %q", got, tc.wantModel)
			}
			if got := rlm.GetModelName(); got != tc.modelName {
				t.Errorf("GetModelName = %q, want %q", got, tc.modelName)
			}
		})
	}
}
