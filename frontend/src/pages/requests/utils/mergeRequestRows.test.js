import test from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';

import { mergeRequestRows, COMPARED_FIELDS } from './mergeRequestRows.js';
import { TABLE_COLUMNS } from './constants.js';

const row = (overrides = {}) => ({
  requestId: 'req-1',
  timestamp: '2026-08-28T14:25:57.000000Z',
  status: 'completed',
  model: 'claude-opus-5',
  requestFamily: 'claude',
  upstreamName: 'primary',
  duration: 1200,
  firstTokenMs: 300,
  completionMs: 900,
  isStreaming: true,
  inputTokens: 100,
  outputTokens: 200,
  cacheCreationTokens: 0,
  cacheReadTokens: 50,
  cost: 0.0012,
  ...overrides
});

test('会话字段变化更新行，未变化保持引用', () => {
  const previous = row({ sessionId: 'session-a' });
  const next = row({ sessionId: 'session-b' });
  assert.equal(mergeRequestRows([previous], [next])[0], next);
  assert.equal(mergeRequestRows([previous], [row({ sessionId: 'session-a' })])[0], previous);
});

test('未变化的行复用旧引用，变化的行使用新对象', () => {
  const prevA = row({ requestId: 'req-a' });
  const prevB = row({ requestId: 'req-b', outputTokens: 10 });
  const nextA = row({ requestId: 'req-a' });
  const nextB = row({ requestId: 'req-b', outputTokens: 42 });

  const merged = mergeRequestRows([prevA, prevB], [nextA, nextB]);

  assert.equal(merged[0], prevA, 'req-a 渲染字段全等，应复用旧引用');
  assert.equal(merged[1], nextB, 'req-b 的 outputTokens 变了，应换成新对象');
  assert.equal(merged[1].outputTokens, 42);
});

test('新到达的请求原样保留，且不改变顺序与长度', () => {
  const prevA = row({ requestId: 'req-a' });
  const incoming = row({ requestId: 'req-new', status: 'forwarding' });
  const nextA = row({ requestId: 'req-a' });

  const merged = mergeRequestRows([prevA], [incoming, nextA]);

  assert.equal(merged.length, 2);
  assert.equal(merged[0], incoming, '新行没有旧引用可复用');
  assert.equal(merged[1], prevA, '已存在的行仍复用旧引用');
});

test('进行中的请求推进状态时必须换新引用', () => {
  const prev = row({ requestId: 'req-x', status: 'pending', duration: 0 });
  const next = row({ requestId: 'req-x', status: 'processing', duration: 0 });

  const merged = mergeRequestRows([prev], [next]);

  assert.equal(merged[0], next);
  assert.equal(merged[0].status, 'processing');
});

test('每个比对字段单独变化都能被检出', () => {
  const prev = row({ requestId: 'req-f' });
  for (const field of COMPARED_FIELDS) {
    if (field === 'requestId') continue;
    const mutated = row({ requestId: 'req-f', [field]: '__changed__' });
    const merged = mergeRequestRows([prev], [mutated]);
    assert.equal(merged[0], mutated, `字段 ${field} 变化未被检出`);
  }
});

test('连续重试时详情字段推进必须换新引用', () => {
  // status / tokens / duration 全都没动，只有重试链路在推进 —— 表格看不出区别，
  // 但详情面板的首帧读的就是这个对象。这是引用复用最容易吃掉数据的场景。
  const prev = row({ requestId: 'req-r', status: 'retry', retryCount: 1, httpStatusCode: 429 });
  const next = row({ requestId: 'req-r', status: 'retry', retryCount: 2, httpStatusCode: 500 });

  const merged = mergeRequestRows([prev], [next]);

  assert.equal(merged[0], next, '重试次数与状态码推进了，不能复用旧引用');
  assert.equal(merged[0].retryCount, 2);
});

test('空的上一页或非数组输入不会抛错', () => {
  const next = [row()];
  assert.equal(mergeRequestRows([], next), next);
  assert.equal(mergeRequestRows(null, next), next);
  assert.deepEqual(mergeRequestRows([row()], null), []);
});

test('缺少 requestId 的行按原样透传', () => {
  const broken = { status: 'completed' };
  const merged = mergeRequestRows([row()], [broken]);
  assert.equal(merged[0], broken);
});

test('COMPARED_FIELDS 覆盖所有可见列所依赖的字段', () => {
  // 列 id 与行字段名一一对应的列，必须出现在 COMPARED_FIELDS 中。
  // duration 列额外依赖 firstTokenMs / completionMs / isStreaming（见 RequestTimingCell）。
  const columnBackedFields = TABLE_COLUMNS.map((column) => column.id);
  const missing = columnBackedFields.filter((field) => !COMPARED_FIELDS.includes(field));
  assert.deepEqual(missing, [], `新增列后忘记同步 COMPARED_FIELDS：${missing.join(', ')}`);
});

test('COMPARED_FIELDS 覆盖详情面板与生命周期面板读到的字段', () => {
  // 行对象不只喂给表格：选中后它是详情面板的先行渲染数据源。
  // 静态扫描这两个消费方源码里的 request.xxx，任何一个漏进比对表，
  // 都会让该字段的变化被引用复用吃掉。
  const consumers = [
    '../components/RequestDetailPane.jsx',
    '../components/LifecyclePanel.jsx'
  ];

  const readFields = new Set();
  for (const relativePath of consumers) {
    const source = readFileSync(new URL(relativePath, import.meta.url), 'utf8');
    for (const match of source.matchAll(/\brequest\.([a-zA-Z_][a-zA-Z0-9_]*)/g)) {
      readFields.add(match[1]);
    }
  }

  assert.ok(readFields.size > 0, '没扫到任何字段，正则或路径失效了');

  const missing = [...readFields].filter((field) => !COMPARED_FIELDS.includes(field)).sort();
  assert.deepEqual(missing, [], `详情侧读了但未参与比对的字段：${missing.join(', ')}`);
});

test('首次联系与重试起点推进可触发行更新，值原样保留', () => {
  const previous = row({ upstreamFirstDispatchMs: null, retryStartedAt: '' });
  const next = row({ upstreamFirstDispatchMs: 0, retryStartedAt: '2026-10-30T00:00:02.000000Z' });
  const merged = mergeRequestRows([previous], [next]);
  assert.equal(merged[0], next);
  assert.equal(merged[0].upstreamFirstDispatchMs, 0);
  assert.equal(merged[0].retryStartedAt, next.retryStartedAt);
});
