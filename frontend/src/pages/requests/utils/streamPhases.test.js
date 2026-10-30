import test from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { contentTiming, streamPhaseBounds, streamSpeed, stopReasonView } from './streamPhases.js';
import { buildLifecycleSegments, buildRailLifecycleSegments, lifecycleSegmentColors, railSegmentColors } from './lifecycle.js';

const row = (extra = {}) => ({
  isStreaming: true, status: 'completed', duration: 2200, upstreamWriteMs: 100,
  firstTokenMs: 100, completionMs: 2000, firstContentMs: 1000, firstTextMs: 1000,
  streamFlowMs: 1500, outputTokens: 300, requestFamily: 'codex', ...extra
});

test('速度：首个内容到流完成，推理扣除、正文起算，旧数据保持TPS', () => {
  assert.equal(streamSpeed(row()).value, 250);
  const reasoning = streamSpeed(row({ firstContentMs: 500, firstTextMs: 1200, responseReasoning: 'effort:high reasoning_tokens:100' }));
  assert.equal(reasoning.value, 200);
  assert.equal(reasoning.windowMs, 1000);
  assert.match(reasoning.title, /扣除推理 100 tok，从首个正文起算/);
  assert.equal(streamSpeed(row({ firstContentMs: null })).value, 150);
  assert.equal(streamSpeed(row({ firstContentMs: 0, upstreamWriteMs: 0, firstTokenMs: 0, completionMs: 1000, duration: 1000 })).value, 300);
});

test('速度：窗口、超速、一次性下发、正文憋着、无正文与未知flow边界', () => {
  assert.equal(streamSpeed(row({ firstContentMs: 2150, firstTextMs: 2150 })).reason, 'window');
  assert.equal(streamSpeed(row({ outputTokens: 12001 })).reason, 'window');
  assert.equal(streamSpeed(row({ outputTokens: 12000 })).value, 10000);
  assert.equal(streamSpeed(row({ streamFlowMs: 299 })).reason, 'burst');
  assert.equal(streamSpeed(row({ streamFlowMs: 300 })).value, 250);
  assert.equal(streamSpeed(row({ streamFlowMs: null })).value, 250);
  assert.equal(streamSpeed(row({ firstTextMs: null, responseReasoning: 'reasoning_tokens:1' })).reason, 'no-text');
  assert.equal(streamSpeed(row({ firstContentMs: 300, firstTextMs: 2000, responseReasoning: 'reasoning_tokens:1' })).reason, 'held-text');
  assert.equal(streamSpeed(row({ outputTokens: 100, responseReasoning: 'reasoning_tokens:100' })).value, null);
  assert.equal(streamSpeed(row({ usageKnown: false })).value, null);
});

test('分段：可见思考、不可见思考、无思考、仅工具、失败残差与钳制', () => {
  const keys = (r) => buildLifecycleSegments(r).map((s) => s.key);
  assert.deepEqual(keys(row({ firstReasoningMs: 500 })), ['connect', 'first', 'think', 'output']);
  assert.deepEqual(keys(row({ reasoningHidden: true })), ['connect', 'first', 'think', 'output']);
  assert.deepEqual(keys(row()), ['connect', 'first', 'await', 'output']);
  assert.deepEqual(keys(row({ firstTextMs: null })), ['connect', 'first', 'await', 'output']);
  for (const r of [row(), row({ reasoningHidden: true }), row({ firstContentMs: -2, firstTextMs: -5 }), row({ firstContentMs: 9999 }), row({ upstreamWriteMs: 9999 }), row({ status: 'failed', completionMs: null })]) {
    const segments = buildLifecycleSegments(r);
    assert.equal(segments.reduce((n,s) => n+s.ms, 0), r.duration);
    assert.ok(segments.every((s) => s.ms >= 0));
  }
  assert.match(buildLifecycleSegments(row({ reasoningHidden: true })).find((s) => s.key === 'think').detail, /不可见推理/);
  assert.equal(lifecycleSegmentColors.think, 'bg-fuchsia-200 dark:bg-fuchsia-800');
  assert.equal(railSegmentColors.output, 'bg-emerald-400');
  assert.equal(streamPhaseBounds(row({ firstContentMs: null })), null);
  assert.ok(!buildRailLifecycleSegments(row()).some((s) => ['think','await','output'].includes(s.key)));
});

