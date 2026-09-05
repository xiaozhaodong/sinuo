package service

import (
	"reflect"
	"testing"
	"time"
)

func newTestSnapshot(accountID int64, outcome string) *LatestAccountScheduleSnapshot {
	return &LatestAccountScheduleSnapshot{
		RequestID:  "req-matched",
		CapturedAt: time.Now(),
		UpdatedAt:  time.Now(),
		Candidates: []AccountScheduleCandidateDecision{
			{AccountID: accountID, AccountName: "a", Decision: accountScheduleDecisionSelected},
		},
		FinalOutcome: outcome,
	}
}

// TestCompleteSnapshot_PendingHit pending 命中 → matched=true 且返回快照、弹出 pending。
func TestCompleteSnapshot_PendingHit(t *testing.T) {
	store := newLatestAccountScheduleSnapshotStore()
	store.saveDraft(newTestSnapshot(1, ""))

	snapshot, matched := store.complete("req-matched", 1, "a", accountScheduleOutcomeSuccess, "")
	if !matched {
		t.Fatal("expected pending hit to match")
	}
	if snapshot == nil {
		t.Fatal("expected snapshot returned")
	}
	if snapshot.FinalOutcome != accountScheduleOutcomeSuccess {
		t.Fatalf("expected success outcome, got %q", snapshot.FinalOutcome)
	}
	if _, exists := store.pending["req-matched"]; exists {
		t.Fatal("expected pending entry popped after complete")
	}
	if store.latest.RequestID != "req-matched" {
		t.Fatal("expected latest updated")
	}
}

func TestCompleteSnapshot_CodexRoutingErrorsAreTerminal(t *testing.T) {
	outcomes := []string{
		CodexModelRoutingMissingModel,
		CodexModelRoutingUnassigned,
		CodexModelRoutingLoadFailed,
		CodexModelRoutingNoAvailable,
		CodexModelRoutingFixedUnavailable,
	}
	for _, outcome := range outcomes {
		t.Run(outcome, func(t *testing.T) {
			store := newLatestAccountScheduleSnapshotStore()
			snapshot := newTestSnapshot(1, "")
			snapshot.RequestID = "req-" + outcome
			store.saveDraft(snapshot)

			completed, matched := store.complete(snapshot.RequestID, 0, "", outcome, "routing failure")
			if !matched || completed == nil {
				t.Fatalf("expected terminal snapshot update, matched=%v snapshot=%+v", matched, completed)
			}
			if _, exists := store.pending[snapshot.RequestID]; exists {
				t.Fatalf("Codex routing outcome must remove pending snapshot: %q", outcome)
			}
		})
	}
}

// TestCompleteSnapshot_LatestFallbackSameRequestID 无 pending、latest 是同一请求 → matched=true。
func TestCompleteSnapshot_LatestFallbackSameRequestID(t *testing.T) {
	store := newLatestAccountScheduleSnapshotStore()
	store.saveDraft(newTestSnapshot(1, ""))

	// 模拟 pending 已超 TTL 被清理，latest 仍是同一请求。
	store.pending = map[string]*LatestAccountScheduleSnapshot{}

	snapshot, matched := store.complete("req-matched", 1, "a", accountScheduleOutcomeAuthFailed, "bad creds")
	if !matched {
		t.Fatal("expected latest with same request id to match")
	}
	if snapshot == nil {
		t.Fatal("expected snapshot returned")
	}
	if snapshot.FinalOutcome != accountScheduleOutcomeAuthFailed {
		t.Fatalf("expected auth_failed outcome, got %q", snapshot.FinalOutcome)
	}
}

// TestCompleteSnapshot_LatestIsOtherRequest 无 pending、latest 是他人快照 → matched=false 返回 nil；
// 全局 latest 必须逐字段保持不变。
func TestCompleteSnapshot_LatestIsOtherRequest(t *testing.T) {
	store := newLatestAccountScheduleSnapshotStore()
	other := newTestSnapshot(1, "")
	other.RequestID = "req-other"
	store.saveDraft(other)
	before := cloneLatestAccountScheduleSnapshot(store.latest)

	snapshot, matched := store.complete("req-matched", 1, "a", accountScheduleOutcomeSuccess, "")
	if matched {
		t.Fatal("expected other request's latest to not match")
	}
	if snapshot != nil {
		t.Fatal("expected nil snapshot for unmatched request")
	}
	if !reflect.DeepEqual(store.latest, before) {
		t.Fatalf("expected other request latest unchanged, before=%+v after=%+v", before, store.latest)
	}
}

// TestCompleteSnapshot_AttemptSurvivesConcurrentLatest 覆盖 A 首次失败、B 覆盖 latest、
// A 随后成功的交错：A 的 pending 必须保留，B 的快照不能被 A 串改。
func TestCompleteSnapshot_AttemptSurvivesConcurrentLatest(t *testing.T) {
	store := newLatestAccountScheduleSnapshotStore()
	requestA := &LatestAccountScheduleSnapshot{
		RequestID:  "req-a",
		CapturedAt: time.Now(),
		UpdatedAt:  time.Now(),
		Candidates: []AccountScheduleCandidateDecision{
			{AccountID: 1, AccountName: "a1", Decision: accountScheduleDecisionSelected},
			{AccountID: 2, AccountName: "a2", Decision: accountScheduleDecisionEligible},
		},
	}
	store.saveDraft(requestA)

	if snapshot, matched := store.complete("req-a", 1, "a1", accountScheduleOutcomeTransientFailure, "retryable"); !matched || snapshot == nil {
		t.Fatalf("expected request A attempt update, matched=%v snapshot=%+v", matched, snapshot)
	}
	if _, exists := store.pending["req-a"]; !exists {
		t.Fatal("request A pending must survive a non-terminal attempt")
	}

	requestB := newTestSnapshot(9, "")
	requestB.RequestID = "req-b"
	requestB.SelectedAccountID = 9
	requestB.SelectedAccountName = "b9"
	store.saveDraft(requestB)
	requestBBefore := cloneLatestAccountScheduleSnapshot(store.pending["req-b"])

	finalA, matched := store.complete("req-a", 2, "a2", accountScheduleOutcomeSuccess, "")
	if !matched || finalA == nil {
		t.Fatalf("expected request A terminal update, matched=%v snapshot=%+v", matched, finalA)
	}
	if finalA.RequestID != "req-a" || finalA.FinalOutcome != accountScheduleOutcomeSuccess {
		t.Fatalf("unexpected request A final snapshot: %+v", finalA)
	}
	if finalA.FinalError != "" {
		t.Fatalf("successful terminal outcome must clear prior attempt error, got %q", finalA.FinalError)
	}
	if _, exists := store.pending["req-a"]; exists {
		t.Fatal("request A pending must be removed at terminal success")
	}
	if !reflect.DeepEqual(store.pending["req-b"], requestBBefore) {
		t.Fatalf("request B pending was modified, before=%+v after=%+v", requestBBefore, store.pending["req-b"])
	}
	if store.latest == nil || store.latest.RequestID != "req-a" {
		t.Fatalf("latest should reflect the most recently updated request A, got %+v", store.latest)
	}

	candidate1 := mustFindCandidateDecision(t, finalA, 1)
	if candidate1.RuntimeOutcome != accountScheduleOutcomeTransientFailure {
		t.Fatalf("expected first attempt retained, got %+v", candidate1)
	}
	candidate2 := mustFindCandidateDecision(t, finalA, 2)
	if candidate2.RuntimeOutcome != accountScheduleOutcomeSuccess {
		t.Fatalf("expected terminal success recorded, got %+v", candidate2)
	}
}
