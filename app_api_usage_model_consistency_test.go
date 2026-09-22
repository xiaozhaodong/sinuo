package main

import (
	"encoding/json"
	"testing"

	"cc-forwarder/internal/tracking"
)

// TestRequestDetailToRecord_ModelConsistencyColumns 5 个新列逐项映射，且空值 omitempty 不出现在 JSON 中。
func TestRequestDetailToRecord_ModelConsistencyColumns(t *testing.T) {
	detail := tracking.RequestDetail{
		RequestID:         "req-mc-dto",
		ModelName:         "gpt-5.5",
		RequestModel:      "gpt-5.4",
		UpstreamModel:     "gpt-5.5",
		ResponseModel:     "gpt-5.5",
		RequestReasoning:  "effort:xhigh",
		ResponseReasoning: "effort:medium reasoning_tokens:12",
	}
	record := requestDetailToRecord(detail)
	if record.Model != "gpt-5.5" || record.RequestModel != "gpt-5.4" || record.UpstreamModel != "gpt-5.5" ||
		record.ResponseModel != "gpt-5.5" || record.RequestReasoning != "effort:xhigh" || record.ResponseReasoning != "effort:medium reasoning_tokens:12" {
		t.Fatalf("record mapping mismatch: %+v", record)
	}

	raw, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"request_model", "upstream_model", "response_model", "request_reasoning", "response_reasoning"} {
		if _, ok := payload[key]; !ok {
			t.Fatalf("json key %s missing", key)
		}
	}

	legacy := requestDetailToRecord(tracking.RequestDetail{RequestID: "req-legacy", ModelName: "claude-test"})
	raw, _ = json.Marshal(legacy)
	payload = map[string]any{}
	_ = json.Unmarshal(raw, &payload)
	if _, ok := payload["response_model"]; ok {
		t.Fatalf("empty diagnostics must be omitted from json: %s", string(raw))
	}
}
