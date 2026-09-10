package proxy

import (
	"context"
	"strings"
	"testing"
	"time"

	"cc-forwarder/internal/proxy/handlers"
	"cc-forwarder/internal/tracking"
)

func TestSubscriptionHTTPTrackingKnownAndUnknownSettlement(t *testing.T) {
	tracker := newSubscriptionUnitTracker(t)
	observer := newSubscriptionTrackingObserver(tracker, nil)
	started := time.Now().Add(-20 * time.Millisecond)

	known := observer.StartHTTP(SubscriptionHTTPStart{
		RequestID: "http-known", StartedAt: started, Method: "POST", Path: "/v1/responses",
		RequestKind: tracking.RequestKindGeneration, IsStreaming: false,
	})
	known.SetModel("synthetic-model")
	known.SetStreaming(true)
	known.SetHTTPStatus(201)
	known.MarkOutbound(41, started.Add(2*time.Millisecond))
	known.MarkFirstOutput(17, started.Add(7*time.Millisecond))
	known.MarkResponseBytes(123)
	known.ApplyParseResult(&ParseResult{
		ModelName: "synthetic-model", IsCompleted: true, Status: "completed",
		HasTokenUsage: true, TokenUsage: &tracking.TokenUsage{},
	})

	detail := waitSubscriptionDetail(t, tracker, "http-known")
	if detail.Status != "completed" || detail.RequestSource != tracking.RequestSourceCodexSubscriptionProxy ||
		detail.RequestKind != tracking.RequestKindGeneration || detail.UpstreamType != "subscription" ||
		detail.UpstreamName != subscriptionTrackingUpstreamName || detail.UpstreamID != 0 {
		t.Fatalf("known dimensions = %+v", detail)
	}
	if !detail.UsageKnown || !detail.CostKnown || detail.HTTPStatusCode == nil || *detail.HTTPStatusCode != 201 {
		t.Fatalf("known state = %+v", detail)
	}
	if detail.FirstTokenMs == nil || detail.CompletionMs == nil {
		t.Fatalf("known timing = first:%v completion:%v", detail.FirstTokenMs, detail.CompletionMs)
	}

	unknown := observer.StartHTTP(SubscriptionHTTPStart{
		RequestID: "http-unknown", StartedAt: time.Now(), Method: "POST", Path: "/v1/responses/compact",
		RequestKind: tracking.RequestKindCompaction,
	})
	unknown.Complete(nil, "SYNTHETIC-SECRET")
	unknownDetail := waitSubscriptionDetail(t, tracker, "http-unknown")
	if unknownDetail.Status != "failed" || unknownDetail.FailureReason != "subscription_failure" ||
		unknownDetail.HTTPStatusCode != nil || unknownDetail.UsageKnown || unknownDetail.CostKnown {
		t.Fatalf("unknown state = %+v", unknownDetail)
	}
	if strings.Contains(unknownDetail.FailureReason, "SECRET") {
		t.Fatalf("unsafe failure persisted: %q", unknownDetail.FailureReason)
	}

	externalError := observer.StartHTTP(SubscriptionHTTPStart{
		RequestID: "http-error", StartedAt: time.Now(), Method: "POST", Path: "/v1/responses",
		RequestKind: tracking.RequestKindGeneration,
	})
	externalError.ApplyParseResult(&ParseResult{
		IsCompleted: true, Status: StatusErrorAPI,
		ErrorInfo: &ErrorInfo{Type: "SYNTHETIC-SECRET", Message: "SYNTHETIC-SECRET"},
	})
	errorDetail := waitSubscriptionDetail(t, tracker, "http-error")
	if errorDetail.FailureReason != "subscription_response_failed" ||
		strings.Contains(errorDetail.FailureReason, "SECRET") || strings.Contains(errorDetail.ModelName, "SECRET") {
		t.Fatalf("unsafe parse error metadata persisted: %+v", errorDetail)
	}

	observer.StartHTTP(SubscriptionHTTPStart{RequestID: "auxiliary", Path: "/v1/models"}).Complete(nil, "")
	time.Sleep(20 * time.Millisecond)
	count, err := tracker.CountRequestDetails(context.Background(), &tracking.QueryOptions{RequestID: "auxiliary", UpstreamType: "all"})
	if err != nil || count != 0 {
		t.Fatalf("auxiliary count = %d, err=%v", count, err)
	}
}

func TestSubscriptionWSObserverCancellationRemovesHandle(t *testing.T) {
	tracker := newSubscriptionUnitTracker(t)
	observer := newSubscriptionTrackingObserver(tracker, nil)
	warmup := &subscriptionSubmission{RequestID: "warmup", Generate: false, StartedAt: time.Now()}
	observer.SubmissionStarted(warmup)

	submission := &subscriptionSubmission{
		RequestID: "ws-cancelled", Generate: true, StartedAt: time.Now(), Model: "synthetic-model",
		PrivacyState: handlers.NewPrivacyRequestState("ws-cancelled"),
	}
	observer.SubmissionStarted(submission)
	observer.SubmissionOutbound(submission, 31, time.Now())
	observer.SubmissionFirstOutput(submission, 11, time.Now())
	observer.SubmissionResponseProgress(submission, 79)
	observer.SubmissionTerminal(submission, &ParseResult{IsCompleted: true, IsCancelled: true, Status: StatusErrorAPI})

	observer.mu.Lock()
	remaining := len(observer.submissions)
	observer.mu.Unlock()
	if remaining != 0 {
		t.Fatalf("settled WS handles = %d, want 0", remaining)
	}
	detail := waitSubscriptionDetail(t, tracker, "ws-cancelled")
	if detail.Status != "cancelled" || detail.FailureReason != "subscription_cancelled" ||
		detail.HTTPStatusCode != nil || detail.UsageKnown || detail.CostKnown {
		t.Fatalf("cancelled WS detail = %+v", detail)
	}
	count, err := tracker.CountRequestDetails(context.Background(), &tracking.QueryOptions{RequestID: "warmup", UpstreamType: "all"})
	if err != nil || count != 0 {
		t.Fatalf("warmup count = %d, err=%v", count, err)
	}
}

func newSubscriptionUnitTracker(t *testing.T) *tracking.UsageTracker {
	t.Helper()
	tracker, err := tracking.NewUsageTracker(&tracking.Config{
		Enabled: true, DatabasePath: ":memory:", BufferSize: 50, BatchSize: 1,
		FlushInterval: time.Hour, MaxRetry: 1, CleanupInterval: time.Hour, RetentionDays: 30,
		HotPool: &tracking.HotPoolSettings{Enabled: false},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tracker.Close() })
	return tracker
}

func waitSubscriptionDetail(t *testing.T, tracker *tracking.UsageTracker, requestID string) tracking.RequestDetail {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		details, err := tracker.QueryRequestDetails(context.Background(), &tracking.QueryOptions{RequestID: requestID, UpstreamType: "all"})
		if err == nil && len(details) == 1 && details[0].Status != "pending" {
			return details[0]
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("request %s did not settle", requestID)
	return tracking.RequestDetail{}
}
