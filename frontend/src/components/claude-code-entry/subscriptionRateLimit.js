// Claude 订阅直通额度的纯函数：后端状态归一、两个来源合并、窗口剩余、状态提示、刷新冷却与到点重算。
// 额度有两个来源：每个订阅响应头（实时，只有 5h / d7）与请求结束后的主动查询（含按模型周额度、额外用量）。
// 进度条与账号池一样展示「剩余」。

import { toQuotaProgressClass, toRemainingPercent } from '../../pages/account-pool/utils/accountPool.js';

// 两个窗口，标签与账号池一致
export const SUBSCRIPTION_QUOTA_WINDOWS = [
  { key: 'fiveHour', label: '5h' },
  { key: 'sevenDay', label: 'd7' }
];

const SOURCE_LABELS = {
  headers: '请求响应头',
  usage: '官方查询'
};

const CLAIM_LABELS = {
  five_hour: '5 小时额度',
  seven_day: '周额度',
  seven_day_overage_included: '周额度（含额外用量）',
  overage: '额外用量'
};

// rejected 时额外用量处于这些状态，表示请求仍会成功、改为消耗额外用量
const OVERAGE_ACTIVE_STATUSES = new Set(['allowed', 'allowed_warning']);

// 角标文字色，阈值与进度条一致
const BADGE_TONE_CLASSES = {
  'bg-success-solid': 'text-success',
  'bg-warn-solid': 'text-warn',
  'bg-danger-solid': 'text-danger'
};

// 额外用量金额为货币最小单位（如美分）
const MINOR_UNITS_PER_MAJOR = 100;

// setTimeout 的延时上限（约 24.8 天），超出会被当成 0 立即触发
const MAX_TIMER_DELAY_MS = 2 ** 31 - 1;
// 到点后稍等再重算，避开时钟抖动
const RESET_RECHECK_MARGIN_MS = 1000;

const toMillis = (value) => {
  const ms = Date.parse(value || '');
  return Number.isFinite(ms) ? ms : null;
};

const isPast = (value, nowMs) => {
  const ms = toMillis(value);
  return ms !== null && ms <= nowMs;
};

const toFiniteOrNull = (value) => (Number.isFinite(value) ? value : null);

const normalizeWindow = (window) => ({
  usedPercent: toFiniteOrNull(window?.used_percent),
  resetAt: window?.reset_at || ''
});

const normalizeHeaders = (raw) => {
  if (!raw || !raw.captured_at) return null;
  return {
    status: raw.status || '',
    representativeClaim: raw.representative_claim || '',
    resetAt: raw.reset_at || '',
    overageStatus: raw.overage_status || '',
    fiveHour: normalizeWindow(raw.five_hour),
    sevenDay: normalizeWindow(raw.seven_day),
    capturedAt: raw.captured_at
  };
};

const normalizeExtraUsage = (raw) => {
  if (!raw) return null;
  return {
    enabled: Boolean(raw.enabled),
    usedPercent: toFiniteOrNull(raw.used_percent),
    usedCredits: toFiniteOrNull(raw.used_credits),
    monthlyLimit: toFiniteOrNull(raw.monthly_limit),
    currency: raw.currency || ''
  };
};

const normalizeUsage = (raw) => {
  if (!raw || !raw.fetched_at) return null;
  return {
    fiveHour: normalizeWindow(raw.five_hour),
    sevenDay: normalizeWindow(raw.seven_day),
    modelWeekly: (Array.isArray(raw.model_weekly) ? raw.model_weekly : [])
      .filter((item) => item?.label)
      .map((item) => ({ label: item.label, ...normalizeWindow(item) })),
    extraUsage: normalizeExtraUsage(raw.extra_usage),
    fetchedAt: raw.fetched_at
  };
};

/**
 * 归一后端状态（Get / Refresh 返回值与 claude-code-entry:rate-limit 事件共用）；代理未就绪返回 null
 * @param {object|null} raw
 */
