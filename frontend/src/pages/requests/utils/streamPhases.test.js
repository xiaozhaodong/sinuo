import test from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { generationThroughput, firstContentDelayMs, requestSpeedMetrics, streamPhaseBounds, streamSpeed, stopReasonView } from './streamPhases.js';
import { buildLifecycleSegments, buildRailLifecycleSegments, lifecycleSegmentColors, railSegmentColors } from './lifecycle.js';

const row = (extra = {}) => ({
  isStreaming: true, status: 'completed', duration: 2200, upstreamWriteMs: 100,
  firstTokenMs: 100, completionMs: 2000, firstContentMs: 1000, firstTextMs: 1000,
  streamFlowMs: 1500, outputTokens: 300, requestFamily: 'codex', ...extra
});

test('速度：首个内容到流完成，推理扣除、正文起算，缺计时不更换分母', () => {
  assert.equal(streamSpeed(row()).value, 250);
  const reasoning = streamSpeed(row({ firstContentMs: 500, firstTextMs: 1200, responseReasoning: 'effort:high reasoning_tokens:100' }));
  assert.equal(reasoning.value, 200);
  assert.equal(reasoning.windowMs, 1000);
  assert.match(reasoning.title, /扣除推理 100 tok，从首个正文起算/);
  assert.equal(streamSpeed(row({ firstContentMs: null })).value, null);
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

test('首字耗时：内容事件先到时使用绝对时刻，查询补齐后数值一致', () => {
  const start = '2026-10-30T00:00:00.000000Z';
  const sent = '2026-10-30T00:00:00.100000Z';
  const content = '2026-10-30T00:00:01.000000Z';
  const r = row({ timestamp: start, firstTokenStartAt: sent, firstContentAt: '', firstContentMs: null, completionMs: null });
  assert.equal(firstContentDelayMs(r), null);
  const live = firstContentDelayMs({ ...r, firstContentAt: content });
  assert.equal(live, 900);
  assert.equal(firstContentDelayMs({ ...r, firstContentMs: 1000, firstContentAt: content, completionMs: 2000 }), live);
  assert.equal(firstContentDelayMs({ ...r, status: 'completed' }), null);
  const eventOnly = { ...r, upstreamWriteMs: null, firstContentAt: content,
    firstTokenAt: '2026-10-30T00:00:00.200000Z', completionMs: 2000 };
  assert.equal(firstContentDelayMs(eventOnly), 900, '只有实时内容时刻和写完时刻时也可计算首字');
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
  for (const [name, tokens, windowMs] of [['claude_thinking',400,4100], ['codex_hidden_reasoning',300,2200]]) {
    const r = { ...samples[name], isStreaming: true, status: 'completed' };
    assert.equal(streamSpeed(r).value, tokens*1000/windowMs);
    const segments = buildLifecycleSegments(r);
    assert.equal(segments.reduce((n,s) => n+s.ms, 0), r.duration);
    console.log(`${name}: ${JSON.stringify({ segments, speed: streamSpeed(r).value, firstContent: firstContentDelayMs(r), generation: r.completionMs })}`);
  }
});

test('首字耗时：图像、非流式和无内容请求返回空，不推断未知时间', () => {
  const empty = row({ firstContentMs: null, firstContentAt: '', firstTextMs: null });
  assert.equal(firstContentDelayMs({ ...empty, requestFamily: 'image', completionMs: null }), null);
  assert.equal(firstContentDelayMs({ ...row(), requestFamily: 'image' }), null);
  assert.equal(firstContentDelayMs(row({ isStreaming: false })), null);
  assert.equal(firstContentDelayMs(row({ upstreamWriteMs: null })), null);
  assert.equal(firstContentDelayMs({ ...empty, firstContentAt: 'invalid', firstTokenStartAt: 'invalid' }), null);
  for (const requestFamily of ['claude', 'codex']) {
    assert.equal(firstContentDelayMs({ ...empty, requestFamily, completionMs: null }), null);
    assert.equal(firstContentDelayMs({ ...empty, requestFamily, completionMs: 0 }), null);
    assert.equal(firstContentDelayMs({ ...empty, requestFamily, completionMs: 900 }), null);
  }
});

test('首字耗时：内容实时事件先到时，完成和状态变化不丢值，保留真实0', () => {
  const request = row({
    firstContentMs: null, timestamp: '2026-10-30T00:00:00.000000Z',
    firstTokenStartAt: '2026-10-30T00:00:00.100000Z',
    firstTokenAt: '2026-10-30T00:00:00.200000Z',
    firstContentAt: '2026-10-30T00:00:01.000000Z'
  });
  for (const status of ['processing', 'completed']) {
    assert.equal(firstContentDelayMs({ ...request, status }), 900);
  }
  assert.equal(firstContentDelayMs(row({ firstContentMs: 0, upstreamWriteMs: 0 })), 0);
});

const claudeThinkingSamples = [
  { requestId: 'synthetic-claude-a', upstreamWriteMs: 1839, firstTokenMs: 2166, firstContentMs: 17976,
    firstReasoningMs: 17976, streamFlowMs: 1843, completionMs: 15445, outputTokens: 1610,
    duration: 19450, thinkingMs: 13971, outputMs: 1474 },
  { requestId: 'synthetic-claude-b', upstreamWriteMs: 369, firstTokenMs: 1929, firstContentMs: 6078,
    firstReasoningMs: null, streamFlowMs: 1, completionMs: 6739, outputTokens: 807,
    duration: 9037, thinkingMs: 3780, outputMs: 2959 },
  { requestId: 'synthetic-claude-c', upstreamWriteMs: 2013, firstTokenMs: 1586, firstContentMs: 4209,
    firstReasoningMs: null, streamFlowMs: 823, completionMs: 1275, outputTokens: 161,
    duration: 4874, thinkingMs: 610, outputMs: 665 }
].map((sample) => row({ ...sample, requestFamily: 'claude', responseReasoning: 'thinking:present',
  firstTextMs: null, reasoningHidden: false }));

test('Claude思考真机数值回归：三段与首事件测速，列表详情结论一致', () => {
  for (const request of claudeThinkingSamples) {
    const speed = streamSpeed(request);
    assert.equal(speed.value, request.outputTokens * 1000 / request.completionMs);
    assert.equal(speed.windowMs, request.completionMs);
    assert.equal(speed.tokens, request.outputTokens);
    assert.equal(speed.reason, '');
    assert.equal(speed.title, 'Claude 不单报思考 token：输出含思考，从首个事件起算');
    const segments = buildLifecycleSegments(request);
    assert.deepEqual(segments.filter((s) => ['first', 'think', 'output'].includes(s.key)).map((s) => [s.label, s.ms]),
      [['首响', request.firstTokenMs], ['思考', request.thinkingMs], ['输出', request.outputMs]]);
    assert.match(segments.find((s) => s.key === 'think').detail, request.firstReasoningMs === null ? /不可见推理/ : /可见的思考内容/);
    assert.equal(segments.reduce((sum, s) => sum + s.ms, 0), request.duration);
    const listRow = { ...request };
    delete listRow.firstReasoningMs;
    delete listRow.reasoningHidden;
    assert.deepEqual(streamSpeed(listRow), speed);
    assert.ok(buildLifecycleSegments(listRow).some((s) => s.key === 'think'));
  }
  assert.match(buildLifecycleSegments(claudeThinkingSamples[1]).find((s) => s.key === 'output').detail, /内容几乎一次性下发（上游憋住后放出）/);
});

test('Claude思考信号：整项匹配，详情独立信号与零时刻也有效', () => {
  for (const signal of [{ responseReasoning: 'effort:high thinking:present speed:fast' },
    { reasoningHidden: true }, { firstReasoningMs: 0 }]) {
    const request = row({ requestFamily: 'claude', streamFlowMs: 1, ...signal });
    assert.equal(streamSpeed(request).value, 150);
    assert.ok(buildLifecycleSegments(request).some((s) => s.key === 'think'));
  }
  for (const responseReasoning of ['', 'thinking:absent', 'xthinking:present', 'thinking:present_extra', 'thinking:present:other']) {
    const request = row({ requestFamily: 'claude', responseReasoning });
    assert.equal(streamSpeed(request).value, 250);
    assert.ok(buildLifecycleSegments(request).some((s) => s.key === 'await'));
    assert.equal(streamSpeed({ ...request, streamFlowMs: 1 }).reason, 'burst');
  }
});

test('Claude思考测速边界：未知用量、无完成、无token、短窗和超速不显示速度', () => {
  const request = claudeThinkingSamples[1];
  for (const extra of [{ outputTokens: 0 }, { outputTokens: -1 }, { completionMs: null }, { usageKnown: false }]) {
    assert.equal(streamSpeed({ ...request, ...extra }).value, null);
  }
  const short = row({ requestFamily: 'claude', responseReasoning: 'thinking:present',
    upstreamWriteMs: 0, firstTokenMs: 0, firstContentMs: 50, firstTextMs: null,
    completionMs: 99, duration: 99, streamFlowMs: 1, outputTokens: 10 });
  assert.equal(streamSpeed(short).value, null);
  assert.equal(streamSpeed(short).reason, 'window');
  assert.equal(streamSpeed({ ...short, completionMs: 100, duration: 100 }).value, 100);
  assert.equal(streamSpeed({ ...short, completionMs: 100, duration: 100, outputTokens: 1000 }).value, 10000);
  assert.equal(streamSpeed({ ...short, completionMs: 100, duration: 100, outputTokens: 1001 }).value, null);
});

test('Claude和Codex流式口径保持不变，旧记录缺少计时不计算流式速度', () => {
  const legacy = row({ requestFamily: 'claude', responseReasoning: 'thinking:present', firstContentMs: null });
  assert.equal(streamSpeed(legacy).reason, 'no-timing');
  assert.equal(streamSpeed(legacy).value, null);
  const codex = row({ responseReasoning: 'thinking:present reasoning_tokens:100' });
  assert.equal(streamSpeed(codex).value, 200 * 1000 / 1200);
  assert.equal(streamSpeed({ ...codex, streamFlowMs: 1 }).reason, 'burst');
  const request = row({ requestFamily: 'claude', responseReasoning: 'thinking:present',
    firstContentMs: 500, firstReasoningMs: 500, firstTextMs: 2000, streamFlowMs: 50 });
  assert.equal(streamSpeed(request).value, 150);
  assert.doesNotMatch(buildLifecycleSegments(request).find((s) => s.key === 'output').detail, /一次性下发/);
  assert.match(buildLifecycleSegments({ ...request, streamFlowMs: 49 }).find((s) => s.key === 'output').detail, /一次性下发/);
});

test('生成吞吐：首个事件到完成，包含推理且与严格流式速度独立', () => {
  const request = row({ duration: 9615, upstreamWriteMs: 1672, firstTokenMs: 2502,
    completionMs: 5440, firstContentMs: 6504, firstTextMs: null,
    reasoningHidden: true, streamFlowMs: 1747, outputTokens: 380,
    responseReasoning: 'effort:xhigh reasoning_tokens:73' });
  assert.equal(generationThroughput(request).value, 380000 / 5440);
  const [generation, stream] = requestSpeedMetrics(request);
  assert.equal(generation.label, '生成吞吐');
  assert.equal(generation.value, '69.9 tok/s');
  assert.match(generation.title, /总输出 token ÷ 生成耗时（首个事件到完成）/);
  assert.equal(stream.value, '—（缺少正文计时）');
  assert.match(stream.title, /未采集到正文增量时间/);
  for (const fields of [{ firstContentMs: null }, { duration: 30000, upstreamWriteMs: 10000, firstTokenMs: 9000 },
    { duration: null, upstreamWriteMs: null, firstTokenMs: null }]) {
    assert.deepEqual(generationThroughput({ ...request, ...fields }), generationThroughput(request));
  }
  assert.equal(requestSpeedMetrics(row({ streamFlowMs: 1 }))[0].value, '150 tok/s');
  assert.equal(requestSpeedMetrics(row({ streamFlowMs: 1 }))[1].value, '—（集中下发）');
});

test('生成吞吐：未知、零用量、进行中和非法生成耗时不混淆', () => {
  for (const fields of [{ usageKnown: false }, { completionMs: 0 }, { completionMs: -1 },
    { completionMs: null }, { completionMs: undefined }, { completionMs: NaN }, { completionMs: Infinity }, { outputTokens: undefined },
    { outputTokens: NaN }, { outputTokens: Infinity }, { outputTokens: -1 }, { outputTokens: 0 }]) {
    assert.equal(generationThroughput(row(fields)).value, null);
  }
  assert.equal(generationThroughput(row({ outputTokens: 0, usageKnown: true })).value, 0);
  for (const status of ['pending', 'forwarding', 'processing', 'retry', 'suspended']) {
    assert.equal(generationThroughput(row({ status })).value, null);
    assert.deepEqual(requestSpeedMetrics(row({ status })), []);
    assert.equal(streamSpeed(row({ status })).value, null);
  }
  assert.equal(streamSpeed(row({ outputTokens: Infinity })).value, null);
  assert.equal(requestSpeedMetrics(row({ firstContentMs: null }))[0].value, '150 tok/s');
  assert.equal(requestSpeedMetrics(row({ firstContentMs: null }))[1].value, '—（缺少流式计时）');
  assert.deepEqual(requestSpeedMetrics(row({ isStreaming: false })).map((m) => m.label), ['生成吞吐']);
  assert.deepEqual(requestSpeedMetrics(row({ requestFamily: 'image' })).map((m) => m.label), ['生成吞吐']);
  assert.equal(requestSpeedMetrics(row({ completionMs: null }))[0].value, '—');
  assert.equal(requestSpeedMetrics(row({ isStreaming: false, completionMs: 0 }))[0].value, '—');
  for (const completionMs of [0, 2000]) {
    const nonStreaming = row({ isStreaming: false, completionMs });
    assert.equal(generationThroughput(nonStreaming).reason, 'non-streaming');
    assert.equal(requestSpeedMetrics(nonStreaming)[0].title, '非流式响应没有生成耗时');
    assert.equal(requestSpeedMetrics(nonStreaming)[0].note, undefined, '非流式不显示「耗时过短」说明');
  }
});

test('生成吞吐采样边界：整段缓冲回放、短窗口和异常速度不显示数字', () => {
  for (const requestFamily of ['claude', 'codex']) {
    for (const [completionMs, outputTokens, expected] of [
      [0, 628, null], [1, 628, null], [99, 1, null],
      [100, 1000, 10000], [100, 1001, null],
      [200, 2000, 10000], [200, 2001, null], [200, 628, 3140]
    ]) {
      const request = row({ requestFamily, completionMs, outputTokens });
      const result = generationThroughput(request);
      const metric = requestSpeedMetrics(request)[0];
      assert.equal(result.value, expected, `${requestFamily}: ${outputTokens} tok / ${completionMs} ms`);
      if (expected === null) {
        assert.equal(result.reason, 'window');
        assert.equal(metric.value, '—');
        assert.equal(metric.title, '生成耗时过短（上游一次性返回），无法计算');
        assert.equal(metric.note, metric.title);
      } else {
        assert.equal(result.reason, '');
        assert.equal(metric.note, undefined);
      }
    }
  }
  assert.equal(generationThroughput(row({ completionMs: null })).reason, 'unknown');
  assert.equal(generationThroughput(row({ completionMs: 1, usageKnown: false })).reason, 'unknown');
});
