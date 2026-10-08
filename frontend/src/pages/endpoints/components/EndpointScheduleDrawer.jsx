// ============================================
// 端点调度快照抽屉
// 2026-07-24
// ============================================

import {
  Activity,
  AlertTriangle,
  CheckCircle2,
  Clock3,
  SkipForward
} from 'lucide-react';
import { useTimezone } from '@contexts/TimezoneContext.jsx';
import { FormBlock, FormSection } from '@components/desktop/FormSection.jsx';
import ListEmpty from '@components/desktop/ListEmpty.jsx';
import SidePanel from '@components/desktop/SidePanel.jsx';

// 快照结果解析与抽屉组件同文件（被测试与页面共用）；仅影响 HMR 精度，不影响构建。
// eslint-disable-next-line react-refresh/only-export-components
export const resolveSnapshotOutcome = (snapshot = {}) => {
  const outcome = String(snapshot.finalOutcome || snapshot.final_outcome || '').trim().toLowerCase();
  const config = {
    pending: { label: '进行中', className: 'tone-blue', isAbnormal: false },
    success: { label: '成功', className: 'tone-emerald', isAbnormal: false },
    passthrough_raw: { label: '原样透传', className: 'tone-slate', isAbnormal: false },
    passthrough_error: { label: '上游失败', className: 'tone-amber', isAbnormal: true },
    cancelled: { label: '已取消', className: 'tone-slate', isAbnormal: true },
    quality_incomplete: { label: '响应不完整', className: 'tone-amber', isAbnormal: true },
    failed_after_commit: { label: '响应阶段失败', className: 'tone-amber', isAbnormal: true },
    privacy_blocked: { label: '隐私策略拦截', className: 'tone-amber', isAbnormal: true },
    no_candidates: { label: '无可用候选', className: 'tone-amber', isAbnormal: true },
    manual_fixed_blocked: { label: '固定目标不可用', className: 'tone-amber', isAbnormal: true },
    model_routing_unassigned: { label: '模型未配置上游端点', className: 'tone-amber', isAbnormal: true },
    all_candidates_failed: { label: '全部候选失败', className: 'tone-amber', isAbnormal: true },
    rejected_by_failure_tracker: { label: '失败阈值拦截', className: 'tone-amber', isAbnormal: true },
    rate_limited: { label: '限流', className: 'tone-amber', isAbnormal: true }
  };
  return config[outcome] || {
    label: outcome || '未完成',
    className: outcome ? 'tone-amber' : 'tone-slate',
    isAbnormal: Boolean(outcome)
  };
};

const runtimeLabels = {
  attempting: '尝试中',
  try_next: '失败，尝试下一候选',
  success: '成功命中',
  quality_incomplete: '响应不完整',
  failed_after_commit: '响应阶段失败',
  cancelled: '已取消',
  privacy_blocked: '隐私策略拦截',
  passthrough_error: '上游失败',
  passthrough_raw: '原样透传'
};

const reasonLabels = {
  auto_priority: '自动调度优先级',
  auto_retained: '同优先级上次成功端点',
  fallback: '故障转移候选',
  manual_preferred: '手动优选目标',
  manual_fixed: '手动固定目标',
  availability_disabled: '端点已硬关闭',
  auto_schedule_disabled: '未参与自动调度',
  endpoint_missing: '端点不存在',
  paused: '手动暂停中',
  cooldown: '冷却中',
  failure_threshold_tripped: '失败阈值已触发',
  manual_fixed_target_missing: '手动固定目标不存在',
  manual_fixed_paused: '手动固定目标已暂停',
  manual_fixed_cooldown: '手动固定目标冷却中',
    manual_fixed_failure_threshold_tripped: '手动固定目标触发失败阈值',
    model_routing_unassigned: '模型未配置上游端点',
    model_routing_missing_model: '请求未携带模型',
    member_missing: '组成员不存在',
    outside_model_group: '不在该模型的端点集合中',
    manual_fixed_target_not_member: '固定目标不在该模型的端点集合中',
    manual_preferred_target_not_member: '优选目标不在该模型的端点集合中'
};

