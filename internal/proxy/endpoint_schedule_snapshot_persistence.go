package proxy

// 端点调度快照的请求级持久化白名单 DTO。
// EndpointScheduleSnapshot 的运行态包含 RuntimeError / FinalError 原文，
// 其中可能带有上游回显或请求内容；历史请求只保存稳定的路由事实和枚举结果。

import (
	"encoding/json"
	"log/slog"
	"time"

	"sinuo/internal/endpoint"
	"sinuo/internal/tracking"
)

type persistedEndpointScheduleSnapshot struct {
	RequestID              string                              `json:"request_id"`
	CapturedAt             time.Time                           `json:"captured_at"`
	UpdatedAt              time.Time                           `json:"updated_at"`
	RequestPath            string                              `json:"request_path"`
	SelectedEndpoint       string                              `json:"selected_endpoint"`
	RouteMode              string                              `json:"route_mode"`
	RouteEndpointName      string                              `json:"route_endpoint_name"`
	RouteFallbackEnabled   bool                                `json:"route_fallback_enabled"`
	RouteScopeKind         string                              `json:"route_scope_kind,omitempty"`
	RouteScope             string                              `json:"route_scope,omitempty"`
	RouteScopeGroupID      int64                               `json:"route_scope_group_id,omitempty"`
	RouteScopeGroupName    string                              `json:"route_scope_group_name,omitempty"`
	UpstreamGroup          *endpoint.UpstreamGroupSnapshot     `json:"upstream_group,omitempty"`
	RequestModel           string                              `json:"request_model,omitempty"`
	ModelRoutingVersion    int64                               `json:"model_routing_version,omitempty"`
	FailoverEnabled        bool                                `json:"failover_enabled"`
	CandidateAttemptBudget int                                 `json:"candidate_attempt_budget,omitempty"`
	FinalOutcome           string                              `json:"final_outcome"`
	Summary                string                              `json:"summary"`
	Decisions              []persistedEndpointScheduleDecision `json:"decisions"`
}

type persistedEndpointScheduleDecision struct {
	Name           string     `json:"name"`
	Decision       string     `json:"decision"`
	Reason         string     `json:"reason"`
	AvailableAt    *time.Time `json:"available_at,omitempty"`
	RuntimeOutcome string     `json:"runtime_outcome,omitempty"`
}

// marshalEndpointScheduleSnapshot serializes only stable, locally-derived fields.
// The shared 32KB limit is intentionally reused with the account-pool snapshot.
func marshalEndpointScheduleSnapshot(snapshot *endpoint.EndpointScheduleSnapshot) string {
	if snapshot == nil {
		return ""
	}

	dto := persistedEndpointScheduleSnapshot{
		RequestID:              snapshot.RequestID,
		CapturedAt:             snapshot.CapturedAt,
		UpdatedAt:              snapshot.UpdatedAt,
		RequestPath:            snapshot.RequestPath,
		SelectedEndpoint:       snapshot.SelectedEndpoint,
		RouteMode:              snapshot.RouteMode,
		RouteEndpointName:      snapshot.RouteEndpointName,
		RouteFallbackEnabled:   snapshot.RouteFallbackEnabled,
		RouteScopeKind:         snapshot.RouteScopeKind,
		RouteScope:             snapshot.RouteScope,
		RouteScopeGroupID:      snapshot.RouteScopeGroupID,
		RouteScopeGroupName:    snapshot.RouteScopeGroupName,
		UpstreamGroup:          clonePersistedUpstreamGroup(snapshot.UpstreamGroup),
		RequestModel:           snapshot.RequestModel,
		ModelRoutingVersion:    snapshot.ModelRoutingVersion,
		FailoverEnabled:        snapshot.FailoverEnabled,
		CandidateAttemptBudget: snapshot.CandidateAttemptBudget,
		FinalOutcome:           snapshot.FinalOutcome,
		Summary:                truncateForSnapshot(snapshot.Summary, 512),
		Decisions:              make([]persistedEndpointScheduleDecision, 0, len(snapshot.Decisions)),
	}
	for _, decision := range snapshot.Decisions {
		var availableAt *time.Time
		if !decision.AvailableAt.IsZero() {
			value := decision.AvailableAt
			availableAt = &value
		}
		dto.Decisions = append(dto.Decisions, persistedEndpointScheduleDecision{
			Name:           decision.Name,
			Decision:       decision.Decision,
			Reason:         decision.Reason,
			AvailableAt:    availableAt,
			RuntimeOutcome: decision.RuntimeOutcome,
		})
	}

	payload, err := json.Marshal(dto)
	if err != nil {
		slog.Warn("⏭️ [端点调度快照持久化] 序列化失败，跳过写入", "request_id", snapshot.RequestID, "error", err)
		return ""
	}
	if len(payload) <= scheduleSnapshotJSONSizeLimit {
		return string(payload)
	}

	// 端点数量受配置限制，但仍给历史表设置保险丝；组身份和终态比候选明细更重要。
	dto.Decisions = nil
	slim, slimErr := json.Marshal(dto)
	if slimErr == nil && len(slim) <= scheduleSnapshotJSONSizeLimit {
		slog.Warn("⏭️ [端点调度快照持久化] 快照超 32KB，已丢弃候选列表",
			"request_id", snapshot.RequestID, "original_bytes", len(payload))
		return string(slim)
	}
	slog.Warn("⏭️ [端点调度快照持久化] 快照超 32KB 且精简后仍超限，跳过写入",
		"request_id", snapshot.RequestID, "bytes", len(payload))
	return ""
}

func clonePersistedUpstreamGroup(group *endpoint.UpstreamGroupSnapshot) *endpoint.UpstreamGroupSnapshot {
	if group == nil {
		return nil
	}
	clone := *group
	clone.Members = append([]string(nil), group.Members...)
	return &clone
}

func (h *Handler) persistEndpointScheduleSnapshot(requestID string, snapshot *endpoint.EndpointScheduleSnapshot) {
	if h == nil || h.usageTracker == nil || requestID == "" || snapshot == nil {
		return
	}
	if payload := marshalEndpointScheduleSnapshot(snapshot); payload != "" {
		h.usageTracker.RecordRequestUpdate(requestID, tracking.UpdateOptions{ScheduleSnapshotJSON: &payload})
	}
}
