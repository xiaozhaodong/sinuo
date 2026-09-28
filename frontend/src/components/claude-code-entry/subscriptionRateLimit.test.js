import test from 'node:test';
import assert from 'node:assert/strict';

import {
  describeSubscriptionExtraUsage,
  describeSubscriptionQuotaBadge,
  describeSubscriptionQuotaSummary,
  describeSubscriptionQuotaWindow,
  describeSubscriptionRateLimitAlert,
  describeSubscriptionRefresh,
  hasSubscriptionQuotaData,
  nextSubscriptionRecheckDelay,
  normalizeSubscriptionQuota,
  pickNewerSubscriptionQuota,
  resolveSubscriptionWindows
} from './subscriptionRateLimit.js';

const NOW = Date.parse('2026-09-27T03:00:00.000000Z');
const FUTURE = '2026-09-27T05:00:00.000000Z';
const PAST = '2026-09-27T02:00:00.000000Z';

const rawHeaders = (overrides = {}) => ({
  status: 'allowed',
  representative_claim: 'five_hour',
  reset_at: FUTURE,
  overage_status: '',
  five_hour: { used_percent: 29, reset_at: FUTURE },
  seven_day: { used_percent: null, reset_at: '' },
  captured_at: '2026-09-27T02:59:00.000000Z',
  ...overrides
});

const rawUsage = (overrides = {}) => ({
  five_hour: { used_percent: 40, reset_at: FUTURE },
  seven_day: { used_percent: 70, reset_at: '2026-09-30T00:00:00.000000Z' },
  model_weekly: [{ label: 'Sonnet', used_percent: 12, reset_at: '2026-09-29T00:00:00.000000Z' }],
  extra_usage: null,
  fetched_at: '2026-09-27T02:50:00.000000Z',
  ...overrides
});

const rawQuota = (overrides = {}) => ({
  revision: 3,
  headers: rawHeaders(),
  usage: null,
  credential_available: true,
  refresh_available_at: '',
  last_queried_at: '',
  last_error: '',
  ...overrides
});

const quotaOf = (overrides) => normalizeSubscriptionQuota(rawQuota(overrides));

test('归一：代理未就绪返回 null，缺失的已用比例保持 null 而不是 0', () => {
  assert.equal(normalizeSubscriptionQuota(null), null);

  const quota = quotaOf({ headers: rawHeaders({ seven_day: undefined }), usage: rawUsage({ model_weekly: [{ label: '', used_percent: 1 }, { label: 'Fable', used_percent: null, reset_at: '' }] }) });
  assert.deepEqual(quota.headers.fiveHour, { usedPercent: 29, resetAt: FUTURE });
  assert.deepEqual(quota.headers.sevenDay, { usedPercent: null, resetAt: '' });
  assert.deepEqual(quota.usage.modelWeekly, [{ label: 'Fable', usedPercent: null, resetAt: '' }]);
  assert.equal(quota.usage.extraUsage, null);
  assert.equal(quota.credentialAvailable, true);

  const empty = quotaOf({ headers: null, usage: null, credential_available: false });
  assert.equal(hasSubscriptionQuotaData(empty), false);
  assert.equal(hasSubscriptionQuotaData(quota), true);
});

test('乱序到达时按 revision 保留较新的状态，同 revision 取后到的', () => {
  const older = quotaOf({ revision: 2 });
  const newer = quotaOf({ revision: 5 });
  const sameAsNewer = quotaOf({ revision: 5, last_error: 'x' });
  assert.equal(pickNewerSubscriptionQuota(null, older), older);
  assert.equal(pickNewerSubscriptionQuota(newer, older), newer);
  assert.equal(pickNewerSubscriptionQuota(older, newer), newer);
  assert.equal(pickNewerSubscriptionQuota(newer, null), newer);
  assert.equal(pickNewerSubscriptionQuota(newer, sameAsNewer), sameAsNewer);
});

