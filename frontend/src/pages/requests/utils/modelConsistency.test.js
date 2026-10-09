import test from 'node:test';
import assert from 'node:assert/strict';

import {
  parseReasoning,
  formatReasoning,
  assessModel,
  assessReasoning,
  formatSpeed,
  buildModelAlert
} from './modelConsistency.js';

test('formatSpeed 只展示请求档位与上游回报值，不做一致性判定', () => {
  const cases = [
    [{}, '标准'],
    [{ requestReasoning: 'speed:default', responseReasoning: 'speed:default' }, '标准'],
    [{ responseReasoning: 'speed:auto' }, '标准'],
    [{ responseReasoning: 'speed:priority' }, '请求 标准 · 上游回报 Fast'],
    [{ requestReasoning: 'speed:priority' }, '请求 Fast · 上游未回报'],
    [{ requestReasoning: 'speed:priority', responseReasoning: 'speed:default' }, '请求 Fast · 上游回报 标准'],
    [{ requestReasoning: 'speed:fast', responseReasoning: 'speed:fast' }, '请求 Fast · 上游回报 Fast'],
    [{ requestReasoning: 'speed:ultrafast', responseReasoning: 'speed:flex' }, '请求 Ultrafast · 上游回报 Flex'],
    [{ requestReasoning: 'speed:__proto__', responseReasoning: 'speed:future-tier' }, '请求 __proto__ · 上游回报 future-tier']
  ];
  for (const [request, text] of cases) {
    assert.equal(formatSpeed(request), text, JSON.stringify(request));
  }
});

test('速度档位不进列表告警', () => {
  assert.equal(buildModelAlert({ requestReasoning: 'speed:priority', responseReasoning: 'speed:default' }), null);
  const combined = buildModelAlert({ requestFamily: 'codex', requestModel: 'gpt-a', responseModel: 'gpt-b', requestReasoning: 'effort:high speed:ultrafast', responseReasoning: 'effort:low speed:default' });
  assert.ok(combined.lines.includes('思考  high → low'));
  assert.ok(combined.lines.every(line => !line.startsWith('速度')));
});

test('速度字段独立于思考显示，旧数据的判定保持不变', () => {
  const legacy = { requestFamily: 'codex', requestReasoning: 'effort:high', responseReasoning: 'effort:low' };
  assert.equal(formatSpeed(legacy), '标准');
  assert.deepEqual(assessReasoning({ ...legacy, requestReasoning: 'effort:high speed:priority', responseReasoning: 'effort:low speed:default' }), assessReasoning(legacy));
  assert.equal(formatReasoning('effort:high speed:priority'), 'high');
  assert.equal(formatReasoning('speed:priority'), '未指定');
  assert.equal(buildModelAlert({}), null);
});

test('parseReasoning / formatReasoning 文法往返', () => {
  assert.deepEqual(parseReasoning('thinking:adaptive effort:xhigh'), { thinking: 'adaptive', effort: 'xhigh' });
  assert.deepEqual(parseReasoning('  effort:medium   reasoning_tokens:1234 bogus :x y: '), { effort: 'medium', reasoning_tokens: '1234' });
  assert.deepEqual(parseReasoning(''), {});
  assert.deepEqual(parseReasoning(null), {});
  assert.equal(formatReasoning('thinking:adaptive effort:xhigh'), 'adaptive · xhigh');
  assert.equal(formatReasoning('effort:xhigh'), 'xhigh');
  assert.equal(formatReasoning('thinking:present'), '有思考块');
  assert.equal(formatReasoning('thinking:enabled budget:16000'), 'enabled · 预算 16,000');
  assert.equal(formatReasoning('effort:medium reasoning_tokens:1234'), 'medium · 推理 1,234 tok');
  assert.equal(formatReasoning(''), '未指定');
});

