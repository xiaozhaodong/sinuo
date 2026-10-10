import test from 'node:test';
import assert from 'node:assert/strict';

import { matchesInventoryFilter } from './useAccountPoolDashboardState.js';
import { buildAccountPoolDashboardModel } from '../utils/dashboardViewModel.js';

const ACCOUNTS = [
  { id: 1, account_name: 'kuaipao', provider_type: 'api_key', state: 'active', enabled: true },
  { id: 2, account_name: 'xiaogmail', provider_type: 'chatgpt_refresh_token', plan_type: 'free', state: 'active', enabled: true },
  { id: 3, account_name: 'cooling', provider_type: 'chatgpt_refresh_token', plan_type: 'plus', state: 'cooldown', enabled: true },
  { id: 4, account_name: 'expired', provider_type: 'chatgpt_refresh_token', plan_type: 'team', state: 'disabled_auth', enabled: true },
  { id: 5, account_name: 'paused', provider_type: 'api_key', state: 'active', enabled: false }
];

const { inventory } = buildAccountPoolDashboardModel({ accounts: ACCOUNTS, timezone: 'Asia/Shanghai' });

// 用下拉选项的真实取值筛一遍，返回命中的账号名
const pick = (field, value) => inventory.rows
  .filter((row) => matchesInventoryFilter(row, field, value))
  .map((row) => row.name)
  .sort();

const optionValues = (options) => options.map((option) => option.value).filter((value) => value !== 'all');

test('授权筛选按 provider_type 精确匹配，ChatGPT RT 能筛出 RT 账号', () => {
  assert.deepEqual(optionValues(inventory.filters.authOptions).sort(), ['api_key', 'chatgpt_refresh_token']);
  assert.deepEqual(pick('auth', 'chatgpt_refresh_token'), ['cooling', 'expired', 'xiaogmail']);
  assert.deepEqual(pick('auth', 'api_key'), ['kuaipao', 'paused']);
});

test('状态筛选按状态键精确匹配，已禁用不会误中鉴权失效', () => {
  assert.deepEqual(pick('status', 'active'), ['kuaipao', 'xiaogmail']);
  assert.deepEqual(pick('status', 'cooldown'), ['cooling']);
  assert.deepEqual(pick('status', 'disabled'), ['paused']);
  assert.deepEqual(pick('status', 'disabled_auth'), ['expired']);
});

test('每个下拉选项都至少命中一个账号', () => {
  const fields = { auth: 'authOptions', plan: 'planOptions', group: 'groupOptions', status: 'statusOptions' };
  for (const [field, optionsKey] of Object.entries(fields)) {
    for (const value of optionValues(inventory.filters[optionsKey])) {
      assert.ok(pick(field, value).length > 0, `${field}=${value} 应至少命中一个账号`);
    }
  }
});

test('计划与组别筛选保持原有结果', () => {
  assert.deepEqual(pick('plan', 'free'), ['xiaogmail']);
  assert.deepEqual(pick('plan', 'prepaid'), ['kuaipao', 'paused']);
  assert.equal(pick('group', inventory.rows[0].groupKey).length > 0, true);
});

test('全部 / 空值不过滤，风险筛选仍走别名匹配', () => {
  assert.equal(pick('auth', 'all').length, ACCOUNTS.length);
  assert.equal(pick('status', '').length, ACCOUNTS.length);
  const healthy = pick('risk', 'healthy');
  assert.ok(healthy.length > 0 && healthy.length < ACCOUNTS.length);
});