export const normalizeSubscriptionQuota = (raw) => {
  if (!raw || typeof raw !== 'object') return null;
  return {
    revision: Number(raw.revision) || 0,
    headers: normalizeHeaders(raw.headers),
    usage: normalizeUsage(raw.usage),
    credentialAvailable: Boolean(raw.credential_available),
    refreshAvailableAt: raw.refresh_available_at || '',
    lastQueriedAt: raw.last_queried_at || '',
    lastError: raw.last_error || ''
  };
};

/**
 * 保留较新的状态：首次读取、刷新返回与推送可能乱序到达，按 revision 取新
 */
export const pickNewerSubscriptionQuota = (current, incoming) => {
  if (!incoming) return current;
  if (!current) return incoming;
  return incoming.revision >= current.revision ? incoming : current;
};

export const hasSubscriptionQuotaData = (quota) => Boolean(quota?.headers || quota?.usage);

/**
 * 5h / d7 取两个来源中较新的一份；较新一份缺该窗口读数时退回另一份
 * @returns {{source:'headers'|'usage'|'', updatedAt:string, fiveHour:object|null, sevenDay:object|null}}
 */
export const resolveSubscriptionWindows = (quota) => {
  const headers = quota?.headers || null;
  const usage = quota?.usage || null;
  const headersNewer = Boolean(headers) && (!usage || toMillis(headers.capturedAt) >= toMillis(usage.fetchedAt));
  const [primary, secondary] = headersNewer ? [headers, usage] : [usage, headers];
  const pick = (key) => {
    const preferred = primary?.[key] || null;
    if (preferred && preferred.usedPercent !== null) return preferred;
    return secondary?.[key] || preferred;
  };
  return {
    source: primary ? (headersNewer ? 'headers' : 'usage') : '',
    updatedAt: primary ? (headersNewer ? headers.capturedAt : usage.fetchedAt) : '',
    fiveHour: pick('fiveHour'),
    sevenDay: pick('sevenDay')
  };
};

export const describeSubscriptionSource = (source) => SOURCE_LABELS[source] || '';

/**
 * 单个窗口的展示；重置时间已过的旧读数不再展示，避免把上个窗口的用量当成现在的
 * @returns {{state:'ok'|'unknown'|'reset', remainingPercent:number|null, progressClass:string, resetAt:string}}
 */
export const describeSubscriptionQuotaWindow = (window, nowMs) => {
  const resetAt = window?.resetAt || '';
  const expired = isPast(resetAt, nowMs);
  const remainingPercent = expired ? null : toRemainingPercent(window?.usedPercent);
  let state = 'ok';
  if (expired) state = 'reset';
  else if (remainingPercent === null) state = 'unknown';
  return { state, remainingPercent, progressClass: toQuotaProgressClass(remainingPercent), resetAt };
};

/**
 * 请求页按钮角标：5h / d7 中剩余更少的那个；都没有有效读数时返回 null
 * @returns {{label:string, remainingPercent:number, toneClass:string}|null}
 */
export const describeSubscriptionQuotaBadge = (quota, nowMs) => {
  const windows = resolveSubscriptionWindows(quota);
  const candidates = SUBSCRIPTION_QUOTA_WINDOWS
    .map(({ key, label }) => ({ label, view: describeSubscriptionQuotaWindow(windows[key], nowMs) }))
    .filter(({ view }) => view.state === 'ok');
  if (candidates.length === 0) return null;
  const tightest = candidates.reduce((min, item) => (item.view.remainingPercent < min.view.remainingPercent ? item : min));
  return {
    label: tightest.label,
    remainingPercent: tightest.view.remainingPercent,
    toneClass: BADGE_TONE_CLASSES[tightest.view.progressClass] || 'text-fg-subtle'
  };
};

/**
 * 按钮悬停提示里的额度摘要，如「5h 剩余 72%，d7 剩余 40%」；没有有效读数返回空串
 */
