import test from 'node:test';
import assert from 'node:assert/strict';
import { distributeCells, formatPercent, inputComposition } from './inputComposition.js';

const summary = { version: 1, window: 2000, window_source: 'codex_catalog', items: [
  { group: 'files', name: 'main.go', estimate: 3 }, { group: 'chat', name: '用户消息', estimate: 2 }
] };
const request = { status: 'completed', requestFamily: 'codex', inputTokens: 90, cacheReadTokens: 20, cacheCreationTokens: 10, usageKnown: true };

test('按协议校准分类、条目和方格总数，输入始终包含且只包含一次缓存', () => {
  for (const [requestFamily, expected] of [['codex', 90], ['claude', 120]]) {
    const data = inputComposition(summary, { ...request, requestFamily });
    assert.equal(data.counted, true);
    assert.equal(data.total, expected);
    assert.equal(data.groups.reduce((n, g) => n + g.tokens, 0), expected);
    assert.equal(data.groups[0].items[0].tokens, data.groups[0].tokens);
    assert.equal(data.window, 2000);
  }
  const cells = distributeCells([100, 1, 1999], 240);
  assert.equal(cells.reduce((n, v) => n + v, 0), 240);
  assert.ok(cells.every((v) => v > 0));
});

test('续接请求只把可见估算归给文件，不可见差额单列', () => {
  const data = inputComposition({ ...summary, partial: true }, request);
  assert.deepEqual(data.groups.map((g) => [g.key, g.tokens]), [['unassigned', 85], ['files', 3], ['chat', 2]]);
  const opaque = inputComposition({ ...summary, items: [], partial: true }, { ...request, usageKnown: false });
  assert.equal(opaque.pendingTotal, true);
});

test('旧记录、未知窗口、没有上游用量、真实零用量可以区分', () => {
  assert.equal(inputComposition(null, request), null);
  for (const row of [{ status: 'processing', inputTokens: 0 }, { usageKnown: false }, { status: 'failed', inputTokens: 0 }]) {
    const data = inputComposition({ ...summary, window_source: undefined }, { ...request, ...row });
    assert.deepEqual([data.counted, data.total, data.window], [false, 5, null]);
  }
  const liveUsage = inputComposition(summary, { ...request, status: 'processing' });
  assert.deepEqual([liveUsage.counted, liveUsage.total], [true, 90]);
  const zero = inputComposition(summary, { ...request, requestSource: 'codex_subscription_proxy', inputTokens: 0 });
  assert.deepEqual([zero.counted, zero.total], [true, 0]);
});

test('输入差额只记一次，已观察到的多种原因单独说明', () => {
  const data = inputComposition({
    ...summary, partial: true, partial_reasons: ['history_reference', 'encrypted_reasoning', 'ws_create', 'unknown_new_reason']
  }, request);
  assert.equal(data.groups[0].items.length, 1);
  assert.equal(data.groups[0].items[0].tokens, 85);
  assert.match(data.attributionNote, /差额已包含在输入总量里/);
  assert.match(data.attributionNote, /引用了上游历史.*加密推理.*创建帧/);
  assert.doesNotMatch(data.attributionNote, /unknown_new_reason/);
  assert.equal(data.groups.reduce((sum, g) => sum + g.tokens, 0), request.inputTokens);
});

test('历史摘要保留已归档的 exec 用量，缺失的原因不补猜', () => {
  const data = inputComposition({ version: 1, partial: true, items: [{ group: 'results', name: 'exec', estimate: 5 }] }, request);
  assert.equal(data.groups.find((g) => g.key === 'results').items[0].name, '执行脚本 · 输出（未细分）');
  assert.equal(data.groups.find((g) => g.key === 'results').tokens, 5);
  assert.match(data.attributionNote, /没有记录具体原因/);
});

test('历史引用、其他不可见输入和旧摘要按各自的证据说明差额', () => {
  const items = [{ group: 'results', name: '工具输出', estimate: 13 }];
  const usage = { ...request, inputTokens: 99845, cacheReadTokens: 98432 };
  for (const [reasons, label] of [
    [['history_reference', 'ws_create'], '历史续接差额'],
    [['history_reference', 'image'], '输入差额'],
    [['history_reference', 'unrecognized_reason'], '输入差额'],
    [['ws_create'], '输入差额'], [undefined, '输入差额']
  ]) {
    const data = inputComposition({ version: 1, items, partial: true, partial_reasons: reasons }, usage);
    assert.equal(data.groups[0].items[0].name, label);
    assert.equal(data.groups[0].name, label);
    assert.deepEqual(data.groups.map((g) => g.tokens), [99832, 13]);
    assert.match(data.attributionNote, /99,845 − 可见估算 13 = 差额 99,832/);
    assert.match(data.attributionNote, /已含缓存读取 98,432/);
  }
  assert.equal(formatPercent(99832, 99845), '>99.9%');
  assert.equal(formatPercent(99845, 99845), '100%');
});

