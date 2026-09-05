// ============================================
// Account Pool 通用纯函数
// 2026-03-07
// ============================================

import { PLAN_TYPE_LABELS } from './constants.js';

const DEFAULT_MODEL_REWRITE_SOURCE = 'gpt-5.4';
const DEFAULT_MODEL_REWRITE_TARGET = 'gpt-5.5';
const CODEX_MODEL_REWRITE_PATHS = ['/v1/responses', '/v1/responses/compact'];

const providerTypeToAuthMethod = (providerType = '') => {
  const type = String(providerType).trim().toLowerCase();
  if (['chatgpt_refresh_token', 'chatgpt_rt', 'refresh_token', 'rt', 'oauth', 'openai_oauth'].includes(type)) return 'chatgpt_refresh_token';
  return 'api_key';
};

const authMethodToProviderType = (authMethod = '') => {
  if (authMethod === 'chatgpt_refresh_token') return 'chatgpt_refresh_token';
  return 'api_key';
};

const toAccountAuthLabel = (providerType = '') => {
  const type = String(providerType).trim().toLowerCase();
  if (['chatgpt_refresh_token', 'chatgpt_rt', 'refresh_token', 'rt', 'oauth', 'openai_oauth'].includes(type)) return 'ChatGPT RT';
  if (type === 'api_key') return 'API Key';
  return providerType || '-';
};

const isAPIKeyProviderType = (providerType = '') => String(providerType).trim().toLowerCase() === 'api_key';

const normalizeModelRewriteSource = (sourceModel = '') => {
  const trimmed = String(sourceModel || '').trim();
  if (!trimmed) {
    return '';
  }
  return trimmed.endsWith('*') ? trimmed.slice(0, -1).trim() : trimmed;
};

const createDefaultModelRewriteRule = () => ({
  source: DEFAULT_MODEL_REWRITE_SOURCE,
  target: DEFAULT_MODEL_REWRITE_TARGET
});

const createDefaultModelRewriteRules = () => [
  createDefaultModelRewriteRule(),
  {
    source: 'gpt-5.4-mini',
    target: DEFAULT_MODEL_REWRITE_TARGET
  }
];

const normalizeModelRewriteFormRule = (rule = {}) => ({
  source: normalizeModelRewriteSource(rule.source ?? rule.from ?? ''),
  target: String(rule.target ?? rule.to ?? '').trim()
});

const buildCodexModelRewriteRules = (options = {}) => {
  const inputRules = Array.isArray(options.rules)
    ? options.rules
    : [{
      source: options.sourceModel ?? DEFAULT_MODEL_REWRITE_SOURCE,
      target: options.targetModel ?? DEFAULT_MODEL_REWRITE_TARGET
    }];
  const rules = inputRules
    .map(normalizeModelRewriteFormRule)
    .filter((rule) => rule.source && rule.target);
  if (!rules.length) {
    return '';
  }
  return JSON.stringify(rules.map((rule) => ({
    paths: CODEX_MODEL_REWRITE_PATHS,
    match: 'exact',
    from: rule.source,
    to: rule.target
  })));
};

const parseCodexModelRewriteSettings = (raw = '') => {
  const fallback = {
    enabled: false,
    rules: createDefaultModelRewriteRules(),
    source: DEFAULT_MODEL_REWRITE_SOURCE,
    target: DEFAULT_MODEL_REWRITE_TARGET
  };
  const text = String(raw || '').trim();
  if (!text) {
    return fallback;
  }

  try {
    const parsed = JSON.parse(text);
    const rules = Array.isArray(parsed) ? parsed : [parsed];
    const formRules = rules
      .filter((item) => {
        if (!item || typeof item !== 'object') {
          return false;
        }
        const from = String(item.from || '').trim();
        const match = String(item.match || 'exact').trim().toLowerCase();
        return from && (match === 'exact' || match === 'prefix') && String(item.to || '').trim();
      })
      .map((item) => normalizeModelRewriteFormRule({
        source: item.from,
        target: item.to
      }))
      .filter((rule) => rule.source && rule.target);
    if (!formRules.length) {
      return fallback;
    }
    return {
      enabled: true,
      rules: formRules,
      source: formRules[0].source,
      target: formRules[0].target
    };
  } catch {
    return fallback;
  }
};

const buildOAuthCredentialRaw = (result = {}) => {
  if (result?.credential_raw) {
    return result.credential_raw;
  }

  const payload = {};
  if (result?.refresh_token) payload.refresh_token = result.refresh_token;
  if (result?.access_token) payload.access_token = result.access_token;
  if (result?.id_token) payload.id_token = result.id_token;
  if (result?.expires_at) payload.expires_at = result.expires_at;
  if (result?.chatgpt_account_id) payload.chatgpt_account_id = result.chatgpt_account_id;
  return JSON.stringify(payload);
};

const normalizePlanType = (value = '') => {
  const normalized = String(value || '').trim().toLowerCase();
  return normalized || '';
};

