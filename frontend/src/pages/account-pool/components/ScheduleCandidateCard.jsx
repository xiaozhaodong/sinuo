// ============================================
// 最近一次调度候选账号卡片
// 2026-03-07
// ============================================

import { useTimezone } from '@contexts/TimezoneContext.jsx';
import { AlertCircle } from 'lucide-react';
import {
  MANUAL_FAILOVER_TIER_PRESETS,
  QUOTA_STATUS_STYLE,
  SCHEDULE_DECISION_STYLE,
  SCHEDULE_OUTCOME_STYLE,
  normalizePriorityValue,
  resolveAccountId,
  toAccountAuthLabel,
  toQuotaStatusLabel,
  toScheduleDecisionLabel,
  toScheduleOutcomeLabel,
  toScheduleReasonLabel
} from '../utils.js';

const ScheduleCandidateCard = ({ candidate, index }) => {
  const { formatTimestamp } = useTimezone();
  const candidateId = resolveAccountId(candidate) ?? `${candidate.account_name || candidate.accountName || 'candidate'}-${index}`;
  const decision = String(candidate.decision || '').trim().toLowerCase();
  const decisionClass = SCHEDULE_DECISION_STYLE[decision] || SCHEDULE_DECISION_STYLE.skipped;
  const runtimeOutcome = String(candidate.runtime_outcome || candidate.runtimeOutcome || '').trim().toLowerCase();
  const runtimeOutcomeClass = SCHEDULE_OUTCOME_STYLE[runtimeOutcome] || SCHEDULE_OUTCOME_STYLE.pending;
  const quotaStatus = String(candidate.quota_status || candidate.quotaStatus || '').trim().toLowerCase() || 'pending';
  const quotaClass = QUOTA_STATUS_STYLE[quotaStatus] || QUOTA_STATUS_STYLE.pending;
  const candidatePriority = normalizePriorityValue(candidate.priority ?? candidate.Priority);
  const candidateTierIndex = normalizePriorityValue(candidate.tier_index ?? candidate.tierIndex);
  const candidateTierClass = MANUAL_FAILOVER_TIER_PRESETS[(candidateTierIndex || 1) - 1]?.className || 'tone-slate';
  const effectiveQuotaRemaining = Number.parseFloat(candidate.effective_quota_remaining ?? candidate.effectiveQuotaRemaining);
  const failCount = Number.parseInt(candidate.fail_count ?? candidate.failCount, 10) || 0;
  const lastSuccessAt = candidate.last_success_at || candidate.lastSuccessAt || '';
  const runtimeError = candidate.runtime_error || candidate.runtimeError || '';

  return (
    <div
      key={String(candidateId)}
      className={`px-3.5 py-2 ${decision === 'selected' ? 'bg-accent-soft/40' : ''}`}
    >
      <div className="flex flex-col gap-2">
        <div className="min-w-0 flex-1">
          <div className="flex flex-wrap items-center gap-2">
            <span className="text-xs font-medium text-fg-muted">#{index + 1}</span>
            <span className="text-[13px] font-semibold text-fg">{candidate.account_name || candidate.accountName || '-'}</span>
            <span className="rounded border px-1.5 text-[11px] leading-[18px] bg-accent-soft text-accent border-accent-line">{toAccountAuthLabel(candidate.provider_type || candidate.providerType || '')}</span>
            <span className={"rounded border px-1.5 text-[11px] leading-[18px] " + (candidateTierClass)}>{`${candidate.tier_label || candidate.tierLabel || '未分组'}${Number.isFinite(candidatePriority) ? ` · 组内顺序 ${candidatePriority}` : ''}`}</span>
            <span className={"rounded border px-1.5 text-[11px] leading-[18px] " + (decisionClass)}>{toScheduleDecisionLabel(decision)}</span>
            {runtimeOutcome && (
              <span className={"rounded border px-1.5 text-[11px] leading-[18px] " + (runtimeOutcomeClass)}>{toScheduleOutcomeLabel(runtimeOutcome)}</span>
            )}
          </div>

          <div className="mt-1.5 text-xs font-medium text-fg">
            {toScheduleReasonLabel(candidate.reason || '')}
          </div>
          <div className="mt-1 text-xs leading-5 text-fg-muted">
            {candidate.reason_detail || candidate.reasonDetail || '调度器未返回更详细的解释。'}
          </div>
        </div>

        <div className="flex flex-wrap gap-1.5">
          <span className={"rounded border px-1.5 text-[11px] leading-[18px] " + (quotaClass)}>{toQuotaStatusLabel(quotaStatus)}</span>
          {Number.isFinite(effectiveQuotaRemaining) && (
            <span className="rounded border px-1.5 text-[11px] leading-[18px] bg-surface text-fg-body border-line">{`剩余额度 ${effectiveQuotaRemaining.toFixed(0)}%`}</span>
          )}
          <span className="rounded border px-1.5 text-[11px] leading-[18px] bg-surface text-fg-body border-line">{`失败 ${failCount}`}</span>
          {lastSuccessAt && (
            <span className="rounded border px-1.5 text-[11px] leading-[18px] bg-surface text-success border-success-line">{`最近成功 ${formatTimestamp(lastSuccessAt)}`}</span>
          )}
        </div>
      </div>

      {runtimeError && (
        <div className="mt-3 flex items-start gap-2 rounded-lg border border-warn-line bg-warn-soft px-3 py-2 text-xs text-warn">
          <AlertCircle size={14} className="mt-0.5 shrink-0" />
          <span>运行时错误：{runtimeError}</span>
        </div>
      )}
    </div>
  );
};

export default ScheduleCandidateCard;
