package tracking

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	timezonepolicy "sinuo/internal/timezone"
)

const maxTrackedIntegrityGapRequests = 1024

// recordIntegrityIssue 将缺失的追踪写入按请求标记，供详情读取及后续持久化。
func (ut *UsageTracker) recordIntegrityIssue(requestID, code string) {
	if ut == nil || strings.TrimSpace(requestID) == "" {
		return
	}
	now := ut.now()
	cleanCode := clampFlowText(code, 64)
	// 若请求仍在热池未提交，尽力把缺口标记并入同一热池对象（不递归触发入队）。
	if ut.hotPoolEnabled && ut.hotPool != nil {
		err := ut.hotPool.Update(requestID, func(req *ActiveRequest) {
			if ApplyRequestFlowEvents(&req.RequestFlow, req.StartTime, now, RequestFlowEvent{
				Kind:             FlowEventMarkIncomplete,
				At:               &now,
				IncompleteReason: cleanCode,
			}) {
				if raw, err := MarshalRequestFlowBounded(req.RequestFlow); err == nil {
					req.RequestFlowJSON = raw
				}
				req.RequestRevision++
			}
		})
		if err == nil {
			return
		}
	}

	ut.gapMu.Lock()
	if ut.pendingGaps == nil {
		ut.pendingGaps = make(map[string]string)
	}
	if _, exists := ut.pendingGaps[requestID]; exists || len(ut.pendingGaps) < maxTrackedIntegrityGapRequests {
		ut.pendingGaps[requestID] = cleanCode
	}
	ut.gapMu.Unlock()
}

func (ut *UsageTracker) getPendingRequestGap(requestID string) string {
	ut.gapMu.RLock()
	defer ut.gapMu.RUnlock()
	return ut.pendingGaps[requestID]
}

func (ut *UsageTracker) recordEventWriteSuccess(requestID, expectedCode string) {
	if expectedCode == "" {
		return
	}
	ut.gapMu.Lock()
	defer ut.gapMu.Unlock()
	if ut.pendingGaps[requestID] == expectedCode {
		delete(ut.pendingGaps, requestID)
	}
}

