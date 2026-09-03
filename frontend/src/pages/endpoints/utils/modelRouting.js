// ============================================
// 模型路由：状态文案与表单校验纯函数
// 2026-09-03
// ============================================
//
// 一条映射 = 一个模型 + 一组端点。上游组是后端的实现细节，前端不呈现该概念。

export const ROUTE_MODES = [
  { value: 'auto', label: '自动', hint: '在所选端点里按优先级与健康状态自动挑选' },
  { value: 'manual_preferred', label: '优选', hint: '优先用指定端点，不可用时回落到其余所选端点' },
  { value: 'manual_fixed', label: '固定', hint: '只用指定端点，不可用时直接失败，不回落' }
];

export const ENDPOINT_STATUS_LABELS = {
  ok: '可调度',
  missing: '端点缺失',
  availability_disabled: '硬关闭',
  auto_schedule_disabled: '未参与自动调度',
  cooldown: '冷却中',
  unhealthy: '不可达'
};

// 端点状态到色调的映射：能挡住请求的用玫红，只影响自动排序的用琥珀。
export const endpointStatusTone = (status) => {
  if (['missing', 'availability_disabled', 'unhealthy'].includes(status)) return 'tone-rose';
  if (['cooldown', 'auto_schedule_disabled'].includes(status)) return 'tone-amber';
  return 'tone-emerald';
};

export const routeModeLabel = (mode, target) => {
  const found = ROUTE_MODES.find((item) => item.value === mode);
  if (!found) return mode || 'auto';
  return found.value === 'auto' || !target ? found.label : `${found.label} → ${target}`;
};

export const endpointNames = (binding) => (binding?.endpoints || []).map((item) => item.name);

export const createBindingDraft = (binding) => ({
  assignmentId: binding?.assignment_id ?? null,
  model: binding?.model || '',
  endpoints: endpointNames(binding),
  routeMode: binding?.route_mode || 'auto',
  routeTarget: binding?.route_target || '',
  enabled: binding?.enabled ?? true,
  note: binding?.note || ''
});

// 切换端点选择时保持策略自洽：目标掉出所选集合就退回自动，
// 否则用户会保存出一个后端必定拒绝的组合。
export const toggleDraftEndpoint = (draft, name) => {
  const selected = draft.endpoints.includes(name)
    ? draft.endpoints.filter((item) => item !== name)
    : [...draft.endpoints, name];
  return normalizeDraftTarget({ ...draft, endpoints: selected });
};

export const normalizeDraftTarget = (draft) => {
  if (draft.routeMode === 'auto') return { ...draft, routeTarget: '' };
  if (draft.routeTarget && draft.endpoints.includes(draft.routeTarget)) return draft;
  // 只剩一个候选时直接替用户选上，省掉一次必然的手动操作。
  return { ...draft, routeTarget: draft.endpoints.length === 1 ? draft.endpoints[0] : '' };
};

export const setDraftRouteMode = (draft, routeMode) => normalizeDraftTarget({ ...draft, routeMode });

const MODEL_NAME_MAX = 128;

// 与后端 modelrouting.ValidateAssignment / ValidateGroup 同口径的前置校验，
// 目的是在点保存之前就把错误显示在字段旁，而不是等一次失败的往返。
export const validateBindingDraft = (draft, existingBindings = []) => {
  const model = (draft.model || '').trim();
  if (!model) return '请输入模型名';
  if (model.length > MODEL_NAME_MAX) return `模型名不能超过 ${MODEL_NAME_MAX} 个字符`;
  // 与后端一致：拒空白与控制字符，但连字符、点号是模型名的正常组成部分。
  if (/[\s\p{Cc}]/u.test(model)) return '模型名不能包含空格或控制字符';

  const duplicated = existingBindings.some(
    (item) => item.assignment_id !== draft.assignmentId && item.model.trim().toLowerCase() === model.toLowerCase()
  );
  if (duplicated) return `模型「${model}」已经配置过上游端点`;

  if (draft.endpoints.length === 0) return '至少选择 1 个上游端点';
  if (draft.routeMode !== 'auto' && !draft.routeTarget) return '优选 / 固定模式需要指定一个目标端点';
  if (draft.routeMode !== 'auto' && !draft.endpoints.includes(draft.routeTarget)) return '目标端点必须在已选端点中';
  if ((draft.note || '').length > 256) return '备注不能超过 256 个字符';
  return '';
};

export const bindingPayload = (draft) => ({
  model: (draft.model || '').trim(),
  endpoints: draft.endpoints,
  route_mode: draft.routeMode,
  route_target: draft.routeMode === 'auto' ? '' : draft.routeTarget,
  enabled: draft.enabled,
  note: (draft.note || '').trim()
});

// 端点选择器的过滤：按名称与 URL 匹配，空关键词返回全量。
export const filterEndpoints = (endpoints, keyword) => {
  const needle = (keyword || '').trim().toLowerCase();
  if (!needle) return endpoints;
  return endpoints.filter((endpoint) =>
    `${endpoint.name || ''} ${endpoint.url || ''}`.toLowerCase().includes(needle)
  );
};