test('合并窗口：取较新的来源，较新来源缺读数的窗口退回另一来源', () => {
  const headersNewer = resolveSubscriptionWindows(quotaOf({ usage: rawUsage() }));
  assert.equal(headersNewer.source, 'headers');
  assert.equal(headersNewer.updatedAt, '2026-09-27T02:59:00.000000Z');
  assert.equal(headersNewer.fiveHour.usedPercent, 29);
  assert.equal(headersNewer.sevenDay.usedPercent, 70, '响应头没带 7d 时用查询结果补上');

  const usageNewer = resolveSubscriptionWindows(quotaOf({ usage: rawUsage({ fetched_at: '2026-09-27T02:59:30.000000Z' }) }));
  assert.equal(usageNewer.source, 'usage');
  assert.equal(usageNewer.fiveHour.usedPercent, 40);

  const onlyUsage = resolveSubscriptionWindows(quotaOf({ headers: null, usage: rawUsage() }));
  assert.equal(onlyUsage.source, 'usage');
  assert.equal(onlyUsage.sevenDay.usedPercent, 70);

  const none = resolveSubscriptionWindows(quotaOf({ headers: null }));
  assert.deepEqual(none, { source: '', updatedAt: '', fiveHour: null, sevenDay: null });
});

test('窗口展示剩余比例，超额截断到 0；缺字段显示未知，已过重置时间不展示旧读数', () => {
  const view = describeSubscriptionQuotaWindow({ usedPercent: 29, resetAt: FUTURE }, NOW);
  assert.equal(view.state, 'ok');
  assert.equal(view.remainingPercent, 71);
  assert.equal(view.progressClass, 'bg-success-solid');

  const exhausted = describeSubscriptionQuotaWindow({ usedPercent: 102, resetAt: FUTURE }, NOW);
  assert.equal(exhausted.remainingPercent, 0);
  assert.equal(exhausted.progressClass, 'bg-danger-solid');

  const unknown = describeSubscriptionQuotaWindow(null, NOW);
  assert.equal(unknown.state, 'unknown');
  assert.equal(unknown.progressClass, 'bg-line-strong');

  const reset = describeSubscriptionQuotaWindow({ usedPercent: 95, resetAt: PAST }, NOW);
  assert.equal(reset.state, 'reset');
  assert.equal(reset.remainingPercent, null);
});

test('按钮角标取剩余更少的窗口并按阈值着色；没有有效读数时不显示', () => {
  const badge = describeSubscriptionQuotaBadge(quotaOf({ usage: rawUsage() }), NOW);
  assert.deepEqual(badge, { label: 'd7', remainingPercent: 30, toneClass: 'text-warn' });
  assert.equal(describeSubscriptionQuotaSummary(quotaOf({ usage: rawUsage() }), NOW), '5h 剩余 71%，d7 剩余 30%');

  const expired = quotaOf({ headers: rawHeaders({ five_hour: { used_percent: 99, reset_at: PAST } }) });
  assert.equal(describeSubscriptionQuotaBadge(expired, NOW), null, '重置时间已过的读数不参与角标');
  assert.equal(describeSubscriptionQuotaSummary(expired, NOW), '');
  assert.equal(describeSubscriptionQuotaBadge(null, NOW), null);

  const danger = describeSubscriptionQuotaBadge(quotaOf({ headers: rawHeaders({ five_hour: { used_percent: 90, reset_at: FUTURE } }) }), NOW);
  assert.equal(danger.toneClass, 'text-danger');
});

test('额外用量：金额按最小单位换算，未开启或没有读数不显示', () => {
  assert.equal(describeSubscriptionExtraUsage({ enabled: true, usedPercent: 24.68, usedCredits: 1234, monthlyLimit: 5000, currency: 'USD' }), '已用 12.34 / 50.00 USD（25%）');
  assert.equal(describeSubscriptionExtraUsage({ enabled: true, usedPercent: null, usedCredits: 50, monthlyLimit: null, currency: '' }), '已用 0.50');
  assert.equal(describeSubscriptionExtraUsage({ enabled: false, usedPercent: 10, usedCredits: 100, monthlyLimit: 1000, currency: 'USD' }), '');
  assert.equal(describeSubscriptionExtraUsage(null), '');
});

