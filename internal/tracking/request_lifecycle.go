// request_lifecycle.go
// 请求生命周期详情读取：热池安全快照 + DB 单行查询的统一出口。
// 分段计时、调度快照与隐私摘要列
// 首次取连接、写完与内容时间同时进入列表；思考信号与上游请求ID仅详情返回。

package tracking

import (
	"context"
	"database/sql"
	"fmt"
)

// RequestLifecycleData 弹窗权威详情数据。
// Detail 复用列表行映射；此结构补足完整阶段字段、调度与隐私摘要。
type RequestLifecycleData struct {
	StreamPhases
	Detail                  RequestDetail // 弹窗权威行数据（status/timing/tokens/route/failure 等）
	UpstreamWriteMs         *int64
	UpstreamFirstDispatchMs *int64
	UpstreamDispatchMs      *int64
	UpstreamConnMs          *int64
	ScheduleSnapshotJSON    string
	PrivacyScanJSON         string
	Source                  string // "hot_pool" | "database"
}

// GetRequestLifecycleData 按 request_id 读取生命周期详情。
// 行不存在返回 (nil, nil)；归档窗口（CompleteAndArchive 后、批量 INSERT 确认前）
// 由 SnapshotRequest 的 archiving map 覆盖。
func (ut *UsageTracker) GetRequestLifecycleData(ctx context.Context, requestID string) (*RequestLifecycleData, error) {
	if requestID == "" {
		return nil, nil
	}

	// 热池启用时先查内存（含 archiving）；miss 后回退 DB。
	if ut.hotPoolEnabled && ut.hotPool != nil {
		if snapshot, ok := ut.hotPool.SnapshotRequest(requestID); ok {
			return &RequestLifecycleData{
				StreamPhases:            snapshot.StreamPhases.Clone(),
				Detail:                  ut.ActiveRequestToDetail(snapshot),
				UpstreamWriteMs:         snapshot.UpstreamWriteMs,
				UpstreamFirstDispatchMs: snapshot.UpstreamFirstDispatchMs,
				UpstreamDispatchMs:      snapshot.UpstreamDispatchMs,
				UpstreamConnMs:          snapshot.UpstreamConnMs,
				ScheduleSnapshotJSON:    snapshot.ScheduleSnapshotJSON,
				PrivacyScanJSON:         snapshot.PrivacyScanJSON,
				Source:                  "hot_pool",
			}, nil
		}
	}

	if ut.readDB == nil {
		return nil, fmt.Errorf("read database not initialized")
	}

	opts := &QueryOptions{RequestID: requestID}
	details, err := ut.QueryRequestDetails(ctx, opts)
	if err != nil {
		return nil, err
	}
	if len(details) == 0 {
		return nil, nil
	}

	var upstreamWriteMs, upstreamDispatchMs, upstreamConnMs sql.NullInt64
	var scheduleJSON, privacyJSON string
	var phases StreamPhases
	row := ut.readDB.QueryRowContext(ctx, `
		SELECT upstream_write_ms, upstream_dispatch_ms, upstream_conn_ms,
			first_content_ms, first_text_ms, first_reasoning_ms, reasoning_hidden,
			stream_flow_ms, stop_reason, upstream_request_id,
			COALESCE(schedule_snapshot_json, ''),
			COALESCE(privacy_scan_json, '')
		FROM request_logs WHERE request_id = ?`, requestID)
	if err := row.Scan(&upstreamWriteMs, &upstreamDispatchMs, &upstreamConnMs,
		&phases.FirstContentMs, &phases.FirstTextMs, &phases.FirstReasoningMs, &phases.ReasoningHidden,
		&phases.StreamFlowMs, &phases.StopReason, &phases.UpstreamRequestID, &scheduleJSON, &privacyJSON); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to query request lifecycle columns: %w", err)
	}

	data := &RequestLifecycleData{
		StreamPhases:            phases,
		Detail:                  details[0],
		UpstreamFirstDispatchMs: details[0].UpstreamFirstDispatchMs,
		ScheduleSnapshotJSON:    scheduleJSON,
		PrivacyScanJSON:         privacyJSON,
		Source:                  "database",
	}
	if upstreamWriteMs.Valid {
		data.UpstreamWriteMs = &upstreamWriteMs.Int64
	}
	if upstreamDispatchMs.Valid {
		data.UpstreamDispatchMs = &upstreamDispatchMs.Int64
	}
	if upstreamConnMs.Valid {
		data.UpstreamConnMs = &upstreamConnMs.Int64
	}
	return data, nil
}