const toPlanTypeLabel = (value = '') => {
  const normalized = normalizePlanType(value);
  if (!normalized) {
    return '';
  }
  if (PLAN_TYPE_LABELS[normalized]) {
    return PLAN_TYPE_LABELS[normalized];
  }
  return normalized
    .replace(/[_-]+/g, ' ')
    .split(/\s+/)
    .filter(Boolean)
    .map((part) => part[0].toUpperCase() + part.slice(1))
    .join(' ');
};

const toRemainingPercent = (usedPercent) => {
  const used = Number.parseFloat(usedPercent);
  if (!Number.isFinite(used)) {
    return null;
  }
  return Math.max(0, Math.min(100, 100 - used));
};

// 三档实色在深浅两底上都成立；无数据态必须走 token，
// 固定浅灰在暗色下会变成一根亮条，比有数据时还抢眼。
const toQuotaProgressClass = (remainingPercent) => {
  if (!Number.isFinite(remainingPercent)) {
    return 'bg-line-strong';
  }
  if (remainingPercent > 50) {
    return 'bg-success-solid';
  }
  if (remainingPercent > 20) {
    return 'bg-warn-solid';
  }
  return 'bg-danger-solid';
};

const toQuotaStatusLabel = (status = '') => {
  const normalized = String(status || '').trim().toLowerCase();
  const labels = {
    ok: '正常',
    unavailable: '暂不可用',
    exhausted: '已耗尽',
    workspace_deactivated: '工作区停用',
    auth_invalid: '鉴权失效',
    pending: '未刷新'
  };
  return labels[normalized] || labels.pending;
};

const toAccountStateLabel = (state) => {
  const stateMap = {
    active: '可用',
    cooldown: '冷却中',
    disabled_auth: '鉴权失效',
    disabled: '已禁用'
  };
  return stateMap[state] || (state || '未知');
};

const toScheduleOutcomeLabel = (outcome = '') => {
  const normalized = String(outcome || '').trim().toLowerCase();
  const labels = {
    pending: '调度中',
    success: '成功',
    auth_failed: '鉴权失败',
    transient_failure: '瞬时失败',
    invalid_request: '请求无效',
    passthrough_no_available_providers: '透传 503',
    passthrough_other_4xx: '透传 4xx',
    no_schedulable_accounts: '无可调度账号'
  };
  return labels[normalized] || '未知';
};

const toScheduleDecisionLabel = (decision = '') => {
  const normalized = String(decision || '').trim().toLowerCase();
  const labels = {
    selected: '已选中',
    eligible: '可候选',
    skipped: '已跳过'
  };
  return labels[normalized] || '未知';
};

const toScheduleReasonLabel = (reason = '') => {
  const normalized = String(reason || '').trim().toLowerCase();
  const labels = {
    highest_ranked_in_selected_tier: '当前组内排序第一，优先命中',
    retained_active_account_in_selected_tier: '保持当前活跃账号，本轮继续优先命中',
    preferred_account_in_selected_group: '当前组已指定首选账号，本轮优先命中',
    same_tier_lower_rank: '同组顺位靠后，本轮作为后续候选',
    higher_priority_tier_selected: '更高优先组已有可用账号',
    higher_priority_tier_recovered_but_retained_degraded_tier: '更高优先组已恢复，但当前仍保持降级组',
    quota_exhausted_until_reset: '额度已耗尽，等待窗口重置',
    invalid_account: '账号记录异常，未参与调度',
    not_selected: '本轮未被调度'
  };
  return labels[normalized] || '调度器未返回原因标签';
};

const normalizeEntityId = (value) => {
  if (value === null || value === undefined) return null;
  if (typeof value === 'number' && Number.isFinite(value)) return value;
  if (typeof value === 'string') {
    const trimmed = value.trim();
    if (!trimmed) return null;
    const numeric = Number(trimmed);
    return Number.isNaN(numeric) ? trimmed : numeric;
  }
  return null;
};

const resolveAccountId = (account = {}) => {
  if (!account || typeof account !== 'object') {
    return null;
  }

  return normalizeEntityId(
    account.id
    ?? account.ID
    ?? account.Id
    ?? account.account_id
    ?? account.accountId
    ?? account.accountID
    ?? account.AccountID
    ?? null
  );
};

const isValidAccountId = (value) => normalizeEntityId(value) !== null;

const buildManualSwitchSuccessMessage = (accountName = '', targetTier = 'primary', { includeRequestPath = false } = {}) => {
  const safeAccountName = String(accountName || '').trim() || '目标账号';
  const normalizedTargetTier = String(targetTier || 'primary').trim().toLowerCase();
  const groupLabel = normalizedTargetTier === 'cold'
    ? '冷备'
    : normalizedTargetTier === 'backup'
      ? '备组'
      : '主组';
  const usageHint = normalizedTargetTier === 'cold'
    ? '系统会在主组和备组都不可用时再使用它'
    : includeRequestPath
      ? '后续 /v1/responses 会按当前可调度状态优先使用它；若暂时不可调度，恢复后会自动回切'
      : '系统会按当前可调度状态优先使用它；若暂时不可调度，恢复后会自动回切';
  return `已将「${safeAccountName}」移到${groupLabel}，${usageHint}`;
};

