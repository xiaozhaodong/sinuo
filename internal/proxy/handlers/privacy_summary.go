package handlers

import (
	"sync"

	"sinuo/internal/privacy"
)

// RestoreSummary 是日志与请求详情共用的交付统计。只持有固定状态码和计数。
type RestoreSummary struct {
	Status      string `json:"status"`
	Text        int    `json:"text"`
	Tool        int    `json:"tool"`
	UnknownText int    `json:"unknown_text"`
	UnknownTool int    `json:"unknown_tool"`
	Reason      string `json:"reason,omitempty"`
}

type privacySummaryRecorder struct {
	mu       sync.Mutex
	write    func(string)
	scan     privacy.ApplyResult
	policy   *privacy.PolicyError
	count    int
	restore  RestoreSummary
	warnings []string
}

// SetSummaryRecorder 串行合并扫描与交付。等待响应期间只保留允许写进详情的字段。
func (s *PrivacyRequestState) SetSummaryRecorder(write func(string)) {
	s.summary = &privacySummaryRecorder{write: write}
	s.SetResultRecorder(s.summary.recordScan)
}

func (r *privacySummaryRecorder) flushLocked() {
	if payload := buildPrivacyScanJSON(r.scan, r.policy, r.count, r.warnings, r.restore); payload != "" {
		r.write(payload)
	}
}

func (r *privacySummaryRecorder) recordScan(result privacy.ApplyResult, rejection *privacy.PolicyError) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.count++
	r.warnings = privacy.MergeWarnings(r.warnings, privacy.RequestWarnings(result, rejection))
	r.scan = result
	r.scan.Body = nil
	r.scan.Restore = nil
	r.scan.RuleHits = append([]privacy.RuleHit(nil), result.RuleHits...)
	for i := range r.scan.RuleHits {
		r.scan.RuleHits[i].Matches = nil
	}
	r.restore = RestoreSummary{Status: result.RestoreStatus}
	r.policy = nil
	if rejection != nil {
		r.policy = &privacy.PolicyError{Code: rejection.Code}
		r.restore.Status = "not_applicable"
	}
	r.flushLocked()
}

// RecordRestore 合并实际交付的状态和计数。HTTP 与 WebSocket 共用请求详情格式。
func (s *PrivacyRequestState) RecordRestore(delivery RestoreSummary) {
	if s == nil || s.summary == nil {
		return
	}
	r := s.summary
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.restore != delivery {
		r.warnings = privacy.MergeWarnings(r.warnings, privacy.ResponseWarnings(delivery.Status))
		r.restore = delivery
		r.flushLocked()
	}
}

func (r *privacySummaryRecorder) finishPending() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.restore.Status == "pending" {
		r.restore.Status = "interrupted"
		r.flushLocked()
	}
}

func (r *privacySummaryRecorder) resolvePublish(result privacy.ApplyResult, err *privacy.PolicyError) {
	r.mu.Lock()
	defer r.mu.Unlock()
	// 不增加 scan_count：发布失败没有产生第二次扫描。
	r.warnings = privacy.MergeWarnings(r.warnings, privacy.RequestWarnings(result, err))
	r.scan = result
	r.scan.Body, r.scan.Restore = nil, nil
	r.scan.RuleHits = append([]privacy.RuleHit(nil), result.RuleHits...)
	for i := range r.scan.RuleHits {
		r.scan.RuleHits[i].Matches = nil
	}
	r.policy = nil
	if err != nil {
		r.policy = &privacy.PolicyError{Code: err.Code}
	}
	r.restore = RestoreSummary{Status: result.RestoreStatus}
	r.flushLocked()
}
