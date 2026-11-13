package privacy

import (
	"reflect"
	"testing"
)

func TestPrivacyWarningsClassification(t *testing.T) {
	for _, tc := range []struct {
		name, payload string
		want          []string
	}{
		{"request-open", `{"fail_open":true,"restore":{"status":"bypassed"}}`, []string{WarningRequestFailOpen}},
		{"legacy-open", `{"skipped_reason":"privacy_scan_failed_fail_open"}`, []string{WarningRequestFailOpen}},
		{"over-limit", `{"skipped_reason":"privacy_scan_over_limit_fail_open"}`, []string{WarningRequestFailOpen}},
		{"request-closed", `{"action":"blocked","blocked_code":"privacy_scan_failed"}`, []string{WarningRequestFailed}},
		{"response-open", `{"restore":{"status":"bypassed"}}`, []string{WarningResponseBypassed}},
		{"response-closed", `{"restore":{"status":"failed"}}`, []string{WarningResponseFailed}},
		{"two-independent", `{"fail_open":true,"restore":{"status":"failed"}}`, []string{WarningRequestFailOpen, WarningResponseFailed}},
		{"sticky", `{"action":"redact","changed":true,"restore":{"status":"completed"},"warnings":["response_failed","request_fail_open","request_fail_open","SECRET"]}`, []string{WarningRequestFailOpen, WarningResponseFailed}},
		{"explicit-empty", `{"fail_open":true,"warnings":[]}`, nil},
		{"normal-block", `{"action":"blocked","blocked_code":"privacy_blocked"}`, nil},
		{"invalid-request", `{"action":"blocked","blocked_code":"privacy_invalid_request"}`, nil},
		{"upstream", `{"restore":{"status":"upstream_error"}}`, nil},
		{"disconnect", `{"restore":{"status":"interrupted"}}`, nil},
		{"disabled", `{"restore":{"status":"disabled"}}`, nil},
		{"unknown-token", `{"restore":{"status":"completed","unknown_text":1}}`, nil},
		{"invalid-json", `{"warnings":`, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := WarningsFromSummary(tc.payload); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}
