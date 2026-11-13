package main

import (
	"encoding/json"
	"strings"
	"testing"

	"sinuo/internal/tracking"
)

func TestRequestRecordPreservesPrivacyWarnings(t *testing.T) {
	row := requestDetailToRecord(tracking.RequestDetail{RequestID: "notification-request", PrivacyWarnings: []string{"request_fail_open", "response_bypassed"}})
	data, err := json.Marshal(row)
	if err != nil || !strings.Contains(string(data), `"privacy_warnings":["request_fail_open","response_bypassed"]`) {
		t.Fatalf("data=%s err=%v", data, err)
	}
	plain, _ := json.Marshal(requestDetailToRecord(tracking.RequestDetail{}))
	if strings.Contains(string(plain), "privacy_warnings") {
		t.Fatal("正常请求不应伪造异常")
	}
}