// applyUpdateOptionsToActiveRequest 在调用方已持有 req.mu 写锁时应用所有 UpdateOptions，
// 仅当持久化可见字段（状态/路径/目标/阶段/计时/用量/诊断）真正变化时才同步刷新 JSON 并递增 RequestRevision。
func applyUpdateOptionsToActiveRequest(req *ActiveRequest, opts UpdateOptions, now time.Time) {
	changed := false

	beforePhases := req.StreamPhases.Clone()
	req.StreamPhases.merge(opts.StreamPhases)
	if !streamPhasesEqual(beforePhases, req.StreamPhases) {
		changed = true
	}
	if req.FirstContentAt == nil && opts.FirstContentAt != nil {
		req.FirstContentAt = phaseCopy(opts.FirstContentAt)
		changed = true
	}

	if opts.EndpointName != nil && req.EndpointName != *opts.EndpointName {
		req.EndpointName = *opts.EndpointName
		changed = true
	}
	if opts.RequestFamily != nil {
		next := normalizeRequestFamily(*opts.RequestFamily)
		if req.RequestFamily != next {
			req.RequestFamily = next
			changed = true
		}
	}
	if opts.UpstreamType != nil && req.UpstreamType != *opts.UpstreamType {
		req.UpstreamType = *opts.UpstreamType
		changed = true
	}
	if opts.UpstreamSourceName != nil && req.UpstreamSourceName != *opts.UpstreamSourceName {
		req.UpstreamSourceName = *opts.UpstreamSourceName
		changed = true
	}
	if opts.UpstreamName != nil && req.UpstreamName != *opts.UpstreamName {
		req.UpstreamName = *opts.UpstreamName
		changed = true
	}
	if opts.UpstreamID != nil && req.UpstreamID != *opts.UpstreamID {
		req.UpstreamID = *opts.UpstreamID
		changed = true
	}
	if opts.RequestSource != nil && req.RequestSource != *opts.RequestSource {
		req.RequestSource = *opts.RequestSource
		changed = true
	}
	if opts.RequestKind != nil && req.RequestKind != *opts.RequestKind {
		req.RequestKind = *opts.RequestKind
		changed = true
	}
	if opts.IsStreaming != nil && req.IsStreaming != *opts.IsStreaming {
		req.IsStreaming = *opts.IsStreaming
		changed = true
	}
	if opts.RouteMode != nil && req.RouteMode != *opts.RouteMode {
		req.RouteMode = *opts.RouteMode
		changed = true
	}
	if opts.RequestedEndpoint != nil && req.RequestedEndpoint != *opts.RequestedEndpoint {
		req.RequestedEndpoint = *opts.RequestedEndpoint
		changed = true
	}
	if opts.EffectiveEndpoint != nil && req.EffectiveEndpoint != *opts.EffectiveEndpoint {
		req.EffectiveEndpoint = *opts.EffectiveEndpoint
		changed = true
	}
	if opts.FallbackReason != nil && req.FallbackReason != *opts.FallbackReason {
		req.FallbackReason = *opts.FallbackReason
		changed = true
	}
	if opts.RouteDecisionAt != nil {
		decisionAt := *opts.RouteDecisionAt
		if req.RouteDecisionAt == nil || !req.RouteDecisionAt.Equal(decisionAt) {
			req.RouteDecisionAt = &decisionAt
			changed = true
		}
	}
	if opts.Status != nil && req.Status != *opts.Status {
		req.Status = *opts.Status
		changed = true
	}
	if opts.RetryCount != nil && req.RetryCount != *opts.RetryCount {
		req.RetryCount = *opts.RetryCount
		changed = true
	}
	if opts.HttpStatus != nil && (!req.HTTPStatusKnown || req.HTTPStatus != *opts.HttpStatus) {
		req.HTTPStatus = *opts.HttpStatus
		req.HTTPStatusKnown = true
		changed = true
	}
	if opts.ModelName != nil && req.ModelName != *opts.ModelName {
		req.ModelName = *opts.ModelName
		changed = true
	}
	if req.FirstTokenMs == nil {
		if req.UpstreamFirstDispatchMs == nil && opts.UpstreamFirstDispatchMs != nil && *opts.UpstreamFirstDispatchMs >= 0 {
			value := *opts.UpstreamFirstDispatchMs
			req.UpstreamFirstDispatchMs = &value
			changed = true
		}
		if opts.UpstreamWriteMs != nil && *opts.UpstreamWriteMs >= 0 {
			value := *opts.UpstreamWriteMs
			if req.UpstreamWriteMs == nil || *req.UpstreamWriteMs != value {
				req.UpstreamWriteMs = &value
				changed = true
			}
		}
		if opts.UpstreamDispatchMs != nil && *opts.UpstreamDispatchMs >= 0 {
			value := *opts.UpstreamDispatchMs
			if req.UpstreamDispatchMs == nil || *req.UpstreamDispatchMs != value {
				req.UpstreamDispatchMs = &value
				changed = true
			}
		}
		if opts.UpstreamConnMs != nil && *opts.UpstreamConnMs >= 0 {
			value := *opts.UpstreamConnMs
			if req.UpstreamConnMs == nil || *req.UpstreamConnMs != value {
				req.UpstreamConnMs = &value
				changed = true
			}
		}
	}
	if opts.FirstTokenStartAt != nil && !opts.FirstTokenStartAt.IsZero() && req.FirstTokenMs == nil {
		startAt := *opts.FirstTokenStartAt
		if req.FirstTokenStartAt == nil || !req.FirstTokenStartAt.Equal(startAt) {
			req.FirstTokenStartAt = &startAt
			changed = true
		}
	}
	if opts.FirstTokenMs != nil && *opts.FirstTokenMs >= 0 && req.FirstTokenMs == nil {
		firstTokenMs := *opts.FirstTokenMs
		req.FirstTokenMs = &firstTokenMs
		if opts.FirstTokenAt != nil && !opts.FirstTokenAt.IsZero() {
			firstTokenAt := *opts.FirstTokenAt
			req.FirstTokenAt = &firstTokenAt
		}
		changed = true
	}
	if opts.CompletionMs != nil && *opts.CompletionMs >= 0 && req.CompletionMs == nil {
		completionMs := *opts.CompletionMs
		req.CompletionMs = &completionMs
		changed = true
	}
	if opts.FailureReason != nil && req.FailureReason != *opts.FailureReason {
		req.FailureReason = *opts.FailureReason
		changed = true
	}
	if opts.ScheduleSnapshotJSON != nil && req.ScheduleSnapshotJSON != *opts.ScheduleSnapshotJSON {
		req.ScheduleSnapshotJSON = *opts.ScheduleSnapshotJSON
		changed = true
	}
	if opts.PrivacyScanJSON != nil && req.PrivacyScanJSON != *opts.PrivacyScanJSON {
		req.PrivacyScanJSON = *opts.PrivacyScanJSON
		changed = true
	}
	if opts.UpstreamModel != nil {
		next := strings.TrimSpace(*opts.UpstreamModel)
		if req.UpstreamModel != next {
			req.UpstreamModel = next
			changed = true
		}
	}
	if opts.ResponseModel != nil && strings.TrimSpace(*opts.ResponseModel) != "" {
		next := strings.TrimSpace(*opts.ResponseModel)
		if req.ResponseModel != next {
			req.ResponseModel = next
			changed = true
		}
	}
	if opts.ResponseReasoning != nil && strings.TrimSpace(*opts.ResponseReasoning) != "" {
		next := strings.TrimSpace(*opts.ResponseReasoning)
		if req.ResponseReasoning != next {
			req.ResponseReasoning = next
			changed = true
		}
	}
	if opts.RequestModel != nil && strings.TrimSpace(*opts.RequestModel) != "" {
		next := strings.TrimSpace(*opts.RequestModel)
		if req.RequestModel != next {
			req.RequestModel = next
			changed = true
		}
	}
	if opts.RequestReasoning != nil && strings.TrimSpace(*opts.RequestReasoning) != "" {
		next := strings.TrimSpace(*opts.RequestReasoning)
		if req.RequestReasoning != next {
			req.RequestReasoning = next
			changed = true
		}
	}

	if len(opts.FlowEvents) > 0 {
		if ApplyRequestFlowEvents(&req.RequestFlow, req.StartTime, now, opts.FlowEvents...) {
			if raw, err := MarshalRequestFlowBounded(req.RequestFlow); err == nil {
				req.RequestFlowJSON = raw
			}
			changed = true
		}
	}

	if changed {
		if req.RequestRevision < 1 {
			req.RequestRevision = 1
		}
		req.RequestRevision++
	}
}