test('提示：rejected 区分请求被拒绝与正在消耗额外用量', () => {
  const rejected = describeSubscriptionRateLimitAlert(quotaOf({ headers: rawHeaders({ status: 'rejected' }) }).headers, NOW);
  assert.equal(rejected.tone, 'danger');
  assert.match(rejected.title, /拒绝/);
  assert.equal(rejected.claimLabel, '5 小时额度');
  assert.equal(rejected.resetAt, FUTURE);

  const overage = describeSubscriptionRateLimitAlert(quotaOf({ headers: rawHeaders({ status: 'rejected', overage_status: 'allowed_warning' }) }).headers, NOW);
  assert.equal(overage.tone, 'warn');
  assert.match(overage.title, /额外用量/);
});

test('提示：整体重置时间缺失时取所卡窗口的重置时间，已过期或 allowed 不提示，未知窗口保留原值', () => {
  const alertOf = (overrides) => describeSubscriptionRateLimitAlert(quotaOf({ headers: rawHeaders(overrides) }).headers, NOW);
  assert.equal(alertOf({ status: 'rejected', reset_at: '' }).resetAt, FUTURE);
  assert.equal(alertOf({ status: 'rejected', reset_at: PAST }), null);
  assert.equal(alertOf({}), null);
  assert.equal(describeSubscriptionRateLimitAlert(null, NOW), null);

  const warning = alertOf({ status: 'allowed_warning', representative_claim: 'seven_day_oi_future' });
  assert.equal(warning.tone, 'warn');
  assert.equal(warning.claimLabel, 'seven_day_oi_future');
});

test('刷新按钮：查询中、没有凭据、冷却中置灰，冷却结束后可用', () => {
  const cooling = quotaOf({ refresh_available_at: '2026-09-27T03:00:30.000000Z' });
  assert.deepEqual(describeSubscriptionRefresh(cooling, NOW, true), { disabled: true, reason: 'refreshing', availableAt: '' });
  assert.equal(describeSubscriptionRefresh(quotaOf({ credential_available: false }), NOW, false).reason, 'no_credential');
  assert.deepEqual(describeSubscriptionRefresh(cooling, NOW, false), { disabled: true, reason: 'cooldown', availableAt: '2026-09-27T03:00:30.000000Z' });
  assert.equal(describeSubscriptionRefresh(cooling, NOW + 30 * 1000, false).disabled, false);
  assert.equal(describeSubscriptionRefresh(quotaOf(), NOW, false).disabled, false);
  assert.equal(describeSubscriptionRefresh(null, NOW, false).reason, 'no_credential');
});

test('到点重算：取各窗口重置与刷新冷却中最近的未来时间，远期时间不超过定时器上限', () => {
  assert.equal(nextSubscriptionRecheckDelay(null, NOW), null);

  const cooling = quotaOf({ headers: rawHeaders({ reset_at: PAST }), refresh_available_at: '2026-09-27T03:01:00.000000Z' });
  assert.equal(nextSubscriptionRecheckDelay(cooling, NOW), 60 * 1000 + 1000);

  const modelSoonest = quotaOf({
    headers: null,
    usage: rawUsage({ five_hour: { used_percent: 1, reset_at: PAST }, seven_day: { used_percent: 1, reset_at: '' }, model_weekly: [{ label: 'Sonnet', used_percent: 1, reset_at: '2026-09-27T04:00:00.000000Z' }] })
  });
  assert.equal(nextSubscriptionRecheckDelay(modelSoonest, NOW), 3600 * 1000 + 1000);

  const allPast = quotaOf({ headers: rawHeaders({ reset_at: PAST, five_hour: { used_percent: 1, reset_at: PAST } }) });
  assert.equal(nextSubscriptionRecheckDelay(allPast, NOW), null);

  const farFuture = quotaOf({ headers: rawHeaders({ reset_at: '2027-09-27T00:00:00.000000Z', five_hour: { used_percent: 1, reset_at: '' } }) });
  assert.equal(nextSubscriptionRecheckDelay(farFuture, NOW), 2 ** 31 - 1);
});
