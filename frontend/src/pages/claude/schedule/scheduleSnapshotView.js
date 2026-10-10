// 端点调度快照的共享文案与展示规则，沿用原抽屉语义。
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

export const runtimeLabels = {
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

export const reasonLabels = {
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

export const resolveReasonLabel = (reason) => {
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

export const routeModeLabels = {
  auto: '自动调度',
  manual_preferred: '手动优选',
  manual_fixed: '手动固定'
};

export const countDecisions = (decisions = []) => (
  decisions.reduce((counts, decision) => {
    if (decision.decision === 'candidate') counts.candidates += 1;
    if (decision.decision === 'skipped') counts.skipped += 1;
    return counts;
  }, { candidates: 0, skipped: 0 })
);

export const resolveRouteScopeLabel = (snapshot = {}) => {
  if (snapshot.routeScopeKind === 'upstream_group') return '模型路由：已分配';
  if (snapshot.routeScopeKind === 'unassigned') return '模型路由：未分配';
  return '全局路由';
};

export const resolveEffectiveFailover = (snapshot = {}) => {
  const enabled = Boolean(snapshot.failoverEnabled
    && snapshot.routeFallbackEnabled !== false
    && snapshot.routeMode !== 'manual_fixed');
  return {
    enabled,
    label: enabled ? '已启用' : snapshot.routeMode === 'manual_fixed' ? '固定模式关闭' : '已关闭'
  };
};