func streamPhasesEqual(a, b StreamPhases) bool {
	return equalInt64Ptr(a.FirstContentMs, b.FirstContentMs) &&
		equalInt64Ptr(a.FirstTextMs, b.FirstTextMs) &&
		equalInt64Ptr(a.FirstReasoningMs, b.FirstReasoningMs) &&
		equalInt64Ptr(a.StreamFlowMs, b.StreamFlowMs) &&
		equalBoolPtr(a.ReasoningHidden, b.ReasoningHidden) &&
		equalStringPtr(a.StopReason, b.StopReason) &&
		equalStringPtr(a.UpstreamRequestID, b.UpstreamRequestID)
}

func equalInt64Ptr(a, b *int64) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

func equalBoolPtr(a, b *bool) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

func equalStringPtr(a, b *string) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

// applyFlowEventsInSQLiteTx 在单写事务内读-改-写 request_flow_json 并严格递增 request_revision。
func (ut *UsageTracker) applyFlowEventsInSQLiteTx(ctx context.Context, tx *sql.Tx, requestID string, events []RequestFlowEvent, gapCode string) (gapStored bool, err error) {
	if tx == nil || strings.TrimSpace(requestID) == "" {
		return false, nil
	}
	if len(events) == 0 && strings.TrimSpace(gapCode) == "" {
		return false, nil
	}

	var startRaw, existingJSON sql.NullString
	var currentRev sql.NullInt64
	err = tx.QueryRowContext(ctx, `
		SELECT CAST(start_time AS TEXT), request_flow_json, request_revision
		FROM request_logs WHERE request_id = ?`, requestID).Scan(&startRaw, &existingJSON, &currentRev)
	if err != nil {
		if err == sql.ErrNoRows {
			return false, nil
		}
		return false, fmt.Errorf("read request_flow_json for update: %w", err)
	}

	startTime := ut.now()
	if startRaw.Valid && strings.TrimSpace(startRaw.String) != "" {
		if parsed, parseErr := timezonepolicy.ParseStorage(startRaw.String); parseErr == nil {
			startTime = parsed
		}
	}

	var flow *RequestFlow
	if existingJSON.Valid && strings.TrimSpace(existingJSON.String) != "" {
		parsed, integrity := ParseRequestFlowWhitelist(existingJSON.String, true)
		if integrity.CorruptFlowJSON || integrity.UnsupportedSchema {
			// 旧值损坏或版本不支持：保留原值不被覆盖为推测路径，仅推进 revision。
			_, updErr := tx.ExecContext(ctx, fmt.Sprintf(`UPDATE request_logs SET request_revision = COALESCE(request_revision, 0) + 1, updated_at = %s WHERE request_id = ?`, ut.adapter.BuildUTCDateTimeNow()), requestID)
			return false, updErr
		}
		flow = parsed
	}

	mergedEvents := make([]RequestFlowEvent, 0, len(events)+1)
	mergedEvents = append(mergedEvents, events...)
	if strings.TrimSpace(gapCode) != "" {
		now := ut.now()
		mergedEvents = append(mergedEvents, RequestFlowEvent{
			Kind:             FlowEventMarkIncomplete,
			At:               &now,
			IncompleteReason: gapCode,
		})
	}

	changed := ApplyRequestFlowEvents(&flow, startTime, ut.now(), mergedEvents...)
	gapStored = gapCode != "" && flow != nil && strings.Contains(flow.IncompleteReason, gapCode)
	if !changed {
		return gapStored, nil
	}

	encoded, err := MarshalRequestFlowBounded(flow)
	if err != nil {
		return false, fmt.Errorf("marshal request_flow_json: %w", err)
	}

	nextRev := currentRev.Int64 + 1
	if nextRev < 1 {
		nextRev = 1
	}

	_, updErr := tx.ExecContext(ctx, fmt.Sprintf(`UPDATE request_logs SET request_flow_json = ?, request_revision = CASE WHEN COALESCE(request_revision, 0) < ? THEN ? ELSE request_revision + 1 END, updated_at = %s WHERE request_id = ?`, ut.adapter.BuildUTCDateTimeNow()),
		encoded, nextRev, nextRev, requestID)
	return gapStored && updErr == nil, updErr
}
