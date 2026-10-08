// 工作区共用纯函数；模型显示名由协议配置提供。
export const ROUTING_FILTERS = [
  { value: 'all', label: '全部' },
  { value: 'enabled', label: '已启用' },
  { value: 'disabled', label: '已停用' }
];

const OUTCOME_LABELS = {
  success: '成功',
  cancelled: '已取消',
  quality_incomplete: '响应不完整',
  failed_after_commit: '响应阶段失败',
  passthrough_error: '上游失败',
  no_candidates: '无可用候选',
  manual_fixed_blocked: '固定目标不可用',
  all_candidates_failed: '全部候选失败'
};

export const routingOutcomeLabel = (outcome) => OUTCOME_LABELS[outcome] || outcome || '—';

const modelDisplayName = (binding) => binding.model || '';

export const filterRoutingBindings = (bindings, { query = '', filter = 'all', displayName = modelDisplayName } = {}) => {
  const needle = query.trim().toLowerCase();
  return bindings.filter((binding) => {
    if (filter === 'enabled' && !binding.enabled) return false;
    if (filter === 'disabled' && binding.enabled) return false;
    return !needle || `${displayName(binding)} ${binding.note || ''}`.toLowerCase().includes(needle);
  });
};

export const routingCounts = (bindings) => {
  const enabled = bindings.filter((binding) => binding.enabled).length;
  return { total: bindings.length, enabled, disabled: bindings.length - enabled };
};
