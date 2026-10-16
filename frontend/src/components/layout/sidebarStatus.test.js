import test from 'node:test';
import assert from 'node:assert/strict';
import { resolveClaudeCodeEntryStatus, resolveCodexEntryStatus, resolveMainPortStatus } from './sidebarStatus.js';

test('主端口：通道未连通先报通道，运行中显示端口，否则未运行', () => {
  assert.equal(resolveMainPortStatus('connecting', { running: true, port: 8088 }).name, '连接中…');
  assert.equal(resolveMainPortStatus('disconnected', { running: true, port: 8088 }).port, 0);

  const running = resolveMainPortStatus('connected', { running: true, port: 8088 });
  assert.equal(running.name, '主端口');
  assert.equal(running.port, 8088);
  assert.equal(running.dot, 'bg-success-solid');

  const stopped = resolveMainPortStatus('connected', { running: false, port: 8088 });
  assert.equal(stopped.name, '主端口未运行');
  assert.equal(stopped.dot, 'bg-danger-solid');
});

test('Claude Code 入口：未启用、读不到或没在运行都不占行', () => {
  assert.equal(resolveClaudeCodeEntryStatus(null), null);
  assert.equal(resolveClaudeCodeEntryStatus({ enabled: false, running: false }), null);
  assert.equal(resolveClaudeCodeEntryStatus({ enabled: true, running: false, listenPort: 18088 }), null);
  assert.equal(resolveClaudeCodeEntryStatus({ enabled: true, running: false, listenPort: 18088, error: '端口被占用' }), null);
});

test('Claude Code 入口：运行中行内写简称，悬停写全称与地址', () => {
  const row = resolveClaudeCodeEntryStatus({
    enabled: true, running: true, mode: 'subscription', listenPort: 18088, baseUrl: 'http://127.0.0.1:18088'
  });
  assert.equal(row.name, 'Claude Code');
  assert.equal(row.detail, '订阅');
  assert.equal(row.port, 18088);
  assert.match(row.title, /订阅直通/);
  assert.match(row.title, /127\.0\.0\.1:18088/);

  assert.equal(resolveClaudeCodeEntryStatus({ enabled: true, running: true, mode: 'endpoint', listenPort: 18088 }).detail, '端点');
});

test('Codex 入口：只有运行中才占行，未启用、失败、启停过渡、读不到运行态都不显示', () => {
  assert.equal(resolveCodexEntryStatus({}), null);
  assert.equal(resolveCodexEntryStatus({ config: { enabled: false }, status: { state: 'stopped' } }), null);
  for (const state of ['stopped', 'starting', 'draining', 'failed']) {
    assert.equal(resolveCodexEntryStatus({ config: { enabled: true }, status: { state, lastError: 'x' } }), null, state);
  }
  assert.equal(resolveCodexEntryStatus({ config: { enabled: true }, status: null, statusError: '超时' }), null);
});

test('Codex 入口：运行中按实际模式写简称，端口取监听地址', () => {
  const row = resolveCodexEntryStatus({
    config: { enabled: true, mode: 'subscription', listenPort: 28088 },
    status: { state: 'running', mode: 'pool', listenAddr: '127.0.0.1:28089' }
  });
  assert.equal(row.name, 'Codex');
  assert.equal(row.detail, '账号池');
  assert.equal(row.port, 28089);
  assert.match(row.title, /账号池/);

  const ipv6 = resolveCodexEntryStatus({
    config: { enabled: true, mode: 'subscription', listenPort: 28088 },
    status: { state: 'running', mode: 'subscription', listenAddr: '[::1]:28088' }
  });
  assert.equal(ipv6.detail, '订阅');
  assert.equal(ipv6.port, 28088);
});
