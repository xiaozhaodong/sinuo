package proxy

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"

	"sinuo/internal/events"
	"sinuo/internal/privacy"
	"sinuo/internal/proxy/handlers"
	"sinuo/internal/tracking"
)

type privacyNoticeFailOpenFilter struct{}

func (privacyNoticeFailOpenFilter) SnapshotVersion() int64 { return 1 }
func (privacyNoticeFailOpenFilter) Apply(_ context.Context, _ privacy.Request, body []byte) (privacy.ApplyResult, error) {
	return privacy.ApplyResult{Body: body, Action: privacy.ModeRedact, FailOpen: true,
		SkippedReason: privacy.SkippedFailOpen, RestoreStatus: "bypassed"}, nil
}

func TestPrivacyNotificationsRegularHTTPEntry(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp-synthetic","model":"synthetic-model","output":[],"usage":{"input_tokens":1,"output_tokens":1}}`))
	}))
	defer upstream.Close()
	tracker := newLifecyclePanelTestTracker(t)
	handler := newSessionTestHandler(t, upstream.URL, tracker)
	bus := &privacyNoticeTestBus{}
	handler.SetEventBus(bus)
	handler.SetPrivacyFilter(privacyNoticeFailOpenFilter{})
	req := sessionTestRequest("/v1/responses", "regular-notice", "Session_id", false)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	bus.mu.Lock()
	defer bus.mu.Unlock()
	if len(bus.notices) != 1 || !reflect.DeepEqual(bus.notices[0].Data["warning_codes"], []string{privacy.WarningRequestFailOpen}) {
		t.Fatalf("请求放行不应误报响应回填失败: %+v", bus.notices)
	}
	rows, _, err := tracker.QueryRequestDetailsWithHotPool(context.Background(), &tracking.QueryOptions{RequestID: "regular-notice"})
	if err != nil || len(rows) != 1 || !reflect.DeepEqual(rows[0].PrivacyWarnings, []string{privacy.WarningRequestFailOpen}) {
		t.Fatalf("rows=%+v err=%v", rows, err)
	}
}

type privacyNoticeTestBus struct {
	events.EventBus
	mu      sync.Mutex
	notices []events.Event
	updates int
}

func (b *privacyNoticeTestBus) Publish(event events.Event) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if event.Type == events.EventPrivacyWarning {
		b.notices = append(b.notices, event)
	}
	if event.Data["change_type"] == "privacy_warning" {
		b.updates++
	}
}

func TestPrivacyNotificationsDeduplicateAndExcludeRawDetails(t *testing.T) {
	bus := &privacyNoticeTestBus{}
	write := newPrivacySummaryWriter(nil, bus, "warning-request")
	var wg sync.WaitGroup
	for n := 0; n < 80; n++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			write(`{"fail_open":true,"failure_reason":"SYNTHETIC-PRIVATE","restore":{"status":"bypassed"}}`)
		}()
	}
	wg.Wait()
	write(`{"warnings":["request_fail_open","response_failed"]}`)
	write(`{"warnings":["request_fail_open","response_failed"]}`)
	if len(bus.notices) != 2 || bus.updates != 2 {
		t.Fatalf("重复通知: %+v", bus)
	}
	for _, event := range bus.notices {
		raw, _ := json.Marshal(event.Data)
		if len(event.Data) != 3 || strings.Contains(string(raw), "SYNTHETIC-PRIVATE") {
			t.Fatal(string(raw))
		}
	}
}

func TestPrivacyNotificationsSubscriptionHTTPAndWSShareRecorder(t *testing.T) {
	for _, ws := range []bool{false, true} {
		t.Run(map[bool]string{false: "http", true: "ws"}[ws], func(t *testing.T) {
			tracker := newLifecyclePanelTestTracker(t)
			bus := &privacyNoticeTestBus{}
			observer := newSubscriptionTrackingObserver(tracker, bus)
			state := handlers.NewPrivacyRequestState("notification-request")
			if ws {
				sub := &subscriptionSubmission{RequestID: "notification-request", Generate: true, PrivacyState: state}
				observer.SubmissionStarted(sub)
				observer.BindPrivacyState(state, sub)
			} else {
				handle := observer.StartHTTP(SubscriptionHTTPStart{RequestID: "notification-request", Path: "/v1/responses", Method: "POST", RequestKind: tracking.RequestKindGeneration})
				state.SetSummaryRecorder(handle.recordPrivacySummary)
			}
			state.RecordRestore(handlers.RestoreSummary{Status: "bypassed"})
			state.RecordRestore(handlers.RestoreSummary{Status: "bypassed"})
			rows, _, err := tracker.QueryRequestDetailsWithHotPool(context.Background(), &tracking.QueryOptions{RequestID: "notification-request"})
			if err != nil || len(rows) != 1 || !reflect.DeepEqual(rows[0].PrivacyWarnings, []string{privacy.WarningResponseBypassed}) {
				t.Fatalf("rows=%+v err=%v", rows, err)
			}
			if len(bus.notices) != 1 {
				t.Fatal("未发送唯一通知", len(bus.notices))
			}
		})
	}
}

func TestPrivacyNotificationsWithoutRequestTracking(t *testing.T) {
	bus := &privacyNoticeTestBus{}
	observer := newSubscriptionTrackingObserver(nil, bus)
	handle := observer.StartHTTP(SubscriptionHTTPStart{RequestID: "untracked-notice", RequestKind: tracking.RequestKindGeneration})
	state := handlers.NewPrivacyRequestState("untracked-notice")
	state.SetSummaryRecorder(handle.recordPrivacySummary)
	state.RecordRestore(handlers.RestoreSummary{Status: "failed"})
	if len(bus.notices) != 1 || handle.active {
		t.Fatal("关闭追踪不应关闭隐私通知")
	}
}
