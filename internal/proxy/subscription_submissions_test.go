package proxy

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"cc-forwarder/internal/privacy"
)

type submissionEvent struct {
	kind            string
	requestID       string
	responseID      string
	generate        bool
	serverInitiated bool
	status          string
}

type recordingSubmissionObserver struct {
	mu       sync.Mutex
	events   []submissionEvent
	warnings []string
}

func (o *recordingSubmissionObserver) append(kind string, s *subscriptionSubmission, status string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.events = append(o.events, submissionEvent{
		kind: kind, requestID: s.RequestID, responseID: s.ResponseID,
		generate: s.Generate, serverInitiated: s.ServerInitiated, status: status,
	})
}

func (o *recordingSubmissionObserver) SubmissionStarted(s *subscriptionSubmission) {
	o.append("started", s, "")
}
func (o *recordingSubmissionObserver) SubmissionBound(s *subscriptionSubmission) {
	o.append("bound", s, "")
}
func (o *recordingSubmissionObserver) SubmissionTerminal(s *subscriptionSubmission, result *ParseResult) {
	status := ""
	if result != nil {
		status = result.Status
	}
	o.append("terminal", s, status)
}
func (o *recordingSubmissionObserver) SubmissionRejected(s *subscriptionSubmission, _ *privacy.PolicyError) {
	o.append("rejected", s, "")
}
func (o *recordingSubmissionObserver) SubmissionInterrupted(s *subscriptionSubmission, reason string) {
	o.append("interrupted", s, reason)
}
func (o *recordingSubmissionObserver) SubmissionWarning(_ string, code string) {
	o.mu.Lock()
	o.warnings = append(o.warnings, code)
	o.mu.Unlock()
}

func (o *recordingSubmissionObserver) count(kind string) int {
	o.mu.Lock()
	defer o.mu.Unlock()
	count := 0
	for _, event := range o.events {
		if event.kind == kind {
			count++
		}
	}
	return count
}

func TestSubscriptionSubmissionsBindsFIFOAndResponseID(t *testing.T) {
	observer := &recordingSubmissionObserver{}
	state := newSubscriptionSubmissionState(context.Background(), "ws-1", observer)
	first, ok := state.beginCreate([]byte(`{"type":"response.create","model":"m1","input":"one"}`))
	if !ok || first == nil {
		t.Fatal("first create not recognized")
	}
	second, ok := state.beginCreate([]byte(`{"type":"response.create","model":"m2","input":"two"}`))
	if !ok || second == nil {
		t.Fatal("second create not recognized")
	}
	state.handleUpstream([]byte(`{"type":"response.created","response":{"id":"resp-1","model":"m1"}}`))
	state.handleUpstream([]byte(`{"type":"response.created","response":{"id":"resp-2","model":"m2"}}`))
	state.handleUpstream([]byte(`{"type":"response.completed","response":{"id":"resp-2","model":"m2","usage":{"input_tokens":2,"output_tokens":1}}}`))
	state.handleUpstream([]byte(`{"type":"response.completed","response":{"id":"resp-1","model":"m1","usage":{"input_tokens":1,"output_tokens":1}}}`))

	if first.ResponseID != "resp-1" || second.ResponseID != "resp-2" {
		t.Fatalf("FIFO binding mismatch: first=%q second=%q", first.ResponseID, second.ResponseID)
	}
	if observer.count("terminal") != 2 {
		t.Fatalf("terminal count = %d, want 2", observer.count("terminal"))
	}
	if len(state.active) != 0 || len(state.pending) != 0 || len(state.byResponseID) != 0 {
		t.Fatalf("settled state retained: active=%d pending=%d ids=%d", len(state.active), len(state.pending), len(state.byResponseID))
	}
}

func TestSubscriptionSubmissionsDuplicateCreatedDoesNotConsumePendingOrCreateSuccessor(t *testing.T) {
	observer := &recordingSubmissionObserver{}
	state := newSubscriptionSubmissionState(context.Background(), "ws-dup", observer)
	first, _ := state.beginCreate([]byte(`{"type":"response.create","input":"one"}`))
	second, _ := state.beginCreate([]byte(`{"type":"response.create","input":"two"}`))
	created := []byte(`{"type":"response.created","response":{"id":"resp-dup"}}`)
	state.handleUpstream(created)
	state.handleUpstream(created)
	if second.ResponseID != "" || len(state.pending) != 1 || state.pending[0] != second {
		t.Fatalf("duplicate created consumed pending: second=%q pending=%d", second.ResponseID, len(state.pending))
	}
	if observer.count("started") != 2 {
		t.Fatalf("duplicate created created successor: starts=%d", observer.count("started"))
	}
	state.handleUpstream([]byte(`{"type":"response.completed","response":{"id":"resp-dup"}}`))
	state.handleUpstream(created) // 已结算 id 仍在有界 recent 集合中，不得创建 successor。
	if observer.count("started") != 2 || first.ResponseID != "resp-dup" {
		t.Fatalf("settled duplicate created changed state: starts=%d first=%q", observer.count("started"), first.ResponseID)
	}
}

