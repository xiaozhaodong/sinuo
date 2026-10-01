import test from 'node:test';
import assert from 'node:assert/strict';

import {
  buildClaudeCodeSettingsSnippet,
  buildClaudeCodeTrialCommand,
  CLAUDE_CODE_ENTRY_MODE_OPTIONS,
  describeClaudeCodeEntryMode,
  describeClaudeCodeEntryState,
  parseClaudeCodeEntryPort,
  resolveClaudeAccessLabel
} from './claudeCodeEntryViewModel.js';

test('端口只接受 1-65535 的整数', () => {
  assert.equal(parseClaudeCodeEntryPort('18088'), 18088);
  assert.equal(parseClaudeCodeEntryPort(' 1 '), 1);
  assert.equal(parseClaudeCodeEntryPort(65535), 65535);
  for (const invalid of ['', '0', '65536', '-1', '80.5', 'abc', null, undefined]) {
    assert.equal(parseClaudeCodeEntryPort(invalid), null, `${invalid} 应判为非法`);
  }
});

test('状态徽章：启动失败优先于运行态与启用意图', () => {
  assert.equal(describeClaudeCodeEntryState(null).label, '加载中');
  assert.equal(describeClaudeCodeEntryState({ enabled: true, running: false, error: '端口被占用' }).label, '启动失败');
  assert.equal(describeClaudeCodeEntryState({ enabled: true, running: true, error: '' }).label, '运行中');
  assert.equal(describeClaudeCodeEntryState({ enabled: true, running: false, error: '' }).label, '未运行');
  assert.equal(describeClaudeCodeEntryState({ enabled: false, running: false, error: '' }).label, '未启用');
});

test('配置片段与试用命令只设置入口地址', () => {
  const snippet = buildClaudeCodeSettingsSnippet('http://127.0.0.1:18088');
  assert.deepEqual(JSON.parse(snippet), { env: { ANTHROPIC_BASE_URL: 'http://127.0.0.1:18088' } });
  assert.equal(buildClaudeCodeTrialCommand('http://127.0.0.1:18088'), 'ANTHROPIC_BASE_URL=http://127.0.0.1:18088 claude');
});

test('接入模式只有订阅直通与端点，未知值回落到订阅直通', () => {
  assert.deepEqual(CLAUDE_CODE_ENTRY_MODE_OPTIONS.map((option) => option.value), ['subscription', 'endpoint']);
  assert.equal(describeClaudeCodeEntryMode('endpoint').label, '端点');
  assert.equal(describeClaudeCodeEntryMode('auto').value, 'subscription');
});

test('请求页按钮标签：入口未启用时显示端点策略，运行中按接入模式显示', () => {
  const running = { enabled: true, running: true, error: '' };
  assert.deepEqual(resolveClaudeAccessLabel({ loading: true }), { label: '读取中', detail: '' });
  assert.equal(resolveClaudeAccessLabel({ error: 'boom', routeLabel: 'Auto' }).label, 'Auto');
  assert.deepEqual(
    resolveClaudeAccessLabel({ info: { enabled: false }, routeLabel: '模型路由 · 3 模型' }),
    { label: '模型路由 · 3 模型', detail: 'Claude Code 入口未启用' }
  );
  assert.equal(resolveClaudeAccessLabel({ info: { enabled: true, error: '端口被占用' } }).label, '入口启动失败');
  assert.equal(resolveClaudeAccessLabel({ info: { enabled: true, running: false, error: '' } }).label, '入口未运行');
  assert.equal(resolveClaudeAccessLabel({ info: { ...running, mode: 'subscription' } }).label, '订阅');
  assert.equal(resolveClaudeAccessLabel({ info: { ...running, mode: 'endpoint' }, routeLabel: 'Auto · vendor' }).label, '端点 · Auto · vendor');
});