const negativeCacheLabels = {
  model_unsupported: '模型不支持',
  schema_incompatible: '请求 schema 不兼容',
  payload_too_large: '请求体过大',
  count_tokens_unsupported: '不支持 count_tokens',
  client_cancel: '客户端取消'
};

const resolveReasonLabel = (reason) => {
  if (!reason) return '-';
  if (reasonLabels[reason]) return reasonLabels[reason];
  if (reason.startsWith('manual_fixed_')) {
    return `手动固定目标：${resolveReasonLabel(reason.slice('manual_fixed_'.length))}`;
  }
  if (reason.startsWith('negative_cache_')) {
    const failureClass = reason.slice('negative_cache_'.length);
    return `路由负缓存：${negativeCacheLabels[failureClass] || failureClass}`;
  }
  return reason;
};

const routeModeLabels = {
  auto: '自动调度',
  manual_preferred: '手动优选',
  manual_fixed: '手动固定'
};

const EndpointScheduleDrawer = ({ open = false, onClose, snapshot = {}, unsupported = false }) => {
  const { formatTimestamp } = useTimezone();
  const formatTime = (value) => (value ? formatTimestamp(value) : '-');
  if (!open) return null;

  const { label: outcomeLabel, className: outcomeClass, isAbnormal } = resolveSnapshotOutcome(snapshot);
  const decisions = Array.isArray(snapshot.decisions) ? snapshot.decisions : [];
  const effectiveFailoverEnabled = snapshot.failoverEnabled
    && snapshot.routeFallbackEnabled !== false
    && snapshot.routeMode !== 'manual_fixed';

  const counts = decisions.reduce((result, decision) => {
    if (decision.decision === 'candidate') result.candidates += 1;
    if (decision.decision === 'skipped') result.skipped += 1;
    return result;
  }, { candidates: 0, skipped: 0 });

  return (
    <SidePanel
      open={open}
      onClose={onClose}
      label="最近一次端点调度"
      title="最近一次端点调度"
      subtitle="展示真实请求的候选筛选、跳过原因、实际命中端点与最终结果。"
      width={680}
      layer={45}
      closeOnOverlay={true}
      overlayLabel="关闭调度快照"
    >
      <FormSection title="调度快照" actions={!unsupported && (
        <>
          <span className="rounded border border-line px-1.5 text-[11px] leading-[18px] text-fg-muted">5s 自动刷新</span>
          {(snapshot.updatedAt || snapshot.capturedAt) && (
            <span className="text-[11.5px] text-fg-muted">更新于 {formatTime(snapshot.updatedAt || snapshot.capturedAt)}</span>
          )}
        </>
      )}>
        {unsupported ? (
          <FormBlock><p className="text-xs text-fg-body">{snapshot.message || '当前运行版本暂不支持端点调度快照。'}</p></FormBlock>
        ) : !snapshot.hasSnapshot ? (
          <ListEmpty icon={Activity} title="暂无端点调度记录"
            description="发起一次 Claude `/v1/messages` 请求后，这里会立即显示本次请求为何选择或跳过每个端点。" />
        ) : (
          <>
            <FormBlock>
              <div className="flex flex-wrap items-center gap-2">
                <span className={`${outcomeClass} rounded border px-1.5 text-[11px] leading-[18px]`}>{outcomeLabel}</span>
                <span className={`min-w-0 truncate text-[13px] font-medium ${isAbnormal ? 'text-warn' : 'text-fg'}`}>
                  {snapshot.summary || '端点调度已完成'}
                </span>
              </div>
              <div className="mt-2 flex flex-wrap gap-x-4 gap-y-1 text-xs text-fg-muted">
                <span>请求：<span className="font-mono text-fg-body">{snapshot.requestPath || '-'}</span></span>
                {snapshot.requestModel && <span>模型：<span className="font-mono text-fg-body">{snapshot.requestModel}</span></span>}
                {/* 同行左侧已有模型，这里只标明路由方式。 */}
                <span>{snapshot.routeScopeKind === 'upstream_group' ? '模型路由：已分配' : snapshot.routeScopeKind === 'unassigned' ? '模型路由：未分配' : '全局路由'}</span>
                <span>候选 {counts.candidates} · 跳过 {counts.skipped}</span>
              </div>
            </FormBlock>
            <FormBlock>
              <dl className="grid grid-cols-[96px_1fr] gap-x-3 gap-y-2 text-xs">
                <dt className="text-right text-fg-muted">实际命中</dt>
                <dd className="truncate font-medium text-fg" title={snapshot.selectedEndpoint}>{snapshot.selectedEndpoint || '-'}</dd>
                <dt className="text-right text-fg-muted">路由模式</dt>
                <dd className="text-fg-body">{routeModeLabels[snapshot.routeMode] || snapshot.routeMode || '自动调度'}</dd>
                <dt className="text-right text-fg-muted">请求内故障转移</dt>
                <dd className={effectiveFailoverEnabled ? 'text-success' : 'text-fg-muted'}>{effectiveFailoverEnabled ? '已启用' : snapshot.routeMode === 'manual_fixed' ? '固定模式关闭' : '已关闭'}</dd>
                <dt className="text-right text-fg-muted">请求 ID</dt>
                <dd className="truncate font-mono text-fg-body" title={snapshot.requestId}>{snapshot.requestId || '-'}</dd>
                <dt className="text-right text-fg-muted">路由目标</dt>
                <dd className="text-fg-body">{snapshot.routeEndpointName || '按自动策略选择'}</dd>
                <dt className="text-right text-fg-muted">捕获时间</dt>
                <dd className="text-fg-body">{formatTime(snapshot.capturedAt)}</dd>
              </dl>
              {snapshot.finalError && (
                <div className="mt-3 flex items-start gap-2 rounded-lg border border-warn-line bg-warn-soft px-3 py-2 text-xs text-warn">
                  <AlertTriangle size={14} className="mt-0.5 shrink-0" />
                  <span>最终信息：{snapshot.finalError}</span>
                </div>
              )}
            </FormBlock>
          </>
        )}
      </FormSection>

      {!unsupported && snapshot.hasSnapshot && (
        <FormSection title="候选与跳过决策" description="按调度解释顺序展示">
          {decisions.length === 0 ? (
            <FormBlock><p className="text-xs text-fg-muted">本次请求未生成端点候选。</p></FormBlock>
          ) : decisions.map((decision, index) => {
            const candidate = decision.decision === 'candidate';
            return (
              <div key={`${decision.name}-${index}`} className="px-3.5 py-2">
                <div className="flex items-start gap-2">
                  {candidate ? <Activity size={14} className="mt-0.5 shrink-0 text-accent" /> : <SkipForward size={14} className="mt-0.5 shrink-0 text-fg-muted" />}
                  <div className="min-w-0 flex-1">
                    <div className="flex flex-wrap items-center gap-2">
                      <span className="truncate text-[13px] font-medium text-fg">{decision.name}</span>
                      <span className={`${candidate ? 'tone-indigo' : 'tone-slate'} rounded border px-1.5 text-[11px] leading-[18px]`}>{candidate ? '候选' : '跳过'}</span>
                      {decision.runtimeOutcome && (
                        <span className={`inline-flex items-center gap-1 rounded border px-1.5 text-[11px] leading-[18px] ${decision.runtimeOutcome === 'success' ? 'tone-emerald' : decision.runtimeOutcome === 'attempting' ? 'tone-blue' : 'tone-amber'}`}>
                          {decision.runtimeOutcome === 'success' ? <CheckCircle2 size={10} /> : <Clock3 size={10} />}
                          {runtimeLabels[decision.runtimeOutcome] || decision.runtimeOutcome}
                        </span>
                      )}
                    </div>
                    <div className="mt-0.5 text-xs text-fg-muted">{resolveReasonLabel(decision.reason)}</div>
                  </div>
                  {decision.availableAt && <span className="shrink-0 text-xs text-warn">预计恢复：{formatTime(decision.availableAt)}</span>}
                </div>
                {decision.runtimeError && (
                  <div className="mt-2 flex items-start gap-2 rounded-lg border border-danger-line bg-danger-soft px-3 py-2 text-xs text-danger">
                    <AlertTriangle size={14} className="mt-0.5 shrink-0" />
                    <span>{decision.runtimeError}</span>
                  </div>
                )}
              </div>
            );
          })}
        </FormSection>
      )}
    </SidePanel>
  );
};

export default EndpointScheduleDrawer;
