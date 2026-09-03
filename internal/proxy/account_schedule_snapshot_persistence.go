// account_schedule_snapshot_persistence.go
// 账号池调度快照的持久化白名单 DTO 与序列化；其中长度保险丝与截断 helper
// 供端点调度快照持久化共用。
// 运行态内存快照（LatestAccountScheduleSnapshot）继续保留 FinalError / RuntimeError
// 供账号池页实时展示；持久化 DTO 明确不收录任何上游自由错误文本
//（readAndCloseResponseBody / err.Error() 截断不等于脱敏，前 256 字符同样可能含 Token 回显）。

package proxy

import (
	"encoding/json"
	"log/slog"
	"time"

	svc "cc-forwarder/internal/service"
)

// persistedScheduleSnapshot 请求级调度快照持久化 DTO。
// JSON tag 与运行态结构同名，前端可复用账号池快照的渲染逻辑，缺失字段自然为 undefined。
type persistedScheduleSnapshot struct {
	RequestID               string                       `json:"request_id"`
	CapturedAt              time.Time                    `json:"captured_at"`
	UpdatedAt               time.Time                    `json:"updated_at"`
	RequestPath             string                       `json:"request_path"`
	SelectedPriority        int                          `json:"selected_priority"`
	SelectedTierIndex       int                          `json:"selected_tier_index"`
	SelectedTierLabel       string                       `json:"selected_tier_label"`
	DegradedToLowerPriority bool                         `json:"degraded_to_lower_priority"`
	SelectedAccountID       int64                        `json:"selected_account_id"`
	SelectedAccountName     string                       `json:"selected_account_name"`
	FinalOutcome            string                       `json:"final_outcome"` // 枚举码
	Summary                 string                       `json:"summary"`       // 调度器内部生成，截断兜底
	Candidates              []persistedScheduleCandidate `json:"candidates"`
}

// persistedScheduleCandidate 候选账号决策持久化 DTO。
// 不收录 RuntimeError 原文，仅保留 RuntimeOutcome 枚举码。
type persistedScheduleCandidate struct {
	AccountID               int64      `json:"account_id"`
	AccountName             string     `json:"account_name"`
	ProviderType            string     `json:"provider_type"`
	Priority                int        `json:"priority"`
	TierIndex               int        `json:"tier_index"`
	TierLabel               string     `json:"tier_label"`
	QuotaStatus             string     `json:"quota_status"`
	EffectiveQuotaRemaining *float64   `json:"effective_quota_remaining,omitempty"`
	FailCount               int        `json:"fail_count"`
	LastSuccessAt           *time.Time `json:"last_success_at,omitempty"`
	Decision                string     `json:"decision"`
	Reason                  string     `json:"reason"`
	ReasonDetail            string     `json:"reason_detail"`             // 调度器内部生成，截断兜底
	RuntimeOutcome          string     `json:"runtime_outcome,omitempty"` // 枚举码
}

// scheduleSnapshotJSONSizeLimit 序列化产物长度保险丝（32KB）。
// 超限时丢弃 Candidates 仅保留头部字段；仍超限返回空串，调用方跳过写入。
const scheduleSnapshotJSONSizeLimit = 32 * 1024

// truncateForSnapshot rune 安全的截断，纯兜底；Summary/ReasonDetail 均由调度器内部生成，不含上游原文。
func truncateForSnapshot(s string, max int) string {
	if max <= 0 {
		return ""
	}
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	return string(runes[:max])
}

// marshalScheduleSnapshot 构造白名单 DTO 并序列化。
// 返回空串表示不应写入（snapshot 为 nil 或序列化失败/超限）。
// 运行态快照不被修改。
func marshalScheduleSnapshot(snapshot *svc.LatestAccountScheduleSnapshot) string {
	if snapshot == nil {
		return ""
	}

	dto := persistedScheduleSnapshot{
		RequestID:               snapshot.RequestID,
		CapturedAt:              snapshot.CapturedAt,
		UpdatedAt:               snapshot.UpdatedAt,
		RequestPath:             snapshot.RequestPath,
		SelectedPriority:        snapshot.SelectedPriority,
		SelectedTierIndex:       snapshot.SelectedTierIndex,
		SelectedTierLabel:       snapshot.SelectedTierLabel,
		DegradedToLowerPriority: snapshot.DegradedToLowerPriority,
		SelectedAccountID:       snapshot.SelectedAccountID,
		SelectedAccountName:     snapshot.SelectedAccountName,
		FinalOutcome:            snapshot.FinalOutcome,
		Summary:                 truncateForSnapshot(snapshot.Summary, 512),
		Candidates:              make([]persistedScheduleCandidate, 0, len(snapshot.Candidates)),
	}
	for _, candidate := range snapshot.Candidates {
		dto.Candidates = append(dto.Candidates, persistedScheduleCandidate{
			AccountID:               candidate.AccountID,
			AccountName:             candidate.AccountName,
			ProviderType:            candidate.ProviderType,
			Priority:                candidate.Priority,
			TierIndex:               candidate.TierIndex,
			TierLabel:               candidate.TierLabel,
			QuotaStatus:             candidate.QuotaStatus,
			EffectiveQuotaRemaining: candidate.EffectiveQuotaRemaining,
			FailCount:               candidate.FailCount,
			LastSuccessAt:           candidate.LastSuccessAt,
			Decision:                candidate.Decision,
			Reason:                  candidate.Reason,
			ReasonDetail:            truncateForSnapshot(candidate.ReasonDetail, 256),
			RuntimeOutcome:          candidate.RuntimeOutcome,
		})
	}

	payload, err := json.Marshal(dto)
	if err != nil {
		slog.Warn("⏭️ [调度快照持久化] 序列化失败，跳过写入", "request_id", snapshot.RequestID, "error", err)
		return ""
	}

	if len(payload) > scheduleSnapshotJSONSizeLimit {
		// 保险丝：丢弃 Candidates 仅保留头部字段重试。
		dto.Candidates = nil
		if slim, slimErr := json.Marshal(dto); slimErr == nil {
			if len(slim) <= scheduleSnapshotJSONSizeLimit {
				slog.Warn("⏭️ [调度快照持久化] 快照超 32KB，已丢弃候选列表",
					"request_id", snapshot.RequestID, "original_bytes", len(payload))
				return string(slim)
			}
		}
		slog.Warn("⏭️ [调度快照持久化] 快照超 32KB 且精简后仍超限，跳过写入",
			"request_id", snapshot.RequestID, "bytes", len(payload))
		return ""
	}

	return string(payload)
}
