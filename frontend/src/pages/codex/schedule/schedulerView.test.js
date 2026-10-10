import test from 'node:test';
import assert from 'node:assert/strict';
import { buildGroupHeader, isLatestHit, buildStatusCounts, buildCandidateRows, countCandidates, buildAccountActionState } from './schedulerView.js';

test('分组标题统计全部账号与可用账号，空组为零', () => {
  assert.deepEqual(buildGroupHeader({ accounts: [{ isAvailable: true }, { isAvailable: false }] }), { total: 2, available: 1 });
  assert.deepEqual(buildGroupHeader(null), { total: 0, available: 0 });
});

test('最近命中按 ID 匹配，不依赖固定标记或同名账号', () => {
  assert.equal(isLatestHit({ id: '12', isActive: false }, { selectedAccountId: 12 }), true);
  assert.equal(isLatestHit({ id: 13, isActive: true, name: '同名' }, { selectedAccountId: 12 }), false);
  assert.equal(isLatestHit({ id: 12 }, {}), false);
  assert.equal(isLatestHit({}, {}), false);
});

test('状态栏汇总主备冷和可用、不可用数量', () => {
  assert.deepEqual(buildStatusCounts([
    { key: 'primary', accounts: [{ isAvailable: true }, { isAvailable: false }] },
    { key: 'backup', accounts: [{ isAvailable: true }] },
    { key: 'cold', accounts: [{ isAvailable: false }] }
  ]), { primary: 2, backup: 1, cold: 1, available: 2, unavailable: 2 });
  assert.deepEqual(buildStatusCounts(), { primary: 0, backup: 0, cold: 0, available: 0, unavailable: 0 });
});

const expectedCandidate = {
  id: 12, accountName: '账号', groupLabel: '备组', priority: 2,
  decision: 'skipped', runtimeOutcome: 'auth_failed', reason: 'invalid_account',
  reasonDetail: '鉴权不可用', quotaStatus: 'auth_invalid', effectiveQuotaRemaining: 0,
  failCount: 3, runtimeError: 'HTTP 401'
};

test('候选字段 snake_case 归一并保留零额度', () => {
  assert.deepEqual(buildCandidateRows({ candidates: [{
    account_id: '12', account_name: '账号', tier_label: '备组', priority: '2',
    decision: ' SKIPPED ', runtime_outcome: 'AUTH_FAILED', reason: 'invalid_account',
    reason_detail: '鉴权不可用', quota_status: 'AUTH_INVALID', effective_quota_remaining: 0,
    fail_count: '3', runtime_error: 'HTTP 401'
  }] }), [expectedCandidate]);
});

test('候选字段 camelCase 与 snake_case 等价', () => {
  assert.deepEqual(buildCandidateRows({ candidates: [{
    accountId: 12, accountName: '账号', tierLabel: '备组', Priority: 2,
    decision: 'skipped', runtimeOutcome: 'auth_failed', reason: 'invalid_account',
    reasonDetail: '鉴权不可用', quotaStatus: 'auth_invalid', effectiveQuotaRemaining: '0',
    failCount: 3, runtimeError: 'HTTP 401'
  }] }), [expectedCandidate]);
});

test('缺失候选和字段时提供展示占位，未返回额度不当成零', () => {
  assert.deepEqual(buildCandidateRows(null), []);
  assert.deepEqual(buildCandidateRows({ candidates: {} }), []);
  const [row] = buildCandidateRows({ candidates: [{}] });
  assert.equal(row.accountName, '-');
  assert.equal(row.groupLabel, '未分组');
  assert.equal(row.priority, null);
  assert.equal(row.quotaStatus, 'pending');
  assert.equal(Number.isNaN(row.effectiveQuotaRemaining), true);
});

test('候选计数区分可候选、命中与跳过', () => {
  assert.deepEqual(countCandidates([{ decision: 'selected' }, { decision: 'eligible' }, { decision: 'skipped' }]), { total: 3, skipped: 1 });
  assert.deepEqual(countCandidates(), { total: 0, skipped: 0 });
});

test('行内与菜单统一首选、固定及各 busy 令牌的禁用规则', () => {
  const account = { id: 12 };
  const group = { key: 'primary' };
  assert.equal(buildAccountActionState(account, group).preferredDisabled, false);
  assert.equal(buildAccountActionState(account, group).pinDisabled, false);
  assert.equal(buildAccountActionState({ ...account, isGroupPreferred: true }, group).preferredDisabled, true);
  const pinned = buildAccountActionState({ ...account, isActive: true }, group);
  assert.equal(pinned.preferredDisabled, true);
  assert.equal(pinned.pinDisabled, true);
  assert.equal(pinned.preferredTitle, '已全局固定，组内首选暂不生效');
  for (const token of ['group-active-primary-12', 'group-swap-primary-swap-down']) {
    assert.equal(buildAccountActionState(account, group, token).preferredDisabled, true);
  }
  assert.equal(buildAccountActionState(account, group, 'group-active-backup-12').preferredDisabled, false);
  assert.equal(buildAccountActionState(account, group, 'scheduler-pin-13').pinDisabled, true);
});
