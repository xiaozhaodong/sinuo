import { NO_UPSTREAM_LABEL, isNoUpstreamReason } from './lifecycle.js';

export const REQUEST_FAMILY_META = {
  claude: { label: 'Claude', className: 'tone-violet' },
  codex: { label: 'Codex', className: 'tone-sky' },
  image: { label: 'Image', className: 'tone-fuchsia' },
  other: { label: 'Other', className: 'tone-slate' }
};

export const normalizeRequestFamily = (value) => {
  const normalized = String(value || '').trim().toLowerCase();
  return REQUEST_FAMILY_META[normalized] ? normalized : 'other';
};

export const getRequestFamilyMeta = (value) => REQUEST_FAMILY_META[normalizeRequestFamily(value)];

export const UNKNOWN_UPSTREAM_LABEL = '未知上游';

// 订阅直通固定标识：库里 upstream_name 是 chatgpt-subscription，
// 界面统一显示为「订阅直通」，筛选仍用真实存储值。
export const SUBSCRIPTION_UPSTREAM_NAME = 'chatgpt-subscription';
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

// 上游筛选候选：返回 { value, label }，订阅直通的 value 仍是真实存储的
// upstream_name（筛选按精确值查库），只有展示名固定为「订阅直通」。
export const filterUpstreamOptionsByFamily = (requests = [], family = 'all') => {
  const selectedFamily = family === 'all' ? '' : normalizeRequestFamily(family);
  const options = new Map();
  for (const request of requests) {
    const normalized = normalizeRequestSource(request);
    if (selectedFamily && normalized.requestFamily !== selectedFamily) continue;
    if (isSubscriptionRequest(request)) {
      options.set(SUBSCRIPTION_UPSTREAM_NAME, { value: SUBSCRIPTION_UPSTREAM_NAME, label: SUBSCRIPTION_UPSTREAM_LABEL });
      continue;
    }
    const name = normalized.upstreamName;
    if (name && !SYNTHETIC_UPSTREAM_LABELS.has(name)) {
      options.set(name, { value: name, label: name });
    }
  }
  return [...options.values()].sort((left, right) => left.label.localeCompare(right.label, 'zh-Hans-CN'));
};
