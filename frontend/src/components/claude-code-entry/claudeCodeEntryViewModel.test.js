import test from 'node:test';
import assert from 'node:assert/strict';

import {
  buildClaudeCodeSettingsSnippet,
  buildClaudeCodeTrialCommand,
  describeClaudeCodeEntryState,
  parseClaudeCodeEntryPort
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
