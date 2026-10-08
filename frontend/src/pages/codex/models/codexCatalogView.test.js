import test from 'node:test';
import assert from 'node:assert/strict';
import {
  codexModelNeedsAttention,
  codexModelStatusTags,
  filterCodexModels,
  formatContextWindow,
  summarizeCodexModel
} from './codexCatalogView.js';

const model = (id, metadata = {}, overrides = {}) => ({
  id, display_name: id, enabled: true, deprecated: false,
  capabilities_json: JSON.stringify(metadata), capabilities_error: '', ...overrides
});

test('上下文窗口只接受正整数，并按 K / M 保留最多一位小数', () => {
  for (const value of [undefined, null, 0, -1, 1.5, NaN, Infinity, '1000']) {
    assert.equal(formatContextWindow(value), '—');
  }
  for (const [value, expected] of [[999, '999'], [1000, '1K'], [272000, '272K'], [1050000, '1.1M'], [1000000, '1M']]) {
    assert.equal(formatContextWindow(value), expected);
  }
});

test('能力摘要使用上下文与等级原顺序，标题包含全部等级', () => {
  assert.deepEqual(summarizeCodexModel(model('a', {
    context_window: 272000, visibility: 'hide',
    supported_reasoning_levels: [{ effort: 'low' }, { effort: 'medium' }, { effort: 'xhigh' }]
  })), { contextWindow: '272K', reasoningRange: 'low–xhigh', reasoningTitle: 'low、medium、xhigh', visibility: 'hide' });
  assert.equal(summarizeCodexModel(model('a', { supported_reasoning_levels: [{ effort: 'high' }] })).reasoningRange, 'high');
});

test('能力缺失或 JSON 损坏时摘要为空，不阻断列表', () => {
  for (const capabilities_json of ['', '{invalid']) {
    assert.deepEqual(summarizeCodexModel(model('a', {}, { capabilities_json })), {
      contextWindow: '—', reasoningRange: '—', reasoningTitle: '', visibility: undefined
    });
  }
});

test('状态标签按无效、弃用、隐藏排序，并保留错误与隐藏说明', () => {
  assert.deepEqual(codexModelStatusTags(model('a', { visibility: 'hide' }, { capabilities_error: '错误原文', deprecated: true })), [
    { key: 'invalid', label: '能力无效', tone: 'danger', title: '错误原文' },
    { key: 'deprecated', label: '已弃用', tone: 'muted' },
    { key: 'hidden', label: '菜单隐藏', tone: 'muted', title: '/v1/models 中保留，Codex 模型菜单不显示' }
  ]);
});

test('待补全、none 与正常可见模型的标签分支', () => {
  assert.deepEqual(codexModelStatusTags(model('a', {}, { capabilities_json: '  ' })), [
    { key: 'incomplete', label: '能力待补全', tone: 'warn', title: '能力待补全：不会出现在 Codex 模型菜单' }
  ]);
  assert.deepEqual(codexModelStatusTags(model('a', {}, { capabilities_json: '', enabled: false })), [
    { key: 'incomplete', label: '能力待补全', tone: 'muted', title: '能力待补全：不会出现在 Codex 模型菜单' }
  ]);
  assert.deepEqual(codexModelStatusTags(model('a', { visibility: 'none' })), [
    { key: 'unlisted', label: '不列出', tone: 'muted' }
  ]);
  assert.deepEqual(codexModelStatusTags(model('a', { visibility: 'list' })), []);
});

test('需处理包含能力无效（不论启用）与已启用的待补全，弃用、隐藏和停用的待补全不计入', () => {
  assert.equal(codexModelNeedsAttention(model('a', {}, { capabilities_error: '错误' })), true);
  assert.equal(codexModelNeedsAttention(model('a', {}, { capabilities_error: '错误', enabled: false })), true);
  assert.equal(codexModelNeedsAttention(model('a', {}, { capabilities_json: '' })), true);
  assert.equal(codexModelNeedsAttention(model('a', {}, { capabilities_json: '', enabled: false, deprecated: true })), false);
  assert.equal(codexModelNeedsAttention(model('a', { visibility: 'hide' }, { deprecated: true })), false);
});

test('筛选不区分大小写匹配 ID 与名称，支持启用和需处理', () => {
  const models = [
    model('alpha'),
    model('beta', {}, { enabled: false, display_name: 'ALPHA', capabilities_json: '' }),
    model('gamma', {}, { capabilities_error: '错误' }),
    model('delta', {}, { capabilities_json: '' })
  ];
  assert.deepEqual(filterCodexModels(models, { query: ' ALPHA ' }).map((entry) => entry.id), ['alpha', 'beta']);
  assert.deepEqual(filterCodexModels(models, { filter: 'enabled' }).map((entry) => entry.id), ['alpha', 'delta', 'gamma']);
  assert.deepEqual(filterCodexModels(models, { filter: 'attention' }).map((entry) => entry.id), ['delta', 'gamma']);
  assert.deepEqual(filterCodexModels(models, { query: 'missing' }), []);
});

test('列表未弃用在前，再按 ID 排序，启用变化不改变顺序且不修改输入', () => {
  const models = [model('a', {}, { deprecated: true }), model('c'), model('b', {}, { enabled: false })];
  assert.deepEqual(filterCodexModels(models).map((entry) => entry.id), ['b', 'c', 'a']);
  assert.deepEqual(filterCodexModels(models.map((entry) => ({ ...entry, enabled: !entry.enabled }))).map((entry) => entry.id), ['b', 'c', 'a']);
  assert.deepEqual(models.map((entry) => entry.id), ['a', 'c', 'b']);
});
