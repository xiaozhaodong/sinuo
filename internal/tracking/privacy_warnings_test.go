package tracking

import (
	"context"
	"reflect"
	"testing"
	"time"

	"sinuo/internal/privacy"
)

func TestPrivacyWarningsHotPoolArchiveAndDatabase(t *testing.T) {
	for _, hot := range []bool{true, false} {
		t.Run(map[bool]string{true: "hot", false: "database"}[hot], func(t *testing.T) {
			tracker := newRequestLifecycleTestTracker(t, hot, 10*time.Millisecond)
			id := "warning-request"
			tracker.RecordRequestStart(id, "127.0.0.1", "synthetic", "POST", "/v1/responses", true)
			payload := `{"action":"redact","restore":{"status":"completed"},"warnings":["request_fail_open","response_failed"],"failure_reason":"SYNTHETIC-PRIVATE"}`
			tracker.RecordRequestUpdate(id, UpdateOptions{PrivacyScanJSON: &payload})
			want := []string{privacy.WarningRequestFailOpen, privacy.WarningResponseFailed}
			check := func(database bool) {
				t.Helper()
				deadline := time.Now().Add(3 * time.Second)
				for time.Now().Before(deadline) {
					rows, _, err := tracker.QueryRequestDetailsWithHotPool(context.Background(), &QueryOptions{RequestID: id})
					data, detailErr := tracker.GetRequestLifecycleData(context.Background(), id)
					if err == nil && detailErr == nil && len(rows) == 1 && data != nil && reflect.DeepEqual(rows[0].PrivacyWarnings, want) && reflect.DeepEqual(data.Detail.PrivacyWarnings, want) && (!database || data.Source == "database") {
						return
					}
					time.Sleep(10 * time.Millisecond)
				}
				t.Fatal("列表/详情未保留统一异常码")
			}
			check(false)
			tracker.RecordRequestSuccess(id, "synthetic", nil, time.Millisecond)
			check(false)
			check(true)
		})
	}
}
