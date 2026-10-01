import test from 'node:test';
import assert from 'node:assert/strict';

import {
  canPinAccountSelection,
  isSamePinnedAccount,
  resolveCodexAccessLabel,
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
  // 绑定数已知时随模型路由一起展示；没有绑定要说清楚，而不是显示 0 模型
  assert.equal(resolveFallbackRouteLabel({ modelRoutingEnabled: true, modelRouteBindingCount: 3 }), '模型路由 · 3 模型');
  assert.equal(resolveFallbackRouteLabel({ modelRoutingEnabled: true, modelRouteBindingCount: 0 }), '模型路由 · 尚无绑定');
  assert.equal(
    resolveFallbackRouteLabel({ modelRoutingEnabled: true, modelRouteBindingCount: 3, compact: true }),
    '3 模型'
  );
  assert.equal(
    resolveFallbackRouteLabel({ modelRoutingEnabled: true, modelRouteBindingCount: 0, compact: true }),
    '尚无绑定'
  );
  // 未开模型路由时绑定数不参与标签
  assert.equal(
    resolveFallbackRouteLabel({ modelRoutingEnabled: false, activeAccount: { account_name: 'primary-a' }, modelRouteBindingCount: 3 }),
    'primary-a'
  );
});

test('resolveCodexAccessLabel 主标签始终回答 Codex 请求现在怎么路由', () => {
  const initialized = { initialized: true, enabled: true, mode: 'pool', revision: 3, accessConfigured: true };

  // 未初始化：用户根本没用独立入口，主标签直接显示原有入口策略，入口状态退到 detail
  assert.deepEqual(
    resolveCodexAccessLabel({ config: { initialized: false, enabled: false }, status: { state: 'stopped' }, routeLabel: 'Auto' }),
    { label: 'Auto', detail: '独立入口未初始化' }
  );
  // 主动停用：原有入口照常在跑，不能让「已停用」占主标签
  assert.deepEqual(
    resolveCodexAccessLabel({
      config: { ...initialized, enabled: false }, status: { state: 'stopped', enabled: false }, routeLabel: '模型路由 · 2 模型'
    }),
    { label: '模型路由 · 2 模型', detail: '独立入口已停用' }
  );
  // 运行中账号池模式：入口模式和池策略一起给
  assert.deepEqual(
    resolveCodexAccessLabel({ config: initialized, status: { state: 'running' }, ready: true, routeLabel: 'primary-a' }),
    { label: '账号池 · primary-a', detail: '' }
  );
  // 运行中订阅直通：请求不经过账号池，不显示池策略
  assert.deepEqual(
    resolveCodexAccessLabel({ config: { ...initialized, mode: 'subscription' }, status: { state: 'running' }, ready: true, routeLabel: 'primary-a' }),
    { label: '订阅直通', detail: '' }
  );
  // 运行中但配置与运行态不一致：保留待确认
  assert.deepEqual(
    resolveCodexAccessLabel({ config: initialized, status: { state: 'running' }, ready: false, routeLabel: 'Auto' }),
    { label: '状态待确认', detail: '' }
  );
});

test('resolveCodexAccessLabel 过渡、失败与读取异常仍由入口状态抢标签', () => {
  const initialized = { initialized: true, enabled: true, mode: 'pool', revision: 3, accessConfigured: true };

  for (const [state, expected] of [['starting', '启动中'], ['draining', '停用中'], ['failed', '启动失败'], ['stopped', '未运行']]) {
    assert.deepEqual(
      resolveCodexAccessLabel({ config: initialized, status: { state }, routeLabel: 'Auto' }),
      { label: expected, detail: '' },
      state
    );
  }
  // 停用意图已保存但入口还在跑：不算已停用
  assert.deepEqual(
    resolveCodexAccessLabel({ config: { ...initialized, enabled: false }, status: { state: 'running' }, ready: false, routeLabel: 'Auto' }),
    { label: '状态待确认', detail: '' }
  );
  // 读取失败：配置错误优先，其次运行态错误；未初始化时运行态错误不重要
  assert.deepEqual(
    resolveCodexAccessLabel({ config: null, configError: 'boom', routeLabel: 'Auto' }),
    { label: '配置读取失败', detail: '' }
  );
  assert.deepEqual(
    resolveCodexAccessLabel({ config: initialized, status: null, statusError: 'boom', routeLabel: 'Auto' }),
    { label: '状态未知', detail: '' }
  );
  assert.deepEqual(
    resolveCodexAccessLabel({ config: { initialized: false }, status: null, statusError: 'boom', routeLabel: 'Auto' }),
    { label: 'Auto', detail: '独立入口未初始化' }
  );
  // 只有一次配置都没读到才显示读取中；打开面板触发的刷新不改写已有标签
  assert.deepEqual(resolveCodexAccessLabel({ loading: true, config: null }), { label: '读取中', detail: '' });
  assert.deepEqual(
    resolveCodexAccessLabel({ loading: true, config: initialized, status: { state: 'running' }, ready: true, routeLabel: 'Auto' }),
    { label: '账号池 · Auto', detail: '' }
  );
  assert.deepEqual(resolveCodexAccessLabel(), { label: 'Auto', detail: '独立入口未初始化' });
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
