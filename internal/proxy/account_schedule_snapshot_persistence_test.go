package proxy

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	svc "sinuo/internal/service"
)

func newRuntimeSnapshot() *svc.LatestAccountScheduleSnapshot {
	return &svc.LatestAccountScheduleSnapshot{
		RequestID:           "req-persist",
		CapturedAt:          time.Now(),
		UpdatedAt:           time.Now(),
		RequestPath:         "/v1/responses",
		SelectedPriority:    10,
		SelectedTierIndex:   1,
		SelectedTierLabel:   "主组",
		SelectedAccountID:   7,
		SelectedAccountName: "acct-a",
		FinalOutcome:        svc.AccountScheduleOutcomeSuccess,
		FinalError:          "sk-secret-token-should-not-leak",
		Summary:             "调度器内部摘要",
		Candidates: []svc.AccountScheduleCandidateDecision{
			{
				AccountID:      7,
				AccountName:    "acct-a",
				ProviderType:   "oauth",
				Decision:       "selected",
				Reason:         "highest_ranked_in_selected_tier",
				ReasonDetail:   "内部排序说明",
				RuntimeOutcome: svc.AccountScheduleOutcomeSuccess,
				RuntimeError:   "Bearer eyJhbGciOiJIUzI1NiJ9-secret",
			},
		},
	}
}

func parseSnapshotPayload(t *testing.T, payload string) map[string]interface{} {
	t.Helper()
	var parsed map[string]interface{}
	if err := json.Unmarshal([]byte(payload), &parsed); err != nil {
		t.Fatalf("failed to unmarshal payload: %v", err)
	}
	return parsed
}

// TestMarshalScheduleSnapshot_Nil 空输入返回空串。
func TestMarshalScheduleSnapshot_Nil(t *testing.T) {
	if payload := marshalScheduleSnapshot(nil); payload != "" {
		t.Fatalf("expected empty payload for nil snapshot, got %q", payload)
	}
}

// TestMarshalScheduleSnapshot_ExcludesFreeErrorText 序列化产物不落 final_error / runtime_error 自由原文。
func TestMarshalScheduleSnapshot_ExcludesFreeErrorText(t *testing.T) {
	snapshot := newRuntimeSnapshot()
	payload := marshalScheduleSnapshot(snapshot)
	if payload == "" {
		t.Fatal("expected non-empty payload")
	}
	if strings.Contains(payload, "sk-secret-token-should-not-leak") {
		t.Fatal("final_error leaked into persisted payload")
	}
	if strings.Contains(payload, "eyJhbGciOiJIUzI1NiJ9") {
		t.Fatal("runtime_error leaked into persisted payload")
	}

	parsed := parseSnapshotPayload(t, payload)
	if _, exists := parsed["final_error"]; exists {
		t.Fatal("persisted payload must not contain final_error key")
	}
	if parsed["final_outcome"] != svc.AccountScheduleOutcomeSuccess {
		t.Fatalf("expected final_outcome preserved, got %v", parsed["final_outcome"])
	}
	candidates, ok := parsed["candidates"].([]interface{})
	if !ok || len(candidates) != 1 {
		t.Fatal("expected one candidate")
	}
	candidate := candidates[0].(map[string]interface{})
	if _, exists := candidate["runtime_error"]; exists {
		t.Fatal("candidate must not contain runtime_error key")
	}
	if candidate["runtime_outcome"] != svc.AccountScheduleOutcomeSuccess {
		t.Fatalf("expected runtime_outcome preserved, got %v", candidate["runtime_outcome"])
	}
}

// TestMarshalScheduleSnapshot_TruncatesSummaryAndReasonDetail 内部生成文本 rune 安全截断兜底。
func TestMarshalScheduleSnapshot_TruncatesSummaryAndReasonDetail(t *testing.T) {
	snapshot := newRuntimeSnapshot()
	snapshot.Summary = strings.Repeat("中", 600)
	snapshot.Candidates[0].ReasonDetail = strings.Repeat("文", 300)

	payload := marshalScheduleSnapshot(snapshot)
	parsed := parseSnapshotPayload(t, payload)

	summary, _ := parsed["summary"].(string)
	if runeLen(summary) > 512 {
		t.Fatalf("summary must be truncated to <=512 runes, got %d", runeLen(summary))
	}
	candidates := parsed["candidates"].([]interface{})
	reasonDetail := candidates[0].(map[string]interface{})["reason_detail"].(string)
	if runeLen(reasonDetail) > 256 {
		t.Fatalf("reason_detail must be truncated to <=256 runes, got %d", runeLen(reasonDetail))
	}
}

// TestMarshalScheduleSnapshot_DropsCandidatesOver32KB 序列化超 32KB 时丢弃候选列表仍可写。
func TestMarshalScheduleSnapshot_DropsCandidatesOver32KB(t *testing.T) {
	snapshot := newRuntimeSnapshot()
	snapshot.Candidates = nil
	for i := 0; i < 5000; i++ {
		snapshot.Candidates = append(snapshot.Candidates, svc.AccountScheduleCandidateDecision{
			AccountID:    int64(i),
			AccountName:  "acct",
			Decision:     "skipped",
			ReasonDetail: strings.Repeat("文", 256),
		})
	}

	payload := marshalScheduleSnapshot(snapshot)
	if payload == "" {
		t.Fatal("expected payload with candidates dropped, got empty")
	}
	if len(payload) > scheduleSnapshotJSONSizeLimit {
		t.Fatalf("payload must fit 32KB fuse, got %d", len(payload))
	}
	parsed := parseSnapshotPayload(t, payload)
	// nil 切片序列化为 null；关键是不再携带任何候选。
	candidates, exists := parsed["candidates"]
	if exists && candidates != nil {
		t.Fatal("expected candidates dropped over 32KB fuse")
	}
	if parsed["request_id"] != "req-persist" {
		t.Fatal("expected header fields preserved")
	}
}

func TestMarshalScheduleSnapshot_PreservesCodexModelRoutingScope(t *testing.T) {
	payload := marshalScheduleSnapshot(&svc.LatestAccountScheduleSnapshot{
		RequestID: "codex-routing", RequestPath: "/v1/responses", RequestModel: "gpt-5",
		ModelRoutingFamily: "codex", ModelRoutingVersion: 7, RouteScopeKind: "upstream_group",
		RouteScopeGroupID: 12, RouteScopeGroupName: "gpt-5", RouteMode: "manual_fixed", RouteTargetAccountID: 42,
	})
	parsed := parseSnapshotPayload(t, payload)
	if parsed["request_model"] != "gpt-5" || parsed["model_routing_family"] != "codex" || parsed["route_target_account_id"] != float64(42) {
		t.Fatalf("Codex model-routing scope was not persisted: %#v", parsed)
	}
}

func runeLen(s string) int { return len([]rune(s)) }