const summarizeCallbackURL = (raw = '') => {
  const text = String(raw || '').trim();
  if (!text) {
    return {
      normalized: '',
      hasCode: false,
      hasState: false,
      hasRefreshToken: false,
      hasFragment: false,
      oauthError: '',
      oauthErrorDescription: ''
    };
  }
  try {
    const parsed = new URL(text);
    const hasCode = !!parsed.searchParams.get('code');
    const hasState = !!parsed.searchParams.get('state');
    const hasRefreshToken = !!(parsed.searchParams.get('refresh_token') || parsed.searchParams.get('rt'));
    let oauthError = parsed.searchParams.get('error') || '';
    let oauthErrorDescription = parsed.searchParams.get('error_description') || '';

    let hasFragment = false;
    if (parsed.hash) {
      const fragQuery = parsed.hash.startsWith('#') ? parsed.hash.slice(1) : parsed.hash;
      const fragParams = new URLSearchParams(fragQuery);
      hasFragment = fragParams.toString() !== '';
      if (!oauthError) oauthError = fragParams.get('error') || '';
      if (!oauthErrorDescription) oauthErrorDescription = fragParams.get('error_description') || '';
    }

    return {
      normalized: `${parsed.protocol}//${parsed.host}${parsed.pathname}`,
      hasCode,
      hasState,
      hasRefreshToken,
      hasFragment,
      oauthError,
      oauthErrorDescription
    };
  } catch {
    return {
      normalized: text.slice(0, 160),
      hasCode: text.includes('code='),
      hasState: text.includes('state='),
      hasRefreshToken: text.includes('refresh_token=') || text.includes('rt='),
      hasFragment: text.includes('#'),
      oauthError: text.includes('error=') ? 'unknown' : '',
      oauthErrorDescription: ''
    };
  }
};

const toDateOrNull = (value) => {
  if (!value) {
    return null;
  }
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? null : date;
};

const hasFutureQuotaReset = (account = {}) => {
  const normalizedPlanType = normalizePlanType(account.plan_type || account.planType || '');
  const isExhaustedWindow = (usedValue, resetValue) => {
    const used = Number.parseFloat(usedValue);
    const resetAt = toDateOrNull(resetValue);
    if (!Number.isFinite(used) || !resetAt) {
      return false;
    }
    return used >= 99.999 && resetAt.getTime() > Date.now();
  };

  if (normalizedPlanType === 'free') {
    return isExhaustedWindow(
      account.quota_weekly_used_percent ?? account.quotaWeeklyUsedPercent,
      account.quota_weekly_reset_at ?? account.quotaWeeklyResetAt
    );
  }

  return isExhaustedWindow(
    account.quota_5h_used_percent ?? account.quota5hUsedPercent,
    account.quota_5h_reset_at ?? account.quota5hResetAt
  ) || isExhaustedWindow(
    account.quota_weekly_used_percent ?? account.quotaWeeklyUsedPercent,
    account.quota_weekly_reset_at ?? account.quotaWeeklyResetAt
  );
};

const isAccountSchedulable = (account = {}) => {
  if (!account || account.enabled === false) {
    return false;
  }

  const state = String(account.state || '').trim().toLowerCase();
  if (state === 'disabled_auth') {
    return false;
  }

  const cooldownUntil = toDateOrNull(account.cooldown_until ?? account.cooldownUntil);
  if (state === 'cooldown' && (!cooldownUntil || cooldownUntil.getTime() > Date.now())) {
    return false;
  }

  const quotaStatus = String(account.quota_status || account.quotaStatus || '').trim().toLowerCase();
  if (quotaStatus === 'exhausted' && hasFutureQuotaReset(account)) {
    return false;
  }

  return true;
};

const maskSessionId = (sessionId = '') => {
  const text = String(sessionId || '').trim();
  if (text.length <= 6) return '***';
  return `${text.slice(0, 6)}***`;
};

export {
  authMethodToProviderType,
  buildCodexModelRewriteRules,
  buildOAuthCredentialRaw,
  buildManualSwitchSuccessMessage,
  createDefaultModelRewriteRule,
  createDefaultModelRewriteRules,
  DEFAULT_MODEL_REWRITE_SOURCE,
  DEFAULT_MODEL_REWRITE_TARGET,
  hasFutureQuotaReset,
  isAccountSchedulable,
  isAPIKeyProviderType,
  isValidAccountId,
  maskSessionId,
  normalizeEntityId,
  normalizePlanType,
  parseCodexModelRewriteSettings,
  providerTypeToAuthMethod,
  resolveAccountId,
  summarizeCallbackURL,
  toAccountAuthLabel,
  toAccountStateLabel,
  toPlanTypeLabel,
  toQuotaProgressClass,
  toQuotaStatusLabel,
  toRemainingPercent,
  toScheduleDecisionLabel,
  toScheduleOutcomeLabel,
  toScheduleReasonLabel
};
