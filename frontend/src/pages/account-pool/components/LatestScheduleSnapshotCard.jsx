// ============================================
// 最近一次调度结果面板
// 2026-03-07
// ============================================

import { useState } from 'react';
import { AlertCircle, ChevronDown, ChevronUp, Info } from 'lucide-react';
import { useTimezone } from '@contexts/TimezoneContext.jsx';
import { Button } from '@components/ui';
import { FormBlock, FormSection } from '@components/desktop/FormSection.jsx';
import ListEmpty from '@components/desktop/ListEmpty.jsx';
import ScheduleCandidateCard from './ScheduleCandidateCard.jsx';
import {
  SCHEDULE_OUTCOME_STYLE,
  normalizePriorityValue,
  toScheduleOutcomeLabel
} from '../utils.js';

const DEFAULT_VISIBLE_CANDIDATES = 3;
const EMPTY_CANDIDATES = [];

const LatestScheduleSnapshotCard = ({ snapshot = {}, snapshotUnsupported = false }) => {
  const { formatTimestamp } = useTimezone();
  const toDisplayTime = (value) => (value ? formatTimestamp(value) : '-');
  const [isExpanded, setIsExpanded] = useState(false);
  const [showAllCandidates, setShowAllCandidates] = useState(false);

  const hasSnapshot = snapshot?.hasSnapshot === true || snapshot?.has_snapshot === true;
  const candidates = Array.isArray(snapshot?.candidates) ? snapshot.candidates : EMPTY_CANDIDATES;
  const outcome = String(snapshot?.finalOutcome || snapshot?.final_outcome || '').trim().toLowerCase() || (hasSnapshot ? 'pending' : '');
  const outcomeClass = SCHEDULE_OUTCOME_STYLE[outcome] || SCHEDULE_OUTCOME_STYLE.pending;
  const requestPath = snapshot?.requestPath || snapshot?.request_path || '/v1/responses';
  const selectedAccountName = snapshot?.selectedAccountName || snapshot?.selected_account_name || '-';
  const selectedTierLabel = snapshot?.selectedTierLabel || snapshot?.selected_tier_label || '-';
  const selectedPriority = normalizePriorityValue(snapshot?.selectedPriority ?? snapshot?.selected_priority);
  const capturedAt = snapshot?.capturedAt || snapshot?.captured_at || '';
  const updatedAt = snapshot?.updatedAt || snapshot?.updated_at || '';
  const finalError = snapshot?.finalError || snapshot?.final_error || '';
  const degraded = snapshot?.degradedToLowerPriority || snapshot?.degraded_to_lower_priority;
  const requestId = snapshot?.requestId || snapshot?.request_id || '';

  const isAbnormal = Boolean(degraded) || Boolean(finalError) || (Boolean(outcome) && !['success', 'pending'].includes(outcome));
  const snapshotIdentity = `${requestId}|${updatedAt}|${outcome}|${degraded ? '1' : '0'}|${finalError}`;

  // 快照变化时重置展开态（渲染期调整，避免 effect 级联渲染；isAbnormal 的输入均含于 identity）
  const [lastSnapshotIdentity, setLastSnapshotIdentity] = useState(null);
  if (lastSnapshotIdentity !== snapshotIdentity) {
    setLastSnapshotIdentity(snapshotIdentity);
    setIsExpanded(isAbnormal);
    setShowAllCandidates(false);
  }

  const visibleCandidates = showAllCandidates
    ? candidates
    : candidates.slice(0, DEFAULT_VISIBLE_CANDIDATES);

  const hiddenCandidateCount = Math.max(candidates.length - visibleCandidates.length, 0);

  return (
    <FormSection
      title="最近一次调度结果"
      description="默认收起详情；出现失败、降级或错误时会自动展开，避免影响账号管理主视图。"
      actions={(
        <>
          {!snapshotUnsupported && <span className="rounded border px-1.5 text-[11px] leading-[18px] tone-slate">{"5s 自动刷新"}</span>}
          {updatedAt && <span className="text-[11.5px] text-fg-muted">更新于 {formatTimestamp(updatedAt)}</span>}
        </>
      )}
    >
      {snapshotUnsupported ? (
        <FormBlock><p className="text-xs text-fg-body">{snapshot?.message || '当前后端版本暂未提供最近一次调度结果接口。'}</p></FormBlock>
      ) : !hasSnapshot ? (
        <ListEmpty icon={Info} title="暂无最近一次调度结果"
          description="发起一次 `/v1/responses` 请求后，这里会显示命中账号、命中组别、组内顺序、是否降级以及候选账号的跳过原因。" />
      ) : (
        <>
          <FormBlock>
            <div className="flex flex-wrap items-center gap-2 text-xs text-fg-body">
              <span className={"rounded border px-1.5 text-[11px] leading-[18px] " + (outcomeClass)}>{toScheduleOutcomeLabel(outcome)}</span>
              <span>命中 <span className="font-semibold text-fg">{selectedAccountName}</span></span>
              <span className="text-fg-muted">
                {selectedTierLabel}{Number.isFinite(selectedPriority) ? ` · 组内顺序 ${selectedPriority}` : ''}
              </span>
              <span className={"rounded border px-1.5 text-[11px] leading-[18px] " + (degraded ? 'tone-amber' : 'tone-emerald')}>{degraded ? '已降级' : '未降级'}</span>
              <span className="text-fg-muted">{toDisplayTime(updatedAt || capturedAt)}</span>
              <Button type="button" variant="ghost" size="sm" className="ml-auto gap-1" onClick={() => setIsExpanded(prev => !prev)}>
                {isExpanded ? '收起详情' : '查看详情'}
                {isExpanded ? <ChevronUp size={14} /> : <ChevronDown size={14} />}
              </Button>
            </div>
            {finalError && !isExpanded && (
              <p className="mt-2 break-all text-xs leading-relaxed text-warn">最终错误：{finalError}</p>
            )}
          </FormBlock>
          {isExpanded && (
            <>
              <FormBlock>
                <div className="text-[13px] font-semibold text-fg">{snapshot?.summary || '已生成最近一次调度结果摘要'}</div>
                <div className="mt-2 flex flex-wrap items-center gap-x-3 gap-y-1 text-xs text-fg-muted">
                  <span>请求路径 {requestPath}</span>
                  {requestId ? <span title={requestId}>请求 ID {requestId.slice(0, 12)}...</span> : null}
                  <span>最近捕获 {toDisplayTime(capturedAt)}</span>
                  <span>候选账号 {candidates.length}</span>
                </div>
                {finalError && (
                  <div className="mt-3 flex items-start gap-2 rounded-lg border border-warn-line bg-warn-soft px-3 py-2 text-xs text-warn">
                    <AlertCircle size={14} className="mt-0.5 shrink-0" />
                    <span>最终错误：{finalError}</span>
                  </div>
                )}
              </FormBlock>
              <FormBlock>
                <div className="text-[13px] font-semibold text-fg">候选账号决策列表</div>
                <div className="mt-0.5 text-xs text-fg-muted">
                  {hiddenCandidateCount > 0
                    ? `默认展示前 ${DEFAULT_VISIBLE_CANDIDATES} 个候选，避免面板过长`
                    : '按本次调度解释顺序展示'}
                </div>
              </FormBlock>
              {candidates.length === 0 ? (
                <FormBlock><p className="text-xs text-fg-muted">当前快照没有候选账号决策明细。</p></FormBlock>
              ) : (
                <>
                  {visibleCandidates.map((candidate, index) => (
                    <ScheduleCandidateCard
                      key={String(candidate.id ?? candidate.account_id ?? candidate.accountId ?? `${candidate.account_name || candidate.accountName || 'candidate'}-${index}`)}
                      candidate={candidate}
                      index={index}
                    />
                  ))}
                  {candidates.length > DEFAULT_VISIBLE_CANDIDATES && (
                    <FormBlock>
                      <div className="flex justify-center">
                        <Button type="button" variant="ghost" size="sm" onClick={() => setShowAllCandidates(prev => !prev)}>
                          {showAllCandidates ? '收起候选列表' : `显示其余 ${hiddenCandidateCount} 个候选`}
                        </Button>
                      </div>
                    </FormBlock>
                  )}
                </>
              )}
            </>
          )}
        </>
      )}
    </FormSection>
  );
};

export default LatestScheduleSnapshotCard;