func TestSubscriptionSubmissionsCloseAndDuplicateTerminalSettleOnce(t *testing.T) {
	observer := &recordingSubmissionObserver{}
	state := newSubscriptionSubmissionState(context.Background(), "ws-close", observer)
	first, _ := state.beginCreate([]byte(`{"type":"response.create","input":"one"}`))
	second, _ := state.beginCreate([]byte(`{"type":"response.create","input":"two"}`))
	state.handleUpstream([]byte(`{"type":"response.created","response":{"id":"resp-one"}}`))
	terminal := []byte(`{"type":"response.completed","response":{"id":"resp-one","usage":{"input_tokens":1,"output_tokens":1}}}`)
	state.handleUpstream(terminal)
	state.handleUpstream(terminal)
	state.close("socket_closed")
	state.close("socket_closed_again")

	if observer.count("terminal") != 1 || observer.count("interrupted") != 1 {
		t.Fatalf("unexpected settlement counts: terminal=%d interrupted=%d", observer.count("terminal"), observer.count("interrupted"))
	}
	if first.Context != nil || first.PrivacyState != nil || second.Context != nil || second.PrivacyState != nil {
		t.Fatal("settled submissions retained privacy context/cache references")
	}
	if len(state.active) != 0 || len(state.pending) != 0 {
		t.Fatalf("close retained submissions: active=%d pending=%d", len(state.active), len(state.pending))
	}
}

func TestSubscriptionSubmissionsWrappedErrorBeforeCreatedTerminatesPendingFIFO(t *testing.T) {
	observer := &recordingSubmissionObserver{}
	state := newSubscriptionSubmissionState(context.Background(), "ws-error", observer)
	first, _ := state.beginCreate([]byte(`{"type":"response.create","input":"one"}`))
	second, _ := state.beginCreate([]byte(`{"type":"response.create","input":"two"}`))
	state.handleUpstream([]byte(`{"type":"error","status":429,"error":{"type":"rate_limit_error","message":"synthetic"}}`))
	if observer.count("terminal") != 1 || !first.settled || second.settled {
		t.Fatalf("wrapped error did not settle oldest pending once: terminals=%d first=%t second=%t", observer.count("terminal"), first.settled, second.settled)
	}
	state.handleUpstream([]byte(`{"type":"error","status":429,"error":{"type":"rate_limit_error","message":"synthetic"}}`))
	if observer.count("terminal") != 2 || !second.settled {
		t.Fatalf("second wrapped error did not advance FIFO: terminals=%d second=%t", observer.count("terminal"), second.settled)
	}
}

func TestSubscriptionSubmissionsWarmupAndServerSuccessorStayDistinct(t *testing.T) {
	observer := &recordingSubmissionObserver{}
	state := newSubscriptionSubmissionState(context.Background(), "ws-steer", observer)
	warmup, _ := state.beginCreate([]byte(`{"type":"response.create","generate":false,"input":[]}`))
	if warmup.Generate {
		t.Fatal("generate=false must be preserved")
	}
	state.handleUpstream([]byte(`{"type":"response.created","response":{"id":"warmup-id"}}`))
	state.handleUpstream([]byte(`{"type":"response.completed","response":{"id":"warmup-id"}}`))
	state.handleUpstream([]byte(`{"type":"response.created","response":{"id":"successor-id","model":"m-next"}}`))
	state.handleUpstream([]byte(`{"type":"response.incomplete","response":{"id":"successor-id","incomplete_details":{"reason":"steered"}}}`))

	if observer.count("started") != 2 || observer.count("terminal") != 2 {
		t.Fatalf("warmup/successor lifecycle mismatch: starts=%d terminals=%d", observer.count("started"), observer.count("terminal"))
	}
	observer.mu.Lock()
	defer observer.mu.Unlock()
	last := observer.events[len(observer.events)-1]
	if !last.serverInitiated || last.status != "steered" {
		t.Fatalf("server successor terminal mismatch: %+v", last)
	}
}

func TestSubscriptionSubmissionsReleaseHistoryAndBoundRecentIDs(t *testing.T) {
	state := newSubscriptionSubmissionState(context.Background(), "ws-many", nil)
	for n := 0; n < subscriptionRecentResponseIDLimit+40; n++ {
		submission, _ := state.beginCreate([]byte(fmt.Sprintf(`{"type":"response.create","input":"secret-%d"}`, n)))
		id := fmt.Sprintf("resp-%d", n)
		state.handleUpstream([]byte(fmt.Sprintf(`{"type":"response.created","response":{"id":%q}}`, id)))
		state.handleUpstream([]byte(fmt.Sprintf(`{"type":"response.completed","response":{"id":%q}}`, id)))
		if submission.Context != nil || submission.PrivacyState != nil {
			t.Fatalf("round %d retained privacy state", n)
		}
	}
	if len(state.active) != 0 || len(state.pending) != 0 || len(state.byResponseID) != 0 {
		t.Fatalf("settled history retained: active=%d pending=%d ids=%d", len(state.active), len(state.pending), len(state.byResponseID))
	}
	if len(state.recentIDs) != subscriptionRecentResponseIDLimit || len(state.recentOrder) != subscriptionRecentResponseIDLimit {
		t.Fatalf("recent id memory is not bounded: ids=%d order=%d", len(state.recentIDs), len(state.recentOrder))
	}
}
