import test from 'node:test';
import assert from 'node:assert/strict';

import {
  buildClaudeCodeSettingsSnippet,
  buildClaudeCodeTrialCommand,
  describeClaudeCodeEntryMode,
  describeClaudeCodeEntryState,
  parseClaudeCodeEntryPort,
  toggleRequestClass
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

test('配置片段与试用命令使用入口地址', () => {
  const snippet = buildClaudeCodeSettingsSnippet('http://127.0.0.1:18088');
  assert.deepEqual(JSON.parse(snippet), { env: { ANTHROPIC_BASE_URL: 'http://127.0.0.1:18088' } });
  assert.equal(buildClaudeCodeTrialCommand('http://127.0.0.1:18088'), 'ANTHROPIC_BASE_URL=http://127.0.0.1:18088 claude');
});

test('模式说明：未知值回落到按模型路由', () => {
  assert.equal(describeClaudeCodeEntryMode('subscription').label, '仅订阅');
  assert.equal(describeClaudeCodeEntryMode('api').label, '仅 API');
  assert.equal(describeClaudeCodeEntryMode('auto').label, '按模型路由');
  assert.equal(describeClaudeCodeEntryMode('unknown').value, 'auto');
});

test('请求类别勾选结果按固定顺序排列', () => {
  assert.deepEqual(toggleRequestClass(['workflow'], 'subagent'), ['subagent', 'workflow']);
  assert.deepEqual(toggleRequestClass(['subagent', 'auxiliary'], 'subagent'), ['auxiliary']);
  assert.deepEqual(toggleRequestClass([], 'compaction'), ['compaction']);
});

test('启用按请求类别路由时配置片段与试用命令带上类别头开关', () => {
  const snippet = JSON.parse(buildClaudeCodeSettingsSnippet('http://127.0.0.1:18088', { hintHeaders: true }));
  assert.deepEqual(snippet.env, {
    ANTHROPIC_BASE_URL: 'http://127.0.0.1:18088',
    CLAUDE_CODE_GATEWAY_HINT_HEADERS: '1'
  });
  assert.equal(
    buildClaudeCodeTrialCommand('http://127.0.0.1:18088', { hintHeaders: true }),
    'CLAUDE_CODE_GATEWAY_HINT_HEADERS=1 ANTHROPIC_BASE_URL=http://127.0.0.1:18088 claude'
  );
});