test('Claude 兼容请求展示厂商窗口，工具定义与工具结果可分别对照原名', () => {
  const items = [
    { group: 'tools', name: 'WebSearch', estimate: 4 }, { group: 'tools', name: 'web_search', estimate: 2 },
    { group: 'results', name: 'WebSearch', estimate: 5 }, { group: 'files', name: 'Read', estimate: 1 },
    { group: 'tools', name: 'CustomLookup', estimate: 1 }
  ];
  for (const [source, hint] of [['model_spec', '请求当时'], ['historical_model_spec', '历史记录']]) {
    const data = inputComposition({ version: 1, items, window_source: source, window_model: 'deepseek-flash', window: 1048576 },
      { ...request, requestFamily: 'claude' });
    assert.equal(data.window, 1048576);
    assert.ok(data.windowDescription.includes(hint));
    assert.equal(data.total, 120);
    const labels = data.groups.flatMap((g) => g.items.map((i) => [i.rawName, i.name]));
    for (const [raw, label] of [['WebSearch', '网页搜索 · 工具定义'], ['WebSearch', '网页搜索 · 工具结果'],
      ['web_search', '服务端网页搜索 · 工具定义'], ['Read', 'Read'], ['CustomLookup', 'CustomLookup']]) {
      assert.ok(labels.some(([key, name]) => key === raw && name === label));
    }
  }
  assert.equal(inputComposition({ version: 1, items }, request).groups.find((g) => g.key === 'results').items[0].name, 'WebSearch');
});

test('父响应摘要与本次同名条目合并，先计本次输入，差额只计一次', () => {
  const s = { version: 1, partial: true, partial_reasons: ['history_reference', 'ws_create'],
    items: [{ group: 'files', name: 'main.go', estimate: 13 }],
    history_items: [{ group: 'files', name: 'main.go', estimate: 50 }, { group: 'chat', name: '第 1 轮对话', estimate: 30 }] };
  for (const [inputTokens, expectedHistory, expectedGap] of [[110, 80, 17], [65, 52, 0], [10, 0, 0], [0, 0, 0]]) {
    const r = inputComposition(s, { ...request, inputTokens, outputTokens: 1 });
    assert.equal(r.inherited, expectedHistory);
    assert.equal(r.groups.reduce((n, g) => n + g.tokens, 0), inputTokens);
    assert.equal(r.groups.find((g) => g.key === 'unassigned')?.tokens ?? 0, expectedGap);
    assert.equal(r.groups.find((g) => g.key === 'files').items.length, 1);
    if (expectedGap) assert.match(r.attributionNote, /可见估算 13 − 父响应摘要估算 80 = 差额 17/);
  }
  const pending = inputComposition(s, { ...request, usageKnown: false });
  assert.deepEqual([pending.total, pending.inherited, pending.counted], [93, 80, false]);
});

test('按本次回显估算工具定义，累计不超过上游用量，同名 exec 的三种用途分别显示', () => {
  const s = {
    version: 1, partial: true, partial_reasons: ['history_reference', 'ws_create'], tool_echo: 'reported',
    items: [{ group: 'chat', name: 'exec 工具调用', estimate: 5 }, { group: 'results', name: 'exec', estimate: 10 }],
    echo_items: [{ group: 'tools', name: 'exec', estimate: 35 }],
    history_items: [{ group: 'files', name: 'main.go', estimate: 30 }]
  };
  const data = inputComposition(s, { ...request, inputTokens: 100 });
  const rows = data.groups.flatMap((g) => g.items);
  assert.deepEqual(rows.filter((i) => i.rawName === 'exec').map((i) => [i.name, i.tokens, i.echoTokens]),
    [['执行脚本 · 定义', 35, 35], ['执行脚本 · 输出（未细分）', 10, 0], ['执行脚本 · 调用参数', 5, 0]]);
  assert.match(data.attributionNote, /可见估算 15 − 上游回显工具估算 35 − 父响应摘要估算 30 = 差额 20/);
  for (const inputTokens of [5, 20, 50, 65, 100]) {
    const r = inputComposition(s, { ...request, inputTokens });
    assert.equal(r.groups.reduce((n, g) => n + g.tokens, 0), inputTokens);
  }
  const local = inputComposition({ ...s, items: [{ group: 'tools', name: 'exec', estimate: 17 }] }, request);
  const tools = local.groups.find((g) => g.key === 'tools');
  assert.equal(tools.tokens, 17);
  assert.equal(tools.items[0].echoTokens, 0);
  for (const [state, tip] of [['unreported', '未提供工具清单'], ['empty', '空工具清单']]) {
    const r = inputComposition({ ...s, tool_echo: state }, request);
    assert.ok(!r.groups.some((g) => g.key === 'tools'));
    assert.ok(r.attributionNote.includes(tip));
  }
});

test('Codex 常用工具显示中文，悬停保留归档标识，同名文件和未知服务保持原名', () => {
  const definitions = [
    ['collaboration', '子代理协作'], ['MCP · cua_repl', 'MCP · 电脑操作'],
    ['request_user_input_async', '询问用户（继续执行）'], ['request_user_input', '询问用户（等待答复）'],
    ['wait', '等待脚本结果'], ['send_message_to_user_async', '向用户发消息'], ['clock', '时间与定时等待'],
    ['MCP · custom', 'MCP · custom']
  ];
  const data = inputComposition({
    version: 1, partial: true,
    items: definitions.map(([name]) => ({ group: 'tools', name, estimate: 1 })),
    history_items: [{ group: 'chat', name: 'functions.exec 工具调用', estimate: 2 }, { group: 'files', name: 'clock', estimate: 2 }]
  }, request);
  const tools = data.groups.find((g) => g.key === 'tools').items;
  assert.deepEqual(tools.map((i) => [i.rawName, i.name]), definitions);
  const call = data.groups.find((g) => g.key === 'chat').items[0];
  assert.deepEqual([call.name, call.rawName, call.historyTokens], ['执行脚本 · 调用参数', 'functions.exec', 2]);
  assert.equal(data.groups.find((g) => g.key === 'files').items[0].name, 'clock');
});
