// request_lifecycle.go
// 请求生命周期详情读取：热池安全快照 + DB 单行查询的统一出口。
// 分段计时、调度快照与隐私摘要列
// 不进入通用 RequestDetail 与列表读链路，只在本路径单独返回。

package tracking

import (
	"context"
	"database/sql"
	"fmt"
)

// RequestLifecycleData 弹窗权威详情数据。
// Detail 复用既有 RequestDetail 映射；生命周期字段仅在此结构返回。
type RequestLifecycleData struct {
	Detail               RequestDetail // 弹窗权威行数据（status/timing/tokens/route/failure 等）
	UpstreamWriteMs      *int64
	UpstreamDispatchMs   *int64
	UpstreamConnMs       *int64
	ScheduleSnapshotJSON string
	PrivacyScanJSON      string
	Source               string // "hot_pool" | "database"
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
				Detail:               ut.ActiveRequestToDetail(snapshot),
				UpstreamWriteMs:      snapshot.UpstreamWriteMs,
				UpstreamDispatchMs:   snapshot.UpstreamDispatchMs,
				UpstreamConnMs:       snapshot.UpstreamConnMs,
				ScheduleSnapshotJSON: snapshot.ScheduleSnapshotJSON,
				PrivacyScanJSON:      snapshot.PrivacyScanJSON,
				Source:               "hot_pool",
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
	row := ut.readDB.QueryRowContext(ctx, `
		SELECT upstream_write_ms, upstream_dispatch_ms, upstream_conn_ms,
			COALESCE(schedule_snapshot_json, ''),
			COALESCE(privacy_scan_json, '')
		FROM request_logs WHERE request_id = ?`, requestID)
	if err := row.Scan(&upstreamWriteMs, &upstreamDispatchMs, &upstreamConnMs, &scheduleJSON, &privacyJSON); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to query request lifecycle columns: %w", err)
	}

	data := &RequestLifecycleData{
		Detail:               details[0],
		ScheduleSnapshotJSON: scheduleJSON,
		PrivacyScanJSON:      privacyJSON,
		Source:               "database",
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
