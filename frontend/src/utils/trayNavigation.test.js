import test from 'node:test';
import assert from 'node:assert/strict';
import { connectTrayNavigation, resolveTrayNavigation } from './trayNavigation.js';

test('托盘导航覆盖请求、今日失败、日志、管理页、模型路由页签与 Codex 接入', () => {
  const expected = {
    requests: { tab: 'requests' }, logs: { tab: 'logs' },
    'requests-failed': { tab: 'requests', requestsFilters: { status: 'failed' } },
    endpoints: { tab: 'endpoints', claudeTab: 'endpoints' },
    'account-pool': { tab: 'account-pool', codexTab: 'accounts' },
    'codex-access': { tab: 'account-pool', codexTab: 'client-access' },
    'claude-model-routing': { tab: 'endpoints', claudeTab: 'routing' },
    'codex-model-routing': { tab: 'account-pool', codexTab: 'routing' }
  };
  for (const [target, route] of Object.entries(expected)) assert.deepEqual(resolveTrayNavigation(target), route);
  for (const invalid of [null, undefined, '', {}, '/logs', 'https://example.com']) assert.equal(resolveTrayNavigation(invalid), null);
});

test('先订阅再告知 Go 就绪，能接到立即发送的导航；卸载后解除订阅', async () => {
  let listener;
  let stopped = false;
  const routes = [];
  const Events = {
    On(name, callback) { assert.equal(name, 'desktop:navigate'); listener = callback; return () => { stopped = true; }; },
    Emit(name) { assert.equal(name, 'desktop:navigation-ready'); listener({ data: 'logs' }); }
  };
  const cleanup = connectTrayNavigation(() => Promise.resolve({ Events }), (route) => routes.push(route), assert.fail);
  await new Promise((resolve) => setImmediate(resolve));
  assert.deepEqual(routes, [{ tab: 'logs' }]);
  listener({ data: 'invalid' });
  assert.equal(routes.length, 1);
  cleanup();
  assert.equal(stopped, true);
});

test('异步初始化期间卸载不能留下订阅，初始化错误要报告', async () => {
  let resolveRuntime;
  const cleanup = connectTrayNavigation(() => new Promise((resolve) => { resolveRuntime = resolve; }), assert.fail, assert.fail);
  cleanup();
  resolveRuntime({ Events: { On: assert.fail, Emit: assert.fail } });
  await new Promise((resolve) => setImmediate(resolve));
  const errors = [];
  connectTrayNavigation(() => Promise.reject(new Error('runtime failed')), assert.fail, (error) => errors.push(error.message));
  await new Promise((resolve) => setImmediate(resolve));
  assert.deepEqual(errors, ['runtime failed']);
});