test('实时K2：首个事件后仍等内容，内容事件先到时锚点一致，结束无内容按旧口径', () => {
  const start = '2026-10-30T00:00:00.000000Z';
  const sent = '2026-10-30T00:00:00.100000Z';
  const content = '2026-10-30T00:00:01.000000Z';
  const r = row({ timestamp: start, firstTokenStartAt: sent, firstContentAt: '', firstContentMs: null, completionMs: null });
  assert.equal(contentTiming(r, true).first, null);
  const live = contentTiming({ ...r, firstContentAt: content }, true);
  assert.equal(live.first, 900);
  assert.deepEqual(live.anchor, { timestamp: content, baseMs: 0 });
  const settled = contentTiming({ ...r, firstContentMs: 1000, firstContentAt: content, completionMs: 2000 }, false);
  assert.equal(settled.first, live.first);
  assert.equal(settled.completion, 1200);
  assert.equal(contentTiming({ ...r, status: 'completed' }, false).useContent, false);
  const eventOnly = { ...r, upstreamWriteMs: null, firstContentAt: content,
    firstTokenAt: '2026-10-30T00:00:00.200000Z', completionMs: 2000 };
  assert.equal(contentTiming(eventOnly,true).completion,1200, '完成事件先于列表查询时仍从C定格');
});

test('结束原因：普通、警示、拒答、未知原文及对象属性名', () => {
  for (const raw of ['end_turn','completed','tool_use','stop_sequence','pause_turn']) assert.equal(stopReasonView(raw).warning, false);
  for (const raw of ['max_tokens','incomplete:max_output_tokens','refusal','model_context_window_exceeded','incomplete:content_filter']) assert.equal(stopReasonView(raw).warning, true);
  assert.equal(stopReasonView('refusal').className, 'text-rose-700 dark:text-rose-300');
  assert.equal(stopReasonView('max_tokens').label, '达到输出上限');
  assert.equal(stopReasonView('__proto__').label, '__proto__');
});

test('合成SSE回放：用同一份时钟与Go采集结果手算分段和速度', () => {
  const samples = JSON.parse(readFileSync(new URL('../../../../../tests/testdata/stream_phases/replay_timing.json', import.meta.url)));
  for (const [name, tokens, windowMs] of [['claude_thinking',400,3800], ['codex_hidden_reasoning',300,2200]]) {
    const r = { ...samples[name], isStreaming: true, status: 'completed' };
    assert.equal(streamSpeed(r).value, tokens*1000/windowMs);
    const segments = buildLifecycleSegments(r);
    assert.equal(segments.reduce((n,s) => n+s.ms, 0), r.duration);
    console.log(`${name}: ${JSON.stringify({ segments, speed: streamSpeed(r).value, first: contentTiming(r,false).first, generation: contentTiming(r,false).completion })}`);
  }
});

test('复审K2：图像始终用旧口径，无内容流完成后立即回到旧口径', () => {
  const empty = row({ firstContentMs: null, firstContentAt: '', firstTextMs: null });
  assert.equal(contentTiming({ ...empty, requestFamily: 'image', completionMs: null }, true).useContent, false);
  assert.equal(contentTiming({ ...row(), requestFamily: 'image' }, false).useContent, false);
  for (const requestFamily of ['claude', 'codex']) {
    assert.equal(contentTiming({ ...empty, requestFamily, completionMs: null }, true).useContent, true);
    assert.equal(contentTiming({ ...empty, requestFamily, completionMs: 0 }, true).useContent, false);
    assert.equal(contentTiming({ ...empty, requestFamily, completionMs: 900 }, true).useContent, false);
  }
});

test('复审K2：内容实时事件先到时，完成与状态查询不能切回旧口径', () => {
  const request = row({
    firstContentMs: null, timestamp: '2026-10-30T00:00:00.000000Z',
    firstTokenStartAt: '2026-10-30T00:00:00.100000Z',
    firstTokenAt: '2026-10-30T00:00:00.200000Z',
    firstContentAt: '2026-10-30T00:00:01.000000Z'
  });
  for (const running of [true, false]) {
    const timing = contentTiming(request, running);
    assert.equal(timing.useContent, true);
    assert.equal(timing.first, 900);
    assert.equal(timing.completion, 1200);
  }
  assert.equal(contentTiming(row({ firstContentMs: 0 }), false).useContent, true);
});