export const describeSubscriptionQuotaSummary = (quota, nowMs) => {
  const windows = resolveSubscriptionWindows(quota);
  return SUBSCRIPTION_QUOTA_WINDOWS
    .map(({ key, label }) => ({ label, view: describeSubscriptionQuotaWindow(windows[key], nowMs) }))
    .filter(({ view }) => view.state === 'ok')
    .map(({ label, view }) => `${label} 剩余 ${view.remainingPercent.toFixed(0)}%`)
    .join('，');
};

const formatMinorAmount = (value) => (value / MINOR_UNITS_PER_MAJOR).toFixed(2);

/**
 * 额外用量一行文案；未开启或没有读数时返回空串
 */
export const describeSubscriptionExtraUsage = (extraUsage) => {
  if (!extraUsage?.enabled || extraUsage.usedCredits === null) return '';
  const currency = extraUsage.currency ? ` ${extraUsage.currency}` : '';
  const limit = extraUsage.monthlyLimit !== null ? ` / ${formatMinorAmount(extraUsage.monthlyLimit)}` : '';
  const percent = extraUsage.usedPercent !== null ? `（${extraUsage.usedPercent.toFixed(0)}%）` : '';
  return `已用 ${formatMinorAmount(extraUsage.usedCredits)}${limit}${currency}${percent}`;
};

/**
 * 额度状态提示（取自响应头）：rejected 区分「请求被拒绝」与「正在消耗额外用量」；重置时间已过的旧状态不再提示
 * @returns {{tone:'danger'|'warn', title:string, claimLabel:string, resetAt:string}|null}
 */
export const describeSubscriptionRateLimitAlert = (headers, nowMs) => {
  if (!headers) return null;
  const claimWindow = { five_hour: headers.fiveHour, seven_day: headers.sevenDay }[headers.representativeClaim];
  const resetAt = headers.resetAt || claimWindow?.resetAt || '';
  if (isPast(resetAt, nowMs)) return null;
  const claimLabel = CLAIM_LABELS[headers.representativeClaim] || headers.representativeClaim;
  if (headers.status === 'rejected') {
    return OVERAGE_ACTIVE_STATUSES.has(headers.overageStatus)
      ? { tone: 'warn', title: '订阅额度已用尽，正在消耗额外用量', claimLabel, resetAt }
      : { tone: 'danger', title: '订阅额度已用尽，请求会被官方拒绝', claimLabel, resetAt };
  }
  if (headers.status === 'allowed_warning') {
    return { tone: 'warn', title: '订阅额度接近上限', claimLabel, resetAt };
  }
  return null;
};

/**
 * 手动刷新按钮状态；冷却与退避由后端给出最早可用时间
 * @returns {{disabled:boolean, reason:'refreshing'|'no_credential'|'cooldown'|'', availableAt:string}}
 */
export const describeSubscriptionRefresh = (quota, nowMs, refreshing) => {
  if (refreshing) return { disabled: true, reason: 'refreshing', availableAt: '' };
  if (!quota?.credentialAvailable) return { disabled: true, reason: 'no_credential', availableAt: '' };
  const availableMs = toMillis(quota.refreshAvailableAt);
  if (availableMs !== null && availableMs > nowMs) {
    return { disabled: true, reason: 'cooldown', availableAt: quota.refreshAvailableAt };
  }
  return { disabled: false, reason: '', availableAt: '' };
};

/**
 * 距最近一个未来时间点（各窗口重置、刷新冷却结束）的延时，到点后重算；没有可等的时间返回 null
 */
export const nextSubscriptionRecheckDelay = (quota, nowMs) => {
  if (!quota) return null;
  const { headers, usage } = quota;
  const upcoming = [
    headers?.resetAt, headers?.fiveHour?.resetAt, headers?.sevenDay?.resetAt,
    usage?.fiveHour?.resetAt, usage?.sevenDay?.resetAt,
    ...(usage?.modelWeekly || []).map((item) => item.resetAt),
    quota.refreshAvailableAt
  ]
    .map(toMillis)
    .filter((ms) => ms !== null && ms > nowMs);
  if (upcoming.length === 0) return null;
  return Math.min(Math.min(...upcoming) - nowMs + RESET_RECHECK_MARGIN_MS, MAX_TIMER_DELAY_MS);
};
