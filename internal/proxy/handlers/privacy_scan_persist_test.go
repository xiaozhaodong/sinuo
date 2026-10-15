package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"sinuo/internal/privacy"
)

// TestBuildPrivacyScanJSON_Actions 四分支：redact/detect/blocked/disabled。
func TestBuildPrivacyScanJSON_Actions(t *testing.T) {
	result := privacy.ApplyResult{
		Action:       privacy.ModeRedact,
		HitCount:     2,
		RuleHits:     []privacy.RuleHit{{RuleName: "中国手机号", Count: 1}, {RuleName: "银行卡号", Count: 1}},
		ScanDuration: 3 * time.Millisecond,
	}

	payload := BuildPrivacyScanJSON(result, nil, 1)
	if payload == "" {
		t.Fatal("expected payload for redact action")
	}
	var parsed map[string]interface{}
	if err := json.Unmarshal([]byte(payload), &parsed); err != nil {
		t.Fatalf("failed to unmarshal payload: %v", err)
	}
	if parsed["action"] != "redact" || parsed["hit_count"] != float64(2) || parsed["scan_count"] != float64(1) {
		t.Fatalf("unexpected payload: %s", payload)
	}
	if parsed["scan_ms"] != float64(3) {
		t.Fatalf("expected scan_ms 3, got %v", parsed["scan_ms"])
	}

	if payload := BuildPrivacyScanJSON(privacy.ApplyResult{Action: privacy.ModeDetect, HitCount: 1}, nil, 2); payload == "" {
		t.Fatal("expected payload for detect action")
	}

	blocked := &privacy.PolicyError{StatusCode: http.StatusRequestEntityTooLarge, Code: privacy.CodeScanBodyTooLarge}
	blockedPayload := BuildPrivacyScanJSON(result, blocked, 1)
	var blockedParsed map[string]interface{}
	if err := json.Unmarshal([]byte(blockedPayload), &blockedParsed); err != nil {
		t.Fatalf("failed to unmarshal blocked payload: %v", err)
	}
	if blockedParsed["action"] != "blocked" || blockedParsed["blocked_code"] != privacy.CodeScanBodyTooLarge {
		t.Fatalf("unexpected blocked payload: %s", blockedPayload)
	}

	if payload := BuildPrivacyScanJSON(privacy.ApplyResult{Action: privacy.ModeDisabled}, nil, 1); payload != "" {
		t.Fatalf("expected empty payload for disabled action, got %q", payload)
	}
	if payload := BuildPrivacyScanJSON(result, nil, 0); payload != "" {
		t.Fatalf("expected empty payload for scanCount 0, got %q", payload)
	}
}

// fakeScanPersistFilter 真实 Apply 计数 + 可配置 block 行为。
type fakeScanPersistFilter struct {
	applyCount     int32
	snapshotVer    int64
	blockCode      string
	blockOnRequest bool
}

func (f *fakeScanPersistFilter) Apply(_ context.Context, _ privacy.Request, body []byte) (privacy.ApplyResult, error) {
	atomic.AddInt32(&f.applyCount, 1)
	result := privacy.ApplyResult{
		Body:     body,
		Action:   privacy.ModeRedact,
		HitCount: 1,
		RuleHits: []privacy.RuleHit{{RuleName: "中国手机号", Count: 1}},
		Changed:  f.blockOnRequest,
	}
	if f.blockCode != "" && f.blockOnRequest {
		return result, &privacy.PolicyError{StatusCode: http.StatusBadRequest, Code: f.blockCode, Result: result}
	}
	return result, nil
}

func (f *fakeScanPersistFilter) SnapshotVersion() int64 { return f.snapshotVer }

func privacyTestRequest(t *testing.T, state *PrivacyRequestState) *http.Request {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	if state != nil {
		req = req.WithContext(WithPrivacyRequestState(req.Context(), state))
	}
	return req
}

// TestApplyPrivacyFilter_NotifiesOnlyOnRealApply cache hit 不触发 recorder，不同 body 重扫触发。
func TestApplyPrivacyFilter_NotifiesOnlyOnRealApply(t *testing.T) {
	filter := &fakeScanPersistFilter{snapshotVer: 1}
	state := NewPrivacyRequestState("req-scan")
	req := privacyTestRequest(t, state)

	var notified int32
	state.SetResultRecorder(func(privacy.ApplyResult, *privacy.PolicyError) {
		atomic.AddInt32(&notified, 1)
	})

	privacyReq := privacy.Request{RequestID: "req-scan", Path: "/v1/messages", UpstreamType: "endpoint"}

	if _, err := ApplyPrivacyFilter(filter, req, privacyReq, []byte("hello")); err != nil {
		t.Fatalf("apply failed: %v", err)
	}
	if _, err := ApplyPrivacyFilter(filter, req, privacyReq, []byte("hello")); err != nil {
		t.Fatalf("apply failed: %v", err)
	}
	if got := atomic.LoadInt32(&notified); got != 1 {
		t.Fatalf("expected 1 notify (cache hit skipped), got %d", got)
	}
	if _, err := ApplyPrivacyFilter(filter, req, privacyReq, []byte("world")); err != nil {
		t.Fatalf("apply failed: %v", err)
	}
	if got := atomic.LoadInt32(&notified); got != 2 {
		t.Fatalf("expected 2 notify after rescan, got %d", got)
	}
	if got := atomic.LoadInt32(&filter.applyCount); got != 2 {
		t.Fatalf("expected 2 real applies, got %d", got)
	}
}

// TestApplyPrivacyFilter_NotifiesPolicyError policyErr 短路时 recorder 收到 *PolicyError。
func TestApplyPrivacyFilter_NotifiesPolicyError(t *testing.T) {
	filter := &fakeScanPersistFilter{snapshotVer: 1, blockCode: privacy.CodeScanBodyTooLarge, blockOnRequest: true}
	state := NewPrivacyRequestState("req-block")
	req := privacyTestRequest(t, state)

	var gotErr *privacy.PolicyError
	state.SetResultRecorder(func(_ privacy.ApplyResult, policyErr *privacy.PolicyError) {
		gotErr = policyErr
	})

	privacyReq := privacy.Request{RequestID: "req-block", Path: "/v1/messages", UpstreamType: "endpoint"}
	_, err := ApplyPrivacyFilter(filter, req, privacyReq, []byte("blocked"))
	if err == nil {
		t.Fatal("expected policy error")
	}
	if gotErr == nil || gotErr.Code != privacy.CodeScanBodyTooLarge {
		t.Fatalf("expected recorder to receive policy error, got %v", gotErr)
	}
}