test('assessModel：一致 / 改写后一致 / 旧数据 / 无响应', () => {
  assert.equal(assessModel({ requestModel: 'claude-sonnet-4-5', responseModel: 'claude-sonnet-4-5' }).level, 'consistent');
  const rewritten = assessModel({ requestModel: 'gpt-5.4', upstreamModel: 'gpt-5.5', responseModel: 'gpt-5.5' });
  assert.equal(rewritten.level, 'consistent');
  assert.equal(rewritten.rewritten, true);
  assert.equal(rewritten.reference, 'gpt-5.5');
  assert.equal(assessModel({ requestModel: '', upstreamModel: '', responseModel: '' }).level, 'legacy');
  assert.equal(assessModel({ requestModel: 'unknown', responseModel: 'x' }).level, 'legacy');
  assert.equal(assessModel({ requestModel: 'claude-test', responseModel: '' }).level, 'no_response');
  assert.equal(assessModel({ requestModel: 'claude-test', responseModel: 'default' }).level, 'no_response');
});

test('assessModel：response_model 为空但 model 有值时必须是 no_response 而非 consistent', () => {
  const result = assessModel({ requestModel: 'claude-test', model: 'claude-test', responseModel: '' });
  assert.equal(result.level, 'no_response');
});

test('assessModel：大小写与空白归一化', () => {
  assert.equal(assessModel({ requestModel: ' Claude-Sonnet-4-5 ', responseModel: 'claude-sonnet-4-5' }).level, 'consistent');
  assert.equal(assessModel({ requestModel: 'gpt-5.4', upstreamModel: ' GPT-5.4 ', responseModel: 'gpt-5.4' }).rewritten, false);
});

test('assessModel：仅认日期快照别名', () => {
  assert.equal(assessModel({ requestModel: 'claude-sonnet-4-5', responseModel: 'claude-sonnet-4-5-20250929' }).level, 'alias');
  assert.equal(assessModel({ requestModel: 'claude-sonnet-4-5-latest', responseModel: 'claude-sonnet-4-5-20250929' }).level, 'alias');
  assert.equal(assessModel({ requestModel: 'gpt-5', responseModel: 'gpt-5-2025-08-07' }).level, 'alias');
  assert.equal(assessModel({ requestModel: 'gpt-5-latest', responseModel: 'gpt-5-2025-08-07' }).level, 'alias');
  assert.equal(assessModel({ requestModel: 'gpt-5', responseModel: 'gpt-5-2025-8-7' }).level, 'mismatch');
  assert.equal(assessModel({ requestModel: 'gpt-5', responseModel: 'gpt-5-mini' }).level, 'mismatch');
  assert.equal(assessModel({ requestModel: 'gpt-5', responseModel: 'gpt-5-codex' }).level, 'mismatch');
  assert.equal(assessModel({ requestModel: 'claude-sonnet-4-5', responseModel: 'claude-sonnet-4-5-20250929-preview' }).level, 'mismatch');
  assert.equal(assessModel({ requestModel: 'claude-sonnet-4-5', responseModel: 'claude-sonnet-4-5-2025092' }).level, 'mismatch');
  assert.equal(assessModel({ requestModel: 'claude-sonnet-4-5', responseModel: 'claude-haiku-4-5' }).level, 'mismatch');
  // 改写后仍不一致：以上游模型为基准
  const rewrittenMismatch = assessModel({ requestModel: 'gpt-5.4', upstreamModel: 'gpt-5.5', responseModel: 'gpt-5.4' });
  assert.equal(rewrittenMismatch.level, 'mismatch');
  assert.equal(rewrittenMismatch.rewritten, true);
});

test('assessReasoning：Codex effort 比较', () => {
  assert.equal(assessReasoning({ requestFamily: 'codex', requestReasoning: 'effort:xhigh', responseReasoning: 'effort:xhigh reasoning_tokens:12' }).level, 'consistent');
  const downgraded = assessReasoning({ requestFamily: 'codex', requestReasoning: 'effort:xhigh', responseReasoning: 'effort:medium reasoning_tokens:1234' });
  assert.equal(downgraded.level, 'mismatch');
  assert.equal(downgraded.responseLabel, 'medium · 推理 1,234 tok');
  const unverifiable = assessReasoning({ requestFamily: 'codex', requestReasoning: 'effort:xhigh', responseReasoning: 'reasoning_tokens:5' });
  assert.equal(unverifiable.level, 'unverifiable');
  assert.equal(unverifiable.note, '上游未回显');
  assert.equal(assessReasoning({ requestFamily: 'codex', requestReasoning: '', responseReasoning: '' }).level, 'none');
  assert.equal(assessReasoning({ requestFamily: 'codex', requestReasoning: '', responseReasoning: 'effort:low' }).level, 'none');
});

