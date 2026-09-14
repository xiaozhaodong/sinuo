import test from 'node:test';
import assert from 'node:assert/strict';

import {
  canPinAccountSelection,
  isSamePinnedAccount,
  resolveDisplayedActiveAccount,
  resolveFallbackRouteLabel
} from './accountSwitcherState.js';

test('resolveDisplayedActiveAccount prefers runtime active selection over primary tier first account', () => {
  const accounts = [
    { id: 1, account_name: 'primary-a', priority: 10, enabled: true, state: 'active' },
    { id: 2, account_name: 'primary-b', priority: 10, enabled: true, state: 'active', is_active_selection: true }
  ];

  const resolved = resolveDisplayedActiveAccount({
    accounts,
    recentSelectedAccountId: null
  });

  assert.equal(resolved?.id, 2);
});

test('resolveDisplayedActiveAccount falls back to recent selected snapshot when runtime selection is missing', () => {
  const accounts = [
    { id: 1, account_name: 'primary-a', priority: 10, enabled: true, state: 'active' },
    { id: 2, account_name: 'primary-b', priority: 10, enabled: true, state: 'active' }
  ];

  const resolved = resolveDisplayedActiveAccount({
    accounts,
    recentSelectedAccountId: 2
  });

  assert.equal(resolved?.id, 2);
});

test('resolveDisplayedActiveAccount keeps runtime pinned target even when latest request temporarily hit backup', () => {
  const accounts = [
    { id: 1, account_name: 'primary-a', priority: 10, enabled: true, state: 'cooldown', is_active_selection: true },
    { id: 2, account_name: 'backup-a', priority: 20, enabled: true, state: 'active' }
  ];

  const resolved = resolveDisplayedActiveAccount({
    accounts,
    recentSelectedAccountId: 2
  });

  assert.equal(resolved?.id, 1);
});

test('isSamePinnedAccount only short-circuits when the displayed account is an actual runtime pin', () => {
  assert.equal(
    isSamePinnedAccount({
      displayedActiveAccount: { id: 2, account_name: 'backup-a' },
      targetAccount: { id: 2, account_name: 'backup-a' }
    }),
    false
  );

  assert.equal(
    isSamePinnedAccount({
      displayedActiveAccount: { id: 2, account_name: 'backup-a', is_active_selection: true },
      targetAccount: { id: 2, account_name: 'backup-a' }
    }),
    true
  );
});

test('resolveFallbackRouteLabel 说明独立入口未运行时请求实际走哪条路', () => {
  // 模型路由优先：原有入口开着模型路由时，固定账号不参与决策
  assert.equal(
    resolveFallbackRouteLabel({ modelRoutingEnabled: true, activeAccount: { account_name: 'primary-a' } }),
    '模型路由'
  );
  // 未开模型路由时显示固定账号名，没有固定账号则回退自动调度
  assert.equal(
    resolveFallbackRouteLabel({ modelRoutingEnabled: false, activeAccount: { account_name: 'primary-a' } }),
    'primary-a'
  );
  assert.equal(
    resolveFallbackRouteLabel({ modelRoutingEnabled: false, activeAccount: { accountName: 'camel-a' } }),
    'camel-a'
  );
  assert.equal(resolveFallbackRouteLabel({ modelRoutingEnabled: false, activeAccount: null }), 'Auto');
  assert.equal(resolveFallbackRouteLabel(), 'Auto');
  // 账号名为空串不能当标签用
  assert.equal(resolveFallbackRouteLabel({ activeAccount: { account_name: '   ' } }), 'Auto');
});

test('canPinAccountSelection still allows temporarily unavailable accounts to be pinned', () => {
  assert.equal(
    canPinAccountSelection({ id: 3, account_name: 'cooldown-a', enabled: true, state: 'cooldown' }),
    true
  );
  assert.equal(
    canPinAccountSelection({ id: 4, account_name: 'quota-a', enabled: true, state: 'active', quota_status: 'exhausted' }),
    true
  );
  assert.equal(
    canPinAccountSelection({ id: 5, account_name: 'disabled-auth', enabled: true, state: 'disabled_auth' }),
    false
  );
  assert.equal(
    canPinAccountSelection({ id: 6, account_name: 'manual-disabled', enabled: false, state: 'active' }),
    false
  );
});
