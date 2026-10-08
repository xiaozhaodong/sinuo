import { NO_UPSTREAM_LABEL, isNoUpstreamReason } from './lifecycle.js';

// className 为请求页类型徽章配色：index.css 的 grad-tone 渐变；Other 保持 tone-slate 纯色
export const REQUEST_FAMILY_META = {
  claude: { label: 'Claude', className: 'grad-tone family-tone-claude' },
  codex: { label: 'Codex', className: 'grad-tone family-tone-codex' },
  image: { label: 'Image', className: 'grad-tone family-tone-image' },
  other: { label: 'Other', className: 'tone-slate' }
};

export const normalizeRequestFamily = (value) => {
  const normalized = String(value || '').trim().toLowerCase();
  return REQUEST_FAMILY_META[normalized] ? normalized : 'other';
};

export const getRequestFamilyMeta = (value) => REQUEST_FAMILY_META[normalizeRequestFamily(value)];

export const UNKNOWN_UPSTREAM_LABEL = '未知上游';

// 订阅直通固定标识：库里 upstream_name 按请求族固定（Codex 为 chatgpt-subscription、
// Claude Code 为 claude-subscription），界面统一显示为「订阅直通」，筛选仍用真实存储值。
export const SUBSCRIPTION_UPSTREAM_NAMES = {
  codex: 'chatgpt-subscription',
  claude: 'claude-subscription'
};
export const SUBSCRIPTION_UPSTREAM_LABEL = '订阅直通';

export const isSubscriptionRequest = (request = {}) => (
  (request.upstreamType || request.upstream_type) === 'subscription'
);

// 合成上游标签：不是真实上游名，DB 里对应的 upstream_name 为空。
// 上游筛选按 upstream_name 精确查库，放进选项必然返回 0 条，故排除出候选。
const SYNTHETIC_UPSTREAM_LABELS = new Set([UNKNOWN_UPSTREAM_LABEL, NO_UPSTREAM_LABEL, SUBSCRIPTION_UPSTREAM_LABEL]);

// 空上游归属：候选为空时后端没有端点可归属（endpoint_pipeline.go 的 no_endpoints_available 分支），
// 一律显示「未知上游」会被读成数据丢失，而实际是明确的「没有端点可路由」。
// 归类规则复用 lifecycle.js 的终态规则表，保证与生命周期分段条标签同源。
const resolveMissingUpstreamLabel = (request = {}) => (
  isNoUpstreamReason(request.failureReason || request.failure_reason)
    ? NO_UPSTREAM_LABEL
    : UNKNOWN_UPSTREAM_LABEL
);

export const resolveRequestUpstream = (request = {}) => (
  isSubscriptionRequest(request)
    ? SUBSCRIPTION_UPSTREAM_LABEL
    : (request.upstreamName
      || request.upstream_name
      || request.effectiveEndpoint
      || request.effective_endpoint
      || request.endpoint
      || request.endpoint_name
      || resolveMissingUpstreamLabel(request))
);

export const normalizeRequestSource = (request = {}) => ({
  ...request,
  requestFamily: normalizeRequestFamily(request.requestFamily || request.request_family),
  upstreamName: resolveRequestUpstream(request)
});

// 单条请求的上游筛选候选：返回 { value, label }，不可筛选（合成标签）时返回 null。
// 订阅直通的 value 仍是真实存储的 upstream_name（筛选按精确值查库），只有展示名固定为「订阅直通」；
// withFamily：不限请求族时两家订阅直通同时出现，标签带上请求族区分。
export const resolveUpstreamFilterOption = (request = {}, { withFamily = true } = {}) => {
  const normalized = normalizeRequestSource(request);
  if (isSubscriptionRequest(request)) {
    const value = SUBSCRIPTION_UPSTREAM_NAMES[normalized.requestFamily];
    if (!value) return null;
    const label = withFamily
      ? `${SUBSCRIPTION_UPSTREAM_LABEL} · ${getRequestFamilyMeta(normalized.requestFamily).label}`
      : SUBSCRIPTION_UPSTREAM_LABEL;
    return { value, label };
  }
  const name = normalized.upstreamName;
  return name && !SYNTHETIC_UPSTREAM_LABELS.has(name) ? { value: name, label: name } : null;
};

// 上游筛选候选列表（按请求族过滤、去重、排序）
export const filterUpstreamOptionsByFamily = (requests = [], family = 'all') => {
  const selectedFamily = family === 'all' ? '' : normalizeRequestFamily(family);
  const options = new Map();
  for (const request of requests) {
    if (selectedFamily && normalizeRequestFamily(request.requestFamily || request.request_family) !== selectedFamily) continue;
    const option = resolveUpstreamFilterOption(request, { withFamily: !selectedFamily });
    if (option) options.set(option.value, option);
  }
  return [...options.values()].sort((left, right) => left.label.localeCompare(right.label, 'zh-Hans-CN'));
};

// 当前选中值不在候选里时补一项（范围内已无该上游的数据），保证菜单里能看到勾选项；
// 订阅直通的固定标识仍按「订阅直通」展示。
const selectedUpstreamOption = (value, withFamily) => {
  const family = Object.keys(SUBSCRIPTION_UPSTREAM_NAMES).find((key) => SUBSCRIPTION_UPSTREAM_NAMES[key] === value);
  return family
    ? resolveUpstreamFilterOption({ request_family: family, upstream_type: 'subscription' }, { withFamily })
    : { value, label: value };
};

// 上游筛选候选：后端按查询范围查出的上游（catalog）+ 当前页出现的上游 + 当前选中值。
// 当前页补上在途请求与刚出现的新上游；catalog 为 null（HTTP 环境或查询失败）时只剩当前页。
export const buildUpstreamFilterOptions = ({ catalog = null, requests = [], family = 'all', selected = '' } = {}) => {
  const options = filterUpstreamOptionsByFamily([...(catalog || []), ...requests], family);
  if (!selected || selected === 'all' || options.some((option) => option.value === selected)) {
    return options;
  }
  return [...options, selectedUpstreamOption(selected, family === 'all')];
};