test('assessReasoning：Claude 只校验思考块', () => {
  assert.equal(assessReasoning({ requestFamily: 'claude', requestReasoning: 'thinking:disabled', responseReasoning: 'thinking:present' }).level, 'mismatch');
  assert.equal(assessReasoning({ requestFamily: 'claude', requestReasoning: 'thinking:disabled', responseReasoning: 'thinking:absent' }).level, 'consistent');
  assert.equal(assessReasoning({ requestFamily: 'claude', requestReasoning: 'thinking:adaptive', responseReasoning: 'thinking:absent' }).level, 'hint');
  assert.equal(assessReasoning({ requestFamily: 'claude', requestReasoning: 'thinking:enabled budget:16000', responseReasoning: 'thinking:absent' }).level, 'mismatch');
  assert.equal(assessReasoning({ requestFamily: 'claude', requestReasoning: 'thinking:enabled budget:16000', responseReasoning: 'thinking:present' }).level, 'consistent');
  const consistent = assessReasoning({ requestFamily: 'claude', requestReasoning: 'thinking:adaptive effort:xhigh', responseReasoning: 'thinking:present' });
  assert.equal(consistent.level, 'consistent');
  assert.equal(consistent.note, '等级无法校验');
  assert.equal(consistent.requestLabel, 'adaptive · xhigh');
  assert.equal(assessReasoning({ requestFamily: 'claude', requestReasoning: 'thinking:adaptive', responseReasoning: '' }).level, 'unverifiable');
  assert.equal(assessReasoning({ requestFamily: 'claude', requestReasoning: '', responseReasoning: '' }).level, 'none');
  // 只有 effort 没有 thinking：不判定
  assert.equal(assessReasoning({ requestFamily: 'claude', requestReasoning: 'effort:high', responseReasoning: 'thinking:present' }).level, 'none');
});

test('buildModelAlert：两项都一致 / 别名 / hint / 旧数据时返回 null', () => {
  assert.equal(buildModelAlert({ requestFamily: 'claude', requestModel: 'claude-test', responseModel: 'claude-test', requestReasoning: 'thinking:adaptive', responseReasoning: 'thinking:present' }), null);
  assert.equal(buildModelAlert({ requestFamily: 'claude', requestModel: 'claude-test', responseModel: 'claude-test-20250929' }), null);
  assert.equal(buildModelAlert({ requestFamily: 'claude', requestModel: 'claude-test', responseModel: 'claude-test', requestReasoning: 'thinking:adaptive', responseReasoning: 'thinking:absent' }), null);
  assert.equal(buildModelAlert({ requestFamily: 'claude', model: 'claude-test' }), null);
  assert.equal(buildModelAlert({ requestFamily: 'claude', requestModel: 'claude-test', responseModel: '' }), null);
});

test('buildModelAlert：模型不一致 / 仅思考不一致', () => {
  const modelAlert = buildModelAlert({ requestFamily: 'claude', requestModel: 'claude-sonnet-4-5', responseModel: 'claude-haiku-4-5' });
  assert.equal(modelAlert.level, 'mismatch');
  assert.deepEqual(modelAlert.lines, ['请求  claude-sonnet-4-5', '响应  claude-haiku-4-5', '上游返回与请求不一致（未应用改写）']);

  const both = buildModelAlert({ requestFamily: 'codex', requestModel: 'gpt-5.4', upstreamModel: 'gpt-5.5', responseModel: 'gpt-5.4', requestReasoning: 'effort:xhigh', responseReasoning: 'effort:medium' });
  assert.deepEqual(both.lines, ['上游  gpt-5.5', '响应  gpt-5.4', '思考  xhigh → medium', '上游返回与改写后的模型不一致']);

  const reasoningOnly = buildModelAlert({ requestFamily: 'codex', requestModel: 'gpt-5.5', responseModel: 'gpt-5.5', requestReasoning: 'effort:xhigh', responseReasoning: 'effort:medium' });
  assert.deepEqual(reasoningOnly.lines, ['思考  xhigh → medium', '上游思考等级与请求不一致']);
});
