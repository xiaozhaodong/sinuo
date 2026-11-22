// request_lifecycle.go
// 请求生命周期详情读取：热池安全快照 + DB 单行查询的统一出口。
// 分段计时、调度快照与隐私摘要列
// 首次取连接、写完与内容时间同时进入列表；思考信号与上游请求ID仅详情返回。

package tracking

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// RequestLifecycleData 弹窗权威详情数据。
// Detail 复用列表行映射；此结构补足完整阶段字段、调度、隐私摘要与路径完整性。
type RequestLifecycleData struct {
	StreamPhases
	OfficialRecalculation   *OfficialRecalculationAvailability
	BillingMultipliers      *AppliedBillingMultipliers
	Detail                  RequestDetail // 弹窗权威行数据（status/timing/tokens/route/failure 等）
	UpstreamWriteMs         *int64
	UpstreamFirstDispatchMs *int64
	UpstreamDispatchMs      *int64
	UpstreamConnMs          *int64
	ScheduleSnapshotJSON    string
	InputCompositionJSON    string
	PrivacyScanJSON         string
	Flow                    *RequestFlow
	RequestRevision         int64
	Integrity               RequestIntegrity
	Source                  string // "hot_pool" | "database"
}

// 热池与 SQLite 详情共用归档格式及字节预算，并叠加尚未落库的写入缺口。
func (ut *UsageTracker) lifecycleFlow(requestID, raw string) (*RequestFlow, RequestIntegrity) {
	flow, integrity := ParseRequestFlowWhitelist(raw, true)
	if gapCode := ut.getPendingRequestGap(requestID); gapCode != "" && !integrity.WriteGap {
		integrity.WriteGap = true
		integrity.WriteGapCode = gapCode
		if integrity.DiagnosticCode == "" {
			integrity.DiagnosticCode = gapCode
			integrity.DiagnosticMessage = "追踪记录存在写入缺口"
		}
	}
	return flow, integrity
}

// GetRequestLifecycleData 按 request_id 读取生命周期详情。
// 行不存在返回 (nil, nil)；归档窗口（CompleteAndArchive 后、批量 INSERT 确认前）
// 由 SnapshotRequest 的 archiving map 覆盖，数据库路径在单条 SELECT 内保证自洽。
func (ut *UsageTracker) GetRequestLifecycleData(ctx context.Context, requestID string) (*RequestLifecycleData, error) {
	if requestID == "" {
		return nil, nil
	}

	// 热池启用时先查内存（含未提交的 archiving）；miss 后回退 DB。
	if ut.hotPoolEnabled && ut.hotPool != nil {
		if snapshot, ok := ut.hotPool.SnapshotRequest(requestID); ok {
			return ut.hotRequestLifecycleData(snapshot), nil
		}
	}

	if ut.readDB == nil {
		return nil, fmt.Errorf("read database not initialized")
	}

	return ut.readRequestLifecycleData(ctx, requestID)
}

func (ut *UsageTracker) hotRequestLifecycleData(snapshot *ActiveRequest) *RequestLifecycleData {
	flow, integrity := ut.lifecycleFlow(snapshot.RequestID, snapshot.RequestFlowJSON)
	facts := snapshot.hotOfficialBillingFacts()
	var multiplier *EndpointMultiplier
	if facts.settled && snapshot.settled != nil && snapshot.billing.versions != nil && snapshot.CostOverrideUSD == nil {
		value := snapshot.billing.multiplier(snapshot)
		multiplier = &value
	}
	return &RequestLifecycleData{
		StreamPhases:            snapshot.StreamPhases.Clone(),
		OfficialRecalculation:   facts.availability(),
		BillingMultipliers:      appliedBillingMultipliers(multiplier, facts.usage, facts.path, facts.decision),
		Detail:                  ut.ActiveRequestToDetail(snapshot),
		UpstreamWriteMs:         snapshot.UpstreamWriteMs,
		UpstreamFirstDispatchMs: snapshot.UpstreamFirstDispatchMs,
		UpstreamDispatchMs:      snapshot.UpstreamDispatchMs,
		UpstreamConnMs:          snapshot.UpstreamConnMs,
		ScheduleSnapshotJSON:    snapshot.ScheduleSnapshotJSON,
		InputCompositionJSON:    snapshot.InputCompositionJSON,
		PrivacyScanJSON:         snapshot.PrivacyScanJSON,
		Flow:                    flow,
		RequestRevision:         snapshot.RequestRevision,
		Integrity:               integrity,
		Source:                  "hot_pool",
	}
}

func (ut *UsageTracker) readRequestLifecycleData(ctx context.Context, requestID string) (*RequestLifecycleData, error) {
	row := ut.readDB.QueryRowContext(ctx, "SELECT "+requestDetailsColumnsBase+`,
    upstream_dispatch_ms, upstream_conn_ms, first_reasoning_ms, reasoning_hidden, upstream_request_id,
    COALESCE(schedule_snapshot_json, ''), COALESCE(input_composition_json, ''), COALESCE(request_flow_json, ''), COALESCE(request_revision, 0), `+archivedRateColumns+`
    FROM request_logs q`+archivedRateJoin+` WHERE q.request_id = ?`, requestID)

	var flowJSON string
	var billing archivedBillingFacts
	data := &RequestLifecycleData{Source: "database"}
	dest := []any{
		&data.UpstreamDispatchMs, &data.UpstreamConnMs, &data.FirstReasoningMs, &data.ReasoningHidden,
		&data.UpstreamRequestID, &data.ScheduleSnapshotJSON, &data.InputCompositionJSON, &flowJSON, &data.RequestRevision,
	}
	dest = append(dest, billing.rate.dest()...)
	detail, err := ut.scanRequestDetailRow(row, &data.PrivacyScanJSON, &billing, dest...)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read request lifecycle: %w", err)
	}
	data.Detail = detail
	usage := TokenUsage{CacheWriteDurations: detail.CacheWriteDurations, InputTokens: detail.InputTokens,
		OutputTokens: detail.OutputTokens, CacheCreationTokens: detail.CacheCreationTokens, CacheReadTokens: detail.CacheReadTokens,
		CacheCreation5mTokens: detail.CacheCreation5mTokens, CacheCreation1hTokens: detail.CacheCreation1hTokens}
	facts := billing.officialFacts(detail.Status, detail.EndTime != nil, detail.Path, detail.BillingRateID, &usage)
	data.OfficialRecalculation = facts.availability()
	if facts.settled {
		data.BillingMultipliers = appliedBillingMultipliers(billing.Multiplier, facts.usage, facts.path, facts.decision)
	}
	data.FirstContentMs, data.FirstTextMs = detail.FirstContentMs, detail.FirstTextMs
	data.StreamFlowMs, data.StopReason = detail.StreamFlowMs, detail.StopReason
	data.UpstreamWriteMs, data.UpstreamFirstDispatchMs = detail.UpstreamWriteMs, detail.UpstreamFirstDispatchMs
	data.Flow, data.Integrity = ut.lifecycleFlow(requestID, flowJSON)
	return data, nil
}
