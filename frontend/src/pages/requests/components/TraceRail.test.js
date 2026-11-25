import test from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { dirname, join } from 'node:path';
import { build } from 'esbuild';

import { buildLifecycleSegments, railSegmentColors, lifecycleSegmentColors } from '../utils/lifecycle.js';
import { carryLiveTiming } from '../utils/liveTiming.js';
import { TABLE_COLUMNS, DEFAULT_VISIBLE_COLUMNS, UNSETTLED_STATUSES } from '../utils/constants.js';
import {
  getSettledRailSegments,
  getTraceRailPhase,
  quantizeSegmentsToCells,
  resolveFirstWaitMs,
  resolveStepRailCells,
  resolveStepRailColor,
  resolveStepRailFrameKey,
  resolveStepRailMeta,
  TRACE_RAIL_CELLS,
  TRACE_RAIL_LABEL_NEUTRAL,
  TRACE_RAIL_LABEL_TONES,
  TRACE_RAIL_SETTLE_MS,
  TRACE_RAIL_STEPS,
  TRACE_RAIL_SWEEP_CELLS,
  TRACE_RAIL_SWEEP_STEP_MS,
  traceRailLabelTone
} from '../utils/traceRail.js';
import { STATUS_CONFIG } from '../../../utils/constants.js';

const HERE = dirname(fileURLToPath(import.meta.url));
const readSource = (relativePath) => readFileSync(join(HERE, relativePath), 'utf8');

const RAIL_SOURCE = readSource('./TraceRail.jsx');
const TABLE_SOURCE = readSource('./RequestsTable.jsx');
const CSS_SOURCE = readSource('../../../index.css');

// 断言「代码里有没有某个属性」时必须先去掉注释，否则解释「为什么不用 role="img"」
// 的那句注释自己就会把 doesNotMatch 判失败。
const stripComments = (source) => source
  .replace(/\/\*[\s\S]*?\*\//g, '')
  .replace(/^[ \t]*\/\/.*$/gm, '');

const RAIL_CODE = stripComments(RAIL_SOURCE);
const TABLE_CODE = stripComments(TABLE_SOURCE);

const IN_FLIGHT = ['pending', 'forwarding', 'processing', 'retry'];

const keysIn = (state, from, to) => state.cells.slice(from, to).map((cell) => cell.key);
const litIn = (state, from, to) => state.cells.slice(from, to).filter((cell) => cell.key && !cell.dim).length;
const PREP_END = 5;
const FIRST_END = 13;

test('阶段游标只用于正在执行的请求', () => {
  for (const status of Object.keys(STATUS_CONFIG)) {
    assert.equal(Boolean(getTraceRailPhase({ status })), IN_FLIGHT.includes(status), status);
  }
});

test('准备、转发、首响、生成和收尾按记录推进', () => {
  const timestamp = '2026-11-19T02:00:00Z';
  const sentAt = '2026-11-19T02:00:01Z';
  for (const [request, key, step] of [
    [{ status: 'pending' }, 'preparing', 0],
    [{ status: 'forwarding' }, 'forwarding', 0],
    [{ status: 'processing', upstreamFirstDispatchMs: 0 }, 'forwarding', 0],
    [{ status: 'processing', firstTokenStartAt: sentAt, duration: 900000 }, 'first', 1],
    [{ status: 'processing', upstreamWriteMs: 0 }, 'first', 1],
    [{ status: 'processing', firstTokenMs: 0 }, 'stream', 2],
    [{ status: 'processing', completionMs: 0 }, 'settling', 2]
  ]) {
    const phase = getTraceRailPhase({ timestamp, ...request });
    assert.equal(phase.key, key);
    assert.equal(phase.step, step);
  }
});

test('重试起点晚于上次写出时标为重试，收到本次实际写出后进入首响阶段', () => {
  const request = {
    status: 'processing',
    timestamp: '2026-11-19T02:00:00Z',
    firstTokenStartAt: '2026-11-19T02:00:01Z',
    retryStartedAt: '2026-11-19T02:00:02Z',
    upstreamWriteMs: 1000
  };
  assert.equal(getTraceRailPhase(request).key, 'retry');
  assert.equal(getTraceRailPhase({ ...request, status: 'forwarding' }).key, 'retry');
  assert.equal(getTraceRailPhase({ ...request, firstTokenStartAt: '2026-11-19T02:00:03Z' }).key, 'first');
  assert.equal(getTraceRailPhase({ ...request, status: 'suspended' }), null);
});

test('已记录的首响和收尾优先于未更新的重试等待信息', () => {
  for (const status of ['processing', 'retry']) {
    for (const firstTokenStartAt of [undefined, '2026-11-19T02:00:01Z']) {
      const request = { status, firstTokenStartAt, retryStartedAt: '2026-11-19T02:00:02Z' };
      assert.equal(getTraceRailPhase(request).key, 'retry');
      assert.equal(getTraceRailPhase({ ...request, firstTokenMs: 0 }).key, 'stream');
      assert.equal(getTraceRailPhase({ ...request, firstTokenMs: 0, completionMs: 200 }).key, 'settling');
    }
  }
});

test('步进轨固定三段 21 格：走过的段整段亮、没到的段空槽、当前段只亮扫光两格', () => {
  assert.equal(TRACE_RAIL_CELLS, 21);
  assert.deepEqual(TRACE_RAIL_STEPS.map((step) => step.cells), [5, 8, 8]);
  assert.equal(TRACE_RAIL_STEPS.reduce((sum, step) => sum + step.cells, 0), TRACE_RAIL_CELLS);

  const timestamp = '2026-11-19T02:00:00.000Z';
  const now = Date.parse(timestamp) + 1000;
  const streaming = { status: 'processing', timestamp, upstreamWriteMs: 200, firstTokenMs: 300 };
  const heads = new Set();
  for (let offset = 0; offset < 4000; offset += 40) {
    const state = resolveStepRailCells(streaming, 'processing', now + offset);
    assert.equal(state.cells.length, TRACE_RAIL_CELLS);
    assert.deepEqual(keysIn(state, 0, PREP_END), Array(5).fill('send'));
    assert.deepEqual(keysIn(state, PREP_END, FIRST_END), Array(8).fill('first'));
    assert.ok(state.cells.slice(0, FIRST_END).every((cell) => !cell.dim), '走过的段整段亮');
    assert.deepEqual(keysIn(state, FIRST_END, 21), Array(8).fill('stream'));
    assert.ok(litIn(state, FIRST_END, 21) <= TRACE_RAIL_SWEEP_CELLS, '当前段最多亮扫光那两格');
    heads.add(state.cells.findIndex((cell, index) => index >= FIRST_END && !cell.dim));
  }
  assert.ok(heads.size > 4, '亮光应随时间在当前段内移动');

  const pending = resolveStepRailCells({ status: 'pending', timestamp }, 'pending', now);
  assert.ok(pending.cells.slice(PREP_END).every((cell) => cell.key === null), '没走到的段是空槽');

  const still = resolveStepRailCells(streaming, 'processing', now, { sweep: false });
  assert.equal(litIn(still, FIRST_END, 21), 8, '减弱动效时当前段整段静止点亮');

  assert.equal(resolveStepRailCells({ status: 'completed' }, 'completed', now), null);
  assert.equal(resolveStepRailCells({ status: 'suspended' }, 'suspended', now), null);
});

test('整格量化严格输出 21 格，0ms 终态与失败保底均不丢失', () => {
  // 0ms 失败整条满红
  const zeroFail = quantizeSegmentsToCells(
    getSettledRailSegments({ status: 'failed', failureReason: 'no_available_providers', duration: 0 }, 0)
  );
  assert.equal(zeroFail.length, TRACE_RAIL_CELLS);
  assert.ok(zeroFail.every((key) => key === 'failure'));

  // 已知阶段占满总耗时时，0ms 失败段保底至少 2 格
  const midFail = quantizeSegmentsToCells(
    getSettledRailSegments({
      status: 'failed',
      failureReason: 'rate_limited: status=429',
      duration: 300000,
      firstTokenMs: 200,
      completionMs: 299800,
      isStreaming: true
    }, 300000)
  );
  assert.equal(midFail.length, TRACE_RAIL_CELLS);
  assert.ok(midFail.filter((key) => key === 'failure').length >= 2);
  assert.ok(midFail.includes('first') && midFail.includes('stream'));

  // 含重试的多段长耗时，每段至少 1 整格且总和严格 21
  const longMulti = quantizeSegmentsToCells([
    { key: 'queue', ms: 400 },
    { key: 'retry', ms: 18100 },
    { key: 'send', ms: 470 },
    { key: 'first', ms: 223000 },
    { key: 'stream', ms: 69600 }
  ]);
  assert.equal(longMulti.length, TRACE_RAIL_CELLS);
  for (const key of ['queue', 'retry', 'send', 'first', 'stream']) {
    assert.ok(longMulti.includes(key), `分段 ${key} 至少应占 1 整格`);
  }
});

test('首响段按等待时长预警（与首响胶囊同阈值），重试段标黄，进入生成后首响段恢复紫色', () => {
  const startMs = Date.parse('2026-11-19T02:00:00Z');
  const base = {
    status: 'processing',
    timestamp: '2026-11-19T02:00:00Z',
    upstreamWriteMs: 300,
    firstTokenStartAt: '2026-11-19T02:00:00.300Z'
  };
  const at = (ms, request = base) => resolveStepRailCells(request, request.status, startMs + ms, { sweep: false });

  const normal = at(3000);
  assert.equal(normal.phase.key, 'first');
  assert.equal(normal.color, 'first');
  assert.deepEqual(keysIn(normal, PREP_END, FIRST_END), Array(8).fill('first'));
  assert.ok(normal.cells.slice(FIRST_END).every((cell) => cell.key === null));

  // 等了恰好 8 秒不变色，超过才变黄；超过 15 秒变红
  assert.equal(at(8300).color, 'first');
  assert.equal(at(9500).color, 'retry');
  assert.equal(at(18000).color, 'failure');
  assert.deepEqual(keysIn(at(18000), PREP_END, FIRST_END), Array(8).fill('failure'));

  const streaming = at(22000, { ...base, firstTokenMs: 14000, firstTokenAt: '2026-11-19T02:00:14.300Z' });
  assert.equal(streaming.phase.key, 'stream');
  assert.deepEqual(keysIn(streaming, PREP_END, FIRST_END), Array(8).fill('first'));
  assert.deepEqual(keysIn(streaming, FIRST_END, 21), Array(8).fill('stream'));

  const retry = at(6000, { ...base, status: 'retry', retryStartedAt: '2026-11-19T02:00:05Z' });
  assert.equal(retry.phase.key, 'retry');
  assert.deepEqual(keysIn(retry, PREP_END, FIRST_END), Array(8).fill('retry'));
});

test('进行中阶段文字平时中性灰，只在重试与慢首响时随轨道变黄变红', () => {
  const startMs = Date.parse('2026-11-19T02:00:00Z');
  const timestamp = '2026-11-19T02:00:00Z';
  const waiting = { status: 'processing', timestamp, upstreamWriteMs: 300, firstTokenStartAt: '2026-11-19T02:00:00.300Z' };
  const amber = TRACE_RAIL_LABEL_TONES.retry;
  const rose = TRACE_RAIL_LABEL_TONES.failure;
  const cases = [
    [{ status: 'pending', timestamp }, 0, 'send', TRACE_RAIL_LABEL_NEUTRAL],
    [{ status: 'forwarding', timestamp }, 0, 'send', TRACE_RAIL_LABEL_NEUTRAL],
    [waiting, 3000, 'first', TRACE_RAIL_LABEL_NEUTRAL],
    [waiting, 8300, 'first', TRACE_RAIL_LABEL_NEUTRAL],
    [waiting, 9500, 'retry', amber],
    [waiting, 18000, 'failure', rose],
    [{ ...waiting, status: 'retry', retryStartedAt: '2026-11-19T02:00:05Z' }, 6000, 'retry', amber],
    [{ ...waiting, firstTokenMs: 800 }, 5000, 'stream', TRACE_RAIL_LABEL_NEUTRAL],
    [{ ...waiting, firstTokenMs: 800, completionMs: 2000 }, 5000, 'stream', TRACE_RAIL_LABEL_NEUTRAL]
  ];
  for (const [request, offset, colorKey, tone] of cases) {
    const phase = getTraceRailPhase(request, request.status);
    const label = `${phase.key} @${offset}ms`;
    assert.equal(resolveStepRailColor(request, phase, startMs + offset), colorKey, label);
    assert.equal(resolveStepRailCells(request, request.status, startMs + offset).color, colorKey, label);
    assert.equal(traceRailLabelTone(colorKey), tone, label);
  }
  assert.equal(TRACE_RAIL_LABEL_NEUTRAL, 'text-fg-muted');
  assert.equal(getTraceRailPhase(waiting).label, '等首响');
  // 渲染期不读时钟：不传 now 时等首响按刚开始处理
  assert.equal(resolveStepRailColor(waiting, getTraceRailPhase(waiting), undefined), 'first');
  for (const [key, tone] of Object.entries(TRACE_RAIL_LABEL_TONES)) {
    assert.match(tone, /^text-\w+-700 dark:text-\w+-300$/, `${key} 文字色需压深，暗色下提亮`);
  }
});

test('最新一次写完距今很短时，滞后的 firstTokenStartAt 不会误报慢首响', () => {
  const startMs = Date.parse('2026-11-19T02:00:00Z');
  const request = {
    status: 'processing',
    timestamp: '2026-11-19T02:00:00Z',
    // 旧的写入时间戳停在 100ms 处，但最新一次写完已是 16000ms，距今才 100ms
    firstTokenStartAt: '2026-11-19T02:00:00.100Z',
    upstreamWriteMs: 16000
  };
  assert.equal(resolveFirstWaitMs(request, startMs + 16100), 100);
  assert.equal(resolveStepRailCells(request, 'processing', startMs + 16100).color, 'first');
});

test('查询已带回重试后的最新写出偏移而行内残留旧写出时间时，进入等待首响且不误报慢首响', () => {
  const startMs = Date.parse('2026-11-19T02:00:00Z');
  // 前一帧行对象：0.2s 首次写完、18s 触发重试
  const prevRow = {
    requestId: 'req-retry-carry',
    timestamp: '2026-11-19T02:00:00.000Z',
    status: 'retry',
    firstTokenStartAt: '2026-11-19T02:00:00.200Z',
    retryStartedAt: '2026-11-19T02:00:18.000Z',
    retryCount: 1
  };
  // 查询返回最新记录：29.9s 已写完（库里无 firstTokenStartAt / retryStartedAt）
  const queryRow = {
    requestId: 'req-retry-carry',
    timestamp: '2026-11-19T02:00:00.000Z',
    status: 'forwarding',
    upstreamWriteMs: 29900,
    retryCount: 1
  };
  const [merged] = carryLiveTiming([prevRow], [queryRow]);
  const state = resolveStepRailCells(merged, merged.status, startMs + 30000);
  assert.equal(state.phase.key, 'first');
  assert.equal(state.color, 'first');
  assert.equal(resolveFirstWaitMs(merged, startMs + 30000), 100);
});

test('重试后首响事件先到且写入时间未刷新时，按已记录的首响直接进入生成段', () => {
  const startMs = Date.parse('2026-11-19T02:00:00Z');
  const state = resolveStepRailCells({
    status: 'processing',
    timestamp: '2026-11-19T02:00:00Z',
    upstreamWriteMs: 200,
    firstTokenStartAt: '2026-11-19T02:00:00.200Z',
    retryStartedAt: '2026-11-19T02:00:18.000Z',
    firstTokenMs: 1200,
    firstTokenAt: '2026-11-19T02:00:20.000Z'
  }, 'processing', startMs + 25000, { sweep: false });
  assert.equal(state.phase.key, 'stream');
  assert.deepEqual(keysIn(state, PREP_END, FIRST_END), Array(8).fill('first'));
  assert.deepEqual(keysIn(state, FIRST_END, 21), Array(8).fill('stream'));
});

test('进行中总耗时显示占位符，跑秒只留在计时胶囊，结束才到货', () => {
  assert.match(RAIL_CODE, /finalMs === null \? '—' : formatTimingBadge\(finalMs\)/);
  assert.doesNotMatch(RAIL_CODE, /resolveElapsedMs|timeRef|textContent/, '状态列不应再跑总耗时');
  // 进行中状态文字按轨道当前段判定：平时中性灰，重试与慢首响变黄变红，由共享时钟和格子同一拍改写
  assert.match(RAIL_CODE, /paintLabel\(labelRef\.current, state\.color\)/);
  assert.match(RAIL_CODE, /isLive \? liveLabelClassName\(resolveStepRailColor\(request, phase\)\)/);
});

test('railSegmentColors 覆盖 buildLifecycleSegments 可能产出的全部 key', () => {
  // 三种降级路径 + 两种终态，覆盖 lifecycle.js 注释里列的 5 条规则
  const samples = [
    { duration: 1200 },                                                             // 规则 1：全无 timing
    { duration: 1200, firstTokenMs: 300, completionMs: 800 },                       // 规则 2：旧数据
    { duration: 1200, upstreamWriteMs: 200, firstTokenMs: 300, completionMs: 600 }, // 规则 3：无 routeDecisionAt
    {
      duration: 1200,
      timestamp: '2026-08-30T00:00:00.000000Z',
      routeDecisionAt: '2026-08-30T00:00:00.050000Z',
      upstreamWriteMs: 200,
      firstTokenMs: 300,
      completionMs: 400
    },                                                                              // 规则 4：全量
    { duration: 1200, firstTokenMs: 300, status: 'failed', failureReason: 'rate_limit: x' },
    { duration: 1200, firstTokenMs: 300, status: 'cancelled', cancelReason: 'client disconnect' }
  ];

  const seen = new Set();
  for (const sample of samples) {
    for (const segment of buildLifecycleSegments(sample)) seen.add(segment.key);
  }
  assert.ok(seen.size >= 5, `样本只覆盖到 ${seen.size} 种分段，不足以验证配色表`);

  for (const key of seen) {
    assert.ok(railSegmentColors[key], `分段 ${key} 缺轨道配色，会掉进 total 兜底而丢失语义`);
  }
});

test('两套分段配色的 key 完全一致', () => {
  // 轨道版与详情条版必须同构：同一个 buildLifecycleSegments 的输出
  // 要能同时喂给两边，任何一边缺 key 都会静默降级成灰色。
  assert.deepEqual(
    Object.keys(railSegmentColors).sort(),
    Object.keys(lifecycleSegmentColors).sort()
  );
});

test('格状轨道在深浅主题使用可辨别的亮色', () => {
  for (const [key, value] of Object.entries(railSegmentColors)) {
    assert.doesNotMatch(value, /bg-\w+-(700|800|900)/,
      `${key} 的轨道色 ${value} 太深；需要压深的是详情条那套（${lifecycleSegmentColors[key]}），它要承载浅色标签文字`);
  }
  // 反向确认分叉理由成立：详情条那套确实存在深色阶，两者不是无谓的重复。
  const deepInDetail = Object.values(lifecycleSegmentColors).filter((value) => /bg-\w+-(700|800)/.test(value));
  assert.ok(deepInDetail.length > 0, '详情条配色里没有深色阶，两套配色的分叉理由已不成立');
});

test('运行中复用共享时钟原地更新格子，终态与挂起停止订阅', () => {
  assert.match(RAIL_CODE, /resolveStepRailCells\(/);
  assert.match(RAIL_CODE, /subscribeElapsedTick\(paint\)/);
  assert.match(RAIL_CODE, /if \(!isLive\) return undefined;/);
  assert.doesNotMatch(RAIL_CODE, /useState|setInterval|requestAnimationFrame/);
});

test('入场与让位作用于整行，叠加位移并保留整行背景和事件命中', () => {
  assert.match(CSS_SOURCE, /^\.requests-table tr\.request-row-enter,\n[^{}]+\{\s*animation: request-row-rise/m);
  assert.match(CSS_SOURCE, /@keyframes request-row-rise \{\s*from \{[^}]*translate: 0 -8px/);
  for (const phase of ['prep', 'run']) {
    assert.match(CSS_SOURCE, new RegExp(`^\\.requests-table tbody tr\\[data-row-shift="${phase}"\\] \\{[^}]*transform: translateY`, 'm'));
  }
});

test('在途时 token 与成本显示占位符而不是 0', () => {
  // 后端只在终态写这几个字段，此刻的 0 是「还不知道」而不是「用了 0 个」。
  // 而且 0 是个合法数字 —— 从 0 换成别的数字眼睛不会注意到，
  // 从 — 变成数字才是一次「到货」，那才是完成瞬间最强的信号。
  assert.match(TABLE_CODE, /const PENDING_VALUE = '—'/, '占位符必须是破折号，不能是 0');
  assert.match(TABLE_CODE, /pending \? PENDING_VALUE : children/);

  // 已落定但用量未知（usage_known / cost_known 为 false）同理：没收到 usage 的
  // 失败/取消记录，0 是「还不知道」而不是「用了 0 个」。
  assert.match(TABLE_CODE, /const usagePending = pending \|\| request\.usageKnown === false/,
    'usage_known=false 的记录必须显示占位符');

  for (const col of ['inputTokens', 'outputTokens', 'cacheCreationTokens', 'cacheReadTokens', 'cost']) {
    assert.match(TABLE_CODE, new RegExp(`case '${col}':[\\s\\S]{0,160}?<ValueCell`),
      `${col} 没走 ValueCell，在途时会显示 0`);
  }

  // 判据是「还没落定」而不是「在跑」：suspended 不该有流光和跑秒，
  // 但它的 token 同样还没被写入。
  assert.equal(UNSETTLED_STATUSES.has('suspended'), true,
    'suspended 的请求没结束，token 还是 0，显示 0 等于陈述假事实');
  for (const status of IN_FLIGHT) {
    assert.equal(UNSETTLED_STATUSES.has(status), true, `${status} 必须显示占位符`);
  }
  for (const status of ['completed', 'failed', 'cancelled', 'timeout']) {
    assert.equal(UNSETTLED_STATUSES.has(status), false, `${status} 已落定，必须显示真实数字`);
  }
});

test('入场 class 的摘除认 animationName，且已移除左缘 2px 闪动亮线并接入 65ms 让位错峰', () => {
  const finalName = TABLE_SOURCE.match(/const FINAL_ENTER_ANIMATION = '([^']+)'/)?.[1];
  assert.equal(finalName, 'request-row-rise');

  // 必须是 CSS 里真实存在的 @keyframes，否则 animationend 永不匹配，
  // class 只能靠 setTimeout 兜底摘掉。
  assert.match(CSS_SOURCE, new RegExp(`@keyframes ${finalName}\\b`),
    `${finalName} 在 index.css 里没有对应的 @keyframes`);
  assert.doesNotMatch(CSS_SOURCE, /request-row-edge-tick/, '左缘 2px 亮线会造成硬闪，应彻底移除');
  assert.doesNotMatch(TABLE_SOURCE, /request-row-edge-tick/, 'RequestsTable 不应再引用已移除的左缘亮线');
  assert.match(TABLE_SOURCE, /const ROW_ENTER_SHIFT_LEAD_MS = 65;/);
  assert.match(TABLE_SOURCE, /const ROW_SHIFT_FLIP_MS = 220;/);
  assert.match(CSS_SOURCE, /animation: request-row-rise 220ms/);
});

test('旧的整行扫光与逐列接力已彻底移除', () => {
  // 扫光是 infinite 动画，留一条在 CSS 里就会有行永远在动。
  for (const dead of ['request-row-inflight', 'request-row-enter-cascade', 'request-row-enter-flat', 'request-cell-enter']) {
    assert.doesNotMatch(CSS_SOURCE, new RegExp(dead), `index.css 仍残留 ${dead}`);
    assert.doesNotMatch(TABLE_SOURCE, new RegExp(dead), `RequestsTable 仍引用 ${dead}`);
  }
  assert.doesNotMatch(TABLE_SOURCE, /--col-index/, '逐列接力已移除，--col-index 不应再下发');
});

test('21 格独立网格无亚像素，进行中不留原地明暗循环', () => {
  const rail = CSS_SOURCE.match(/\.trace-rail \{[^}]*\}/)?.[0];
  assert.match(rail, /grid-template-columns: repeat\(21, 4px\)/);
  assert.match(rail, /gap: 2px/);
  assert.match(rail, /width: 124px/);
  assert.match(rail, /height: 4px/);
  assert.doesNotMatch(CSS_SOURCE, /trace-rail__cell--cursor|@keyframes trace-rail-cursor/, '光标的原地明暗循环已移除');
  assert.doesNotMatch(RAIL_CODE, /trace-rail__cell--cursor/);
  assert.match(CSS_SOURCE, /\.trace-rail__cell--dim \{\s*opacity: \.3;\s*\}/);
  // 空槽背景必须使用独立类，不能写在基础 .trace-rail__cell 上覆盖 Tailwind bg-*
  const baseCell = CSS_SOURCE.match(/\.trace-rail__cell \{[^}]*\}/)?.[0];
  assert.doesNotMatch(baseCell, /background/);
  assert.doesNotMatch(RAIL_CODE, /key=\{request\.timestamp\}/);
});

test('减弱动效时当前段整段静止点亮，关闭扫光过渡与结束换色', () => {
  const reducedBlock = CSS_SOURCE.match(/@media \(prefers-reduced-motion: reduce\)[\s\S]*?\n}\n/)?.[0];
  assert.ok(reducedBlock, 'reduced-motion 段落未找到');
  assert.match(reducedBlock, /\.trace-rail--live \.trace-rail__cell \{[^}]*transition: none/);
  assert.match(reducedBlock, /\.trace-rail\[data-settling\] \.trace-rail__cell[\s\S]*?transition: none/);
  assert.match(RAIL_CODE, /sweep: !prefersReducedMotion\(\)/);
  assert.match(RAIL_CODE, /if \(prefersReducedMotion\(\)\) return undefined;/);
});

test('状态和轨道排在原有 32px 表格行高之内', () => {
  assert.match(RAIL_CODE, /inline-flex w-\[124px\] h-6 flex-col/);
  assert.match(CSS_SOURCE, /\.trace-rail \{[^}]*width: 124px;[^}]*height: 4px;/);
  assert.match(CSS_SOURCE, /\.desk-table td \{[^}]*height: 32px;/);
});

test('轨道退出可访问性树 —— 状态不能被读屏念两遍', () => {
  // 独立的状态文字和计时承担可访问内容，格子退出可访问性树。
  assert.match(RAIL_CODE, /aria-hidden="true"/, '轨道必须 aria-hidden');
  assert.doesNotMatch(RAIL_CODE, /role="img"/, 'aria-hidden 之后 role 已无意义，留着会误导');
  assert.doesNotMatch(RAIL_CODE, /aria-label/, '可访问名由旁边的可见文字承担');

  // 名称的唯一来源，使用与轨道一致的展示分类，删掉文字就只剩颜色编码了
  const traceCell = RAIL_CODE.slice(RAIL_CODE.indexOf('export const RequestTraceCell'));
  assert.match(traceCell, /getRequestPresentation\(request\)/,
    'RequestTraceCell 必须使用统一的展示分类');
  assert.match(traceCell, /getStatusConfig\(status\)\.label/,
    'RequestTraceCell 必须保留可见状态文字');
});

test('状态列排在首位 —— 轨道是这张表的扫描锚点', () => {
  assert.equal(TABLE_COLUMNS[0].id, 'status',
    '状态被挤到后面，轨道就会被长请求 ID 和时间戳隔断，动态感读不出来');
});

test('表头与数据格同源 —— 否则表头会盖在错误的数据列上', () => {
  // 曾经 <td> 遍历 visibleColumns（state 数组）、<thead> 遍历 TABLE_COLUMNS，
  // 两者对得上纯粹因为默认数组被手抄成了同序。TABLE_COLUMNS 一调顺序就整体错位，
  // 而且勾一下任意列就会自愈（toggleColumn 会按 TABLE_COLUMNS 重排），只在首屏错。
  assert.match(TABLE_CODE, /columns=\{visibleColumnConfigs\}/,
    '数据行必须收表头用的那一份列配置');
  assert.doesNotMatch(TABLE_CODE, /visibleColumns\.map\(/,
    '<td> 不能再按 state 数组的顺序渲染');

  // defaultHidden 列不进入默认可见列，派生时仍保持 TABLE_COLUMNS 的顺序。
  assert.deepEqual(DEFAULT_VISIBLE_COLUMNS, TABLE_COLUMNS.filter(col => !col.defaultHidden).map(col => col.id),
    '默认可见列必须派生自 TABLE_COLUMNS，手抄的版本会漏列、写错 id、失配顺序');
  assert.ok(!DEFAULT_VISIBLE_COLUMNS.includes('sessionId'),
    '会话列默认隐藏，不应进入默认可见列');
});

test('右对齐只有一份定义 —— 不靠列 id 猜', () => {
  assert.match(TABLE_CODE, /col\.align === 'right'/, '对齐应该读列配置');
  assert.doesNotMatch(TABLE_CODE, /colId\.includes\('Tokens'\)/,
    'align 已经在 TABLE_COLUMNS 里，再按 id 猜就是同一份信息抄了两遍');
});

test('状态列宽度写死 —— 它一抖后面每一列都跟着右移', () => {
  // table-layout:auto 下列宽取当页内容的最大值。状态列现在排首位，
  // 而 getStatusConfig 的兜底会把未知状态的 label 退化成后端原始串。
  assert.match(RAIL_CODE, /inline-flex w-\[\d+px\]/,
    'RequestTraceCell 外层必须有固定宽度');
  assert.match(RAIL_CODE, /truncate/,
    '标签必须能被截住，否则未知状态的原始串会把整列撑开');
});

test('轨道与耗时节点恒定不卸载重建，收尾走平滑色块过渡而非闪暗关键帧', () => {
  assert.doesNotMatch(RAIL_CODE, /key=\{isLive \? 'live' : 'static'\}/, '切换终态不能卸载重建轨道 DOM');
  assert.doesNotMatch(RAIL_CODE, /key=\{canTick \? 'live' : 'static'\}/, '切换终态不能卸载重建耗时节点');
  assert.doesNotMatch(CSS_SOURCE, /@keyframes trace-rail-settle\b/, '旧版闪暗+缩放关键帧应彻底移除');
  assert.match(CSS_SOURCE, /\.trace-rail\[data-settling\] \.trace-rail__cell \{[^}]*transition: background-color 220ms/);
  assert.match(CSS_SOURCE, /\.trace-rail__label \{[^}]*transition: color/);
  assert.match(TABLE_CODE, /<RequestTraceCell request=\{request\} \/>/);
  assert.match(TABLE_CODE, /useRowShiftFlip\(/);
  const reducedBlock = CSS_SOURCE.match(/@media \(prefers-reduced-motion: reduce\)[\s\S]*?\n}\n/)?.[0];
  assert.match(reducedBlock, /\.trace-rail\[data-settling\] \.trace-rail__cell[\s\S]*?transition: none/);
  assert.match(reducedBlock, /\.requests-table tbody tr\[data-row-shift="run"\][\s\S]*?transition: none/);
  // 防整屏闪烁约束：请求表使用行身份稳定斑马纹与统一灰度抗锯齿，禁止 td 背景色过渡反色频闪
  assert.match(CSS_SOURCE, /\.requests-table \{\s*-webkit-font-smoothing: antialiased;\s*\}/);
  assert.doesNotMatch(CSS_SOURCE, /\.requests-table tbody tr > td \{[^}]*transition: background-color/);
  assert.match(CSS_SOURCE, /\.desk-table\.requests-table tbody tr\[data-zebra="1"\] > td \{/);
});

// ============================================================
// 运行时回归测试：直接编译生产 Hook，验证轨道与表格的动效边界
// ============================================================

const sameDeps = (left, right) => Boolean(left && right && left.length === right.length && left.every((val, idx) => Object.is(val, right[idx])));

function createHookRunner() {
  const slots = [];
  const effects = [];
  let cursor = 0;

  const hooks = {
    useRef(initialValue) {
      const idx = cursor++;
      if (!(idx in slots)) slots[idx] = { current: initialValue };
      return slots[idx];
    },
    useCallback(fn, deps) {
      const idx = cursor++;
      const prev = slots[idx];
      if (!prev || !sameDeps(prev.deps, deps)) {
        slots[idx] = { fn, deps };
      }
      return slots[idx].fn;
    },
    useLayoutEffect(cb, deps) {
      const idx = cursor++;
      effects[idx] = { kind: 'layout', cb, deps, prevDeps: effects[idx]?.prevDeps, cleanup: effects[idx]?.cleanup };
    },
    useEffect(cb, deps) {
      const idx = cursor++;
      effects[idx] = { kind: 'effect', cb, deps, prevDeps: effects[idx]?.prevDeps, cleanup: effects[idx]?.cleanup };
    }
  };

  const render = (fn) => {
    cursor = 0;
    const result = fn();
    for (const slot of effects) {
      if (!slot) continue;
      if (!sameDeps(slot.prevDeps, slot.deps)) {
        slot.cleanup?.();
        slot.prevDeps = slot.deps;
        slot.cleanup = slot.cb?.() || undefined;
      }
    }
    return result;
  };

  const simulateStrictRemount = () => {
    for (const slot of effects) {
      if (!slot) continue;
      slot.cleanup?.();
      slot.cleanup = undefined;
    }
    for (const slot of effects) {
      if (!slot) continue;
      slot.cleanup = slot.cb?.() || undefined;
    }
  };

  const unmount = () => {
    for (const slot of effects) {
      if (!slot) continue;
      slot.cleanup?.();
      slot.cleanup = undefined;
    }
  };

  return { hooks, render, simulateStrictRemount, unmount };
}

let compiledTraceHook;
async function loadTraceRailHook() {
  if (compiledTraceHook) return compiledTraceHook;
  const compiled = await build({
    stdin: {
      contents: `${RAIL_SOURCE}\nexport { useTraceRail };`,
      loader: 'jsx',
      resolveDir: HERE
    },
    bundle: true,
    write: false,
    platform: 'node',
    format: 'esm',
    jsx: 'automatic',
    alias: {
      '@utils': join(HERE, '../../../utils')
    },
    plugins: [{
      name: 'trace-hook-env',
      setup(builder) {
        builder.onResolve({ filter: /^react$/ }, () => ({ path: 'react', namespace: 'trace-hook' }));
        builder.onLoad({ filter: /.*/, namespace: 'trace-hook' }, () => ({
          contents: ['useRef', 'useCallback', 'useLayoutEffect', 'useEffect']
            .map((name) => `export const ${name} = (...args) => globalThis.__traceHookHost.${name}(...args);`)
            .join('\n')
        }));
      }
    }]
  });
  compiledTraceHook = await import(`data:text/javascript;base64,${Buffer.from(compiled.outputFiles[0].text).toString('base64')}`);
  return compiledTraceHook;
}

let compiledFlipHook;
async function loadRowShiftFlipHook() {
  if (compiledFlipHook) return compiledFlipHook;
  const compiled = await build({
    stdin: {
      contents: `${TABLE_SOURCE}\nexport { useRowShiftFlip };`,
      loader: 'jsx',
      resolveDir: HERE
    },
    bundle: true,
    write: false,
    platform: 'node',
    format: 'esm',
    jsx: 'automatic',
    alias: {
      '@components': join(HERE, '../../../components'),
      '@utils': join(HERE, '../../../utils'),
      '@contexts': join(HERE, '../../../contexts')
    },
    plugins: [{
      name: 'flip-hook-env',
      setup(builder) {
        builder.onResolve({ filter: /^react$/ }, () => ({ path: 'react', namespace: 'flip-hook' }));
        builder.onResolve({ filter: /^(lucide-react|@components\/ui|@contexts\/TimezoneContext\.jsx|@utils\/api\.js|\.\/TraceRail\.jsx|\.\/RequestTimingCell\.jsx|\.\/ModelTag\.jsx|\.\/PrivacyWarningBadge\.jsx)$/ }, ({ path }) => ({
          path, namespace: 'flip-stub'
        }));
        builder.onLoad({ filter: /.*/, namespace: 'flip-hook' }, () => ({
          contents: 'export default { useRef: (...a) => globalThis.__flipHookHost.useRef(...a), useLayoutEffect: (...a) => globalThis.__flipHookHost.useLayoutEffect(...a), useEffect: (...a) => globalThis.__flipHookHost.useEffect(...a), useCallback: (...a) => a[0], useState: (v) => [typeof v === "function" ? v() : v, () => {}], useMemo: (fn) => fn(), memo: (c) => c };'
        }));
        builder.onLoad({ filter: /.*/, namespace: 'flip-stub' }, ({ path }) => ({
          contents: path === 'lucide-react'
            ? 'export const Waves = () => null; export const RefreshCw = () => null;'
            : path === '@components/ui'
              ? 'export const LoadingSpinner = () => null;'
              : path === '@contexts/TimezoneContext.jsx'
                ? 'export const useTimezone = () => ({ formatListTimestamp: (v) => v });'
                : path === '@utils/api.js'
                  ? 'export const formatCost = (v) => String(v);'
                  : 'export const RequestTraceCell = () => null; export default () => null;'
        }));
      }
    }]
  });
  compiledFlipHook = await import(`data:text/javascript;base64,${Buffer.from(compiled.outputFiles[0].text).toString('base64')}`);
  return compiledFlipHook;
}

function createFakeBrowserEnv(t) {
  let perfNow = 1000;
  let nextId = 1;
  const rafQueue = new Map();
  const timers = new Map();
  const origWindow = globalThis.window;
  const origPerf = globalThis.performance;
  const origDOMMatrix = globalThis.DOMMatrixReadOnly;

  const fakeWindow = {
    innerHeight: 640,
    matchMedia: () => ({ matches: false }),
    requestAnimationFrame: (cb) => {
      const id = nextId++;
      rafQueue.set(id, cb);
      return id;
    },
    cancelAnimationFrame: (id) => {
      rafQueue.delete(id);
    },
    setTimeout: (cb, ms = 0) => {
      const id = nextId++;
      timers.set(id, { cb, at: perfNow + ms });
      return id;
    },
    clearTimeout: (id) => {
      timers.delete(id);
    },
    getComputedStyle: () => ({ transform: 'none' })
  };

  globalThis.window = fakeWindow;
  Object.defineProperty(globalThis, 'performance', {
    configurable: true,
    value: { now: () => perfNow }
  });
  globalThis.DOMMatrixReadOnly = class FakeDOMMatrix {
    constructor(value) {
      const match = String(value).match(/translateY\(([-\d.]+)px\)/);
      this.m42 = match ? Number(match[1]) : 0;
    }
  };

  t.after(() => {
    globalThis.window = origWindow;
    Object.defineProperty(globalThis, 'performance', { configurable: true, value: origPerf });
    globalThis.DOMMatrixReadOnly = origDOMMatrix;
  });

  return {
    window: fakeWindow,
    advanceRaf(deltaMs) {
      perfNow += deltaMs;
      const pending = [...rafQueue.entries()];
      rafQueue.clear();
      for (const [, cb] of pending) cb(perfNow);
    },
    flushTimers(deltaMs) {
      perfNow += deltaMs;
      for (const [id, entry] of [...timers.entries()]) {
        if (entry.at <= perfNow) {
          timers.delete(id);
          entry.cb();
        }
      }
    }
  };
}

function createFakeRailNode() {
  return {
    dataset: {},
    children: Array.from({ length: TRACE_RAIL_CELLS }, () => ({ className: 'trace-rail__cell trace-rail__cell--empty' }))
  };
}

function renderTraceHook(runner, useTraceRail, rail, request, label = null) {
  const { status } = request;
  const isLive = Boolean(getTraceRailPhase(request, status));
  const finalMs = !UNSETTLED_STATUSES.has(status) && Number.isFinite(request.duration) ? request.duration : null;
  const segments = isLive ? [] : getSettledRailSegments(request, finalMs);
  const settledCellKeys = segments.length > 0
    ? quantizeSegmentsToCells(segments, TRACE_RAIL_CELLS)
    : Array.from({ length: TRACE_RAIL_CELLS }, () => null);
  runner.render(() => {
    const refs = useTraceRail({ request, status, isLive, settledCellKeys });
    refs.railRef.current = rail;
    if (label) refs.labelRef.current = label;
  });
}

const countCells = (rail, pattern) => rail.children.filter((node) => node.className.includes(pattern)).length;

test('计时数据晚到（结束换色播放中补入）时，轨道立即更新为最新分段配色，不打断也不残留灰格', async (t) => {
  const runner = createHookRunner();
  globalThis.__traceHookHost = runner.hooks;
  t.after(() => {
    runner.unmount();
    delete globalThis.__traceHookHost;
  });
  const env = createFakeBrowserEnv(t);
  const { useTraceRail } = await loadTraceRailHook();
  const rail = createFakeRailNode();
  const base = { requestId: 'req-late', timestamp: '2026-11-19T02:00:00.000Z' };

  // 1. 先在途运行（准备段点亮），再以仅含总耗时（全灰）的状态落定
  renderTraceHook(runner, useTraceRail, rail, { ...base, status: 'processing' });
  assert.ok(countCells(rail, 'trace-rail__cell--empty') < TRACE_RAIL_CELLS, '进行中应点亮准备段');
  renderTraceHook(runner, useTraceRail, rail, { ...base, status: 'completed', duration: 1000 });
  assert.equal(rail.dataset.settling, '', '从进行中切到终态应播一次换色');
  assert.equal(countCells(rail, 'bg-slate-300'), 21);

  // 2. 换色播放中补入首响 200ms + 流式输出 800ms（应为 5 格紫 + 16 格绿）
  renderTraceHook(runner, useTraceRail, rail, {
    ...base, status: 'completed', duration: 1000, firstTokenMs: 200, completionMs: 800, isStreaming: true
  });
  assert.equal(countCells(rail, 'bg-violet-400'), 5);
  assert.equal(countCells(rail, 'bg-emerald-400'), 16);
  assert.equal(rail.dataset.settling, '', '计时晚到不打断正在播的换色');

  // 3. 换色结束后摘掉标记，21 格依然保持 5 紫 + 16 绿，绝不回退全灰
  env.flushTimers(TRACE_RAIL_SETTLE_MS + 50);
  assert.equal(rail.dataset.settling, undefined);
  assert.equal(countCells(rail, 'bg-violet-400'), 5);
  assert.equal(countCells(rail, 'bg-emerald-400'), 16);
});

test('首次出现即已结束的行直接按比例着色，不补播结束换色（StrictMode 双跑也不例外）', async (t) => {
  const runner = createHookRunner();
  globalThis.__traceHookHost = runner.hooks;
  t.after(() => {
    runner.unmount();
    delete globalThis.__traceHookHost;
  });
  createFakeBrowserEnv(t);
  const { useTraceRail } = await loadTraceRailHook();
  const rail = createFakeRailNode();

  renderTraceHook(runner, useTraceRail, rail, {
    requestId: 'req-fast-strict',
    status: 'completed',
    timestamp: '2026-11-19T02:00:00.000Z',
    duration: 180,
    firstTokenMs: 60,
    completionMs: 120,
    isStreaming: true
  });
  runner.simulateStrictRemount();

  assert.equal(rail.dataset.settling, undefined, '已有整行入场，不再叠一次换色');
  assert.equal(countCells(rail, 'trace-rail__cell--empty'), 0, '首帧就是终态配色');
  assert.ok(countCells(rail, 'bg-violet-400') > 0 && countCells(rail, 'bg-emerald-400') > 0);
});

test('请求挂起时不播结束换色，挂起后真正结束才播一次', async (t) => {
  const runner = createHookRunner();
  globalThis.__traceHookHost = runner.hooks;
  t.after(() => {
    runner.unmount();
    delete globalThis.__traceHookHost;
  });
  const env = createFakeBrowserEnv(t);
  const { useTraceRail } = await loadTraceRailHook();
  const rail = createFakeRailNode();
  const base = { requestId: 'req-suspend', timestamp: '2026-11-19T02:00:00.000Z' };

  renderTraceHook(runner, useTraceRail, rail, { ...base, status: 'processing' });
  renderTraceHook(runner, useTraceRail, rail, { ...base, status: 'suspended' });
  assert.equal(rail.dataset.settling, undefined, '挂起没有结束，不播结束换色');
  assert.equal(countCells(rail, 'trace-rail__cell--empty'), TRACE_RAIL_CELLS, '挂起显示空槽');

  renderTraceHook(runner, useTraceRail, rail, {
    ...base, status: 'completed', duration: 1000, firstTokenMs: 200, completionMs: 800, isStreaming: true
  });
  assert.equal(rail.dataset.settling, '', '挂起后真正结束仍播一次');
  assert.equal(countCells(rail, 'bg-violet-400'), 5);
  env.flushTimers(TRACE_RAIL_SETTLE_MS + 50);
  assert.equal(rail.dataset.settling, undefined);
});

test('慢首响时文字被时钟改成黄色，之后取消或挂起都会写回终态文字色', async (t) => {
  createFakeBrowserEnv(t);
  const { useTraceRail } = await loadTraceRailHook();
  for (const next of [{ status: 'cancelled', duration: 12000 }, { status: 'suspended' }]) {
    const runner = createHookRunner();
    globalThis.__traceHookHost = runner.hooks;
    const rail = createFakeRailNode();
    const label = { className: '' };
    const base = {
      requestId: `req-slow-${next.status}`,
      timestamp: new Date(Date.now() - 12_000).toISOString(),
      upstreamWriteMs: 300
    };
    renderTraceHook(runner, useTraceRail, rail, { ...base, status: 'processing' }, label);
    assert.ok(label.className.includes(TRACE_RAIL_LABEL_TONES.retry), `等首响超过 8 秒应变黄：${label.className}`);

    renderTraceHook(runner, useTraceRail, rail, { ...base, ...next }, label);
    assert.ok(label.className.includes(TRACE_RAIL_LABEL_NEUTRAL), `${next.status} 应写回中性灰：${label.className}`);
    assert.ok(!label.className.includes('amber'), `${next.status} 不应残留黄色`);
    runner.unmount();
  }
  delete globalThis.__traceHookHost;
});

test('同拍、休止期与减弱动效下帧签名稳定，跨拍或跨慢首响阈值才触发重绘', () => {
  const startMs = Date.parse('2026-11-19T02:00:00.000Z');
  const waiting = {
    requestId: 'req-frame-key',
    status: 'processing',
    timestamp: '2026-11-19T02:00:00.000Z',
    upstreamWriteMs: 0
  };
  const meta = resolveStepRailMeta(waiting, 'processing', 0);

  // 同一个 120ms 步进窗口内签名不变；跨入下一拍立即变化
  const step0 = startMs;
  assert.equal(
    resolveStepRailFrameKey(meta, step0),
    resolveStepRailFrameKey(meta, step0 + TRACE_RAIL_SWEEP_STEP_MS - 1)
  );
  assert.notEqual(
    resolveStepRailFrameKey(meta, step0),
    resolveStepRailFrameKey(meta, step0 + TRACE_RAIL_SWEEP_STEP_MS)
  );

  // 扫出 8 格段尾后的 5 拍休止期视觉全暗，签名折叠为同一值
  const restStart = (8 + TRACE_RAIL_SWEEP_CELLS - 1) * TRACE_RAIL_SWEEP_STEP_MS;
  const restKey = resolveStepRailFrameKey(meta, step0 + restStart);
  for (let step = 1; step <= 4; step += 1) {
    assert.equal(resolveStepRailFrameKey(meta, step0 + restStart + step * TRACE_RAIL_SWEEP_STEP_MS), restKey);
  }
  const restState = resolveStepRailCells(waiting, 'processing', step0 + restStart, { meta });
  assert.equal(litIn(restState, PREP_END, FIRST_END), 0, '休止期当前段应全暗');

  // 减弱动效时仅跨慢首响阈值（8s / 15s）才改变签名
  const stillEarly = resolveStepRailFrameKey(meta, startMs + 1000, { sweep: false });
  assert.equal(resolveStepRailFrameKey(meta, startMs + 5000, { sweep: false }), stillEarly);
  const stillWarn = resolveStepRailFrameKey(meta, startMs + 8500, { sweep: false });
  assert.notEqual(stillWarn, stillEarly);
  assert.notEqual(resolveStepRailFrameKey(meta, startMs + 15500, { sweep: false }), stillWarn);

  // retry 与已超 8s 的 first 虽然同属 step 1 且同为黄色，签名仍须区分 phase.key
  const retryMeta = resolveStepRailMeta({ ...waiting, status: 'retry', retryStartedAt: '2026-11-19T02:00:01.000Z' }, 'retry', 0);
  assert.notEqual(
    resolveStepRailFrameKey(retryMeta, startMs + 8500, { sweep: false }),
    stillWarn
  );
});

test('挂起后同拍恢复进行中，以及 retry 切入慢首响时，均不会被帧缓存拦住', async (t) => {
  const runner = createHookRunner();
  globalThis.__traceHookHost = runner.hooks;
  t.after(() => {
    runner.unmount();
    delete globalThis.__traceHookHost;
  });
  createFakeBrowserEnv(t);
  const { useTraceRail } = await loadTraceRailHook();
  const rail = createFakeRailNode();
  const label = { className: '' };
  const base = {
    requestId: 'req-resume',
    timestamp: new Date(Date.now() - 10_000).toISOString(),
    upstreamWriteMs: 100
  };

  // 1. 进行中 → 挂起（清成空槽）→ 立刻恢复进行中，必须立刻重新点亮而不是停在空槽
  renderTraceHook(runner, useTraceRail, rail, { ...base, status: 'processing' }, label);
  assert.ok(countCells(rail, 'trace-rail__cell--empty') < TRACE_RAIL_CELLS);
  renderTraceHook(runner, useTraceRail, rail, { ...base, status: 'suspended' }, label);
  assert.equal(countCells(rail, 'trace-rail__cell--empty'), TRACE_RAIL_CELLS);
  renderTraceHook(runner, useTraceRail, rail, { ...base, status: 'processing' }, label);
  assert.ok(countCells(rail, 'trace-rail__cell--empty') < TRACE_RAIL_CELLS, '恢复进行中后应立即重绘');

  // 2. retry 切入已超 8s 的慢首响（模拟 React 先把 label 改回中性灰），layout effect 必须立刻写回黄色
  const retryReq = { ...base, status: 'retry', retryStartedAt: new Date(Date.now() - 1000).toISOString() };
  renderTraceHook(runner, useTraceRail, rail, retryReq, label);
  assert.ok(label.className.includes(TRACE_RAIL_LABEL_TONES.retry));
  label.className = `trace-rail__label ${TRACE_RAIL_LABEL_NEUTRAL}`;
  renderTraceHook(runner, useTraceRail, rail, { ...base, status: 'processing' }, label);
  assert.ok(label.className.includes(TRACE_RAIL_LABEL_TONES.retry), '切入慢首响后文字应立刻保持黄色');
});

test('跨 requestId 复用组件时，前一条在途记录不会让新终态行误播结束换色', async (t) => {
  const runner = createHookRunner();
  globalThis.__traceHookHost = runner.hooks;
  t.after(() => {
    runner.unmount();
    delete globalThis.__traceHookHost;
  });
  const env = createFakeBrowserEnv(t);
  const { useTraceRail } = await loadTraceRailHook();
  const rail = createFakeRailNode();

  renderTraceHook(runner, useTraceRail, rail, {
    requestId: 'req-a',
    status: 'processing',
    timestamp: '2026-11-19T02:00:00.000Z'
  });
  renderTraceHook(runner, useTraceRail, rail, {
    requestId: 'req-b',
    status: 'completed',
    timestamp: '2026-11-19T02:00:01.000Z',
    duration: 400,
    firstTokenMs: 100,
    completionMs: 300,
    isStreaming: true
  });
  assert.equal(rail.dataset.settling, undefined, '切换到另一条已结束请求不应补播换色');

  // 正在播结束换色时切到另一条已结束请求，也应立即撤掉旧换色标记
  renderTraceHook(runner, useTraceRail, rail, {
    requestId: 'req-c',
    status: 'processing',
    timestamp: '2026-11-19T02:00:02.000Z'
  });
  renderTraceHook(runner, useTraceRail, rail, {
    requestId: 'req-c',
    status: 'completed',
    timestamp: '2026-11-19T02:00:02.000Z',
    duration: 500,
    firstTokenMs: 200,
    completionMs: 300,
    isStreaming: true
  });
  assert.equal(rail.dataset.settling, '');
  renderTraceHook(runner, useTraceRail, rail, {
    requestId: 'req-d',
    status: 'completed',
    timestamp: '2026-11-19T02:00:03.000Z',
    duration: 500,
    firstTokenMs: 200,
    completionMs: 300,
    isStreaming: true
  });
  assert.equal(rail.dataset.settling, undefined);
  env.flushTimers(TRACE_RAIL_SETTLE_MS + 50);
  assert.equal(rail.dataset.settling, undefined);
});

async function createRowShiftFixture(t, { scrolling = false, height = 640 } = {}) {
  const runner = createHookRunner();
  globalThis.__flipHookHost = runner.hooks;
  t.after(() => {
    runner.unmount();
    delete globalThis.__flipHookHost;
  });
  const env = createFakeBrowserEnv(t);
  const { useRowShiftFlip } = await loadRowShiftFlipHook();

  const ops = [];
  const scroller = {
    scrollTop: 0,
    scrollTo({ top }) {
      ops.push('scrollTo');
      this.scrollTop = top;
    },
    matches: () => true,
    getBoundingClientRect: () => ({ top: 0, bottom: height })
  };
  const metrics = { styleReads: [], flushes: 0, activeOffsetY: 0 };

  env.window.getComputedStyle = (tr) => {
    metrics.styleReads.push(tr.dataset.rowId);
    ops.push('read:style');
    return { transform: metrics.activeOffsetY ? `translateY(${metrics.activeOffsetY}px)` : 'none' };
  };

  const makeRow = (id, index, entering = false) => {
    const props = new Map();
    return {
      dataset: { rowId: id },
      className: entering ? 'group request-row-enter' : 'group',
      index,
      offsetHeight: 32,
      get offsetTop() { return 28 + this.index * 32; },
      style: {
        getPropertyValue: (key) => props.get(key),
        setProperty: (k, v) => {
          ops.push('write:var');
          props.set(k, v);
        },
        removeProperty: (k) => {
          props.delete(k);
        }
      }
    };
  };

  let rowNodes = Array.from({ length: 100 }, (_, i) => makeRow(`r-${i}`, i));
  const tbody = {
    parentElement: { parentElement: scrolling ? scroller : null, tHead: { offsetHeight: 28 } },
    getBoundingClientRect: () => ({ top: 28 - scroller.scrollTop }),
    querySelectorAll: () => rowNodes,
    get offsetHeight() {
      metrics.flushes += 1;
      ops.push('flush');
      return 3200;
    }
  };
  const tbodyRef = { current: tbody };
  const render = (enterAnimations) => runner.render(() => useRowShiftFlip(
    tbodyRef, rowNodes.map((n) => n.dataset.rowId).join('|'), enterAnimations,
    enterAnimations.size > 0 && !env.window.matchMedia().matches
  ));
  render(new Map());

  const insert = (id, index) => {
    const newRow = makeRow(id, index, true);
    rowNodes = [...rowNodes.slice(0, index), newRow, ...rowNodes.slice(index, 99)];
    rowNodes.forEach((row, rowIndex) => { row.index = rowIndex; });
    render(new Map([[id, { delayMs: 0 }]]));
    return newRow;
  };

  return {
    env, ops, metrics, scroller,
    get rows() { return rowNodes; },
    viewportTop: (row) => row.offsetTop - scroller.scrollTop,
    insert,
    prepend: (id) => insert(id, 0),
    remove(indices, nativeScrollTop = scroller.scrollTop) {
      rowNodes = rowNodes.filter((_, rowIndex) => !indices.includes(rowIndex));
      rowNodes.forEach((row, rowIndex) => { row.index = rowIndex; });
      scroller.scrollTop = nativeScrollTop;
      render(new Map());
    }
  };
}

test('大页只让视口附近的整行让位，连续插入时批量读取残余位移', async (t) => {
  const table = await createRowShiftFixture(t);
  const { ops, metrics } = table;
  assert.equal(metrics.flushes, 0);

  table.prepend('new-1');
  assert.equal(metrics.styleReads.length, 0, '常规插入不应调用 getComputedStyle');
  assert.equal(metrics.flushes, 1, '整表应仅触发一次同步 flush');
  const shiftedCount = table.rows.filter((row) => row.dataset.rowShift === 'run').length;
  assert.ok(shiftedCount > 0 && shiftedCount <= 24, `应只给视口附近可见行做移位，实际 ${shiftedCount} 行`);
  assert.equal(table.rows[60].dataset.rowShift, undefined, '屏外行不应挂让位动画');

  ops.length = 0;
  metrics.flushes = 0;
  metrics.activeOffsetY = -12;
  table.prepend('new-2');

  assert.ok(metrics.styleReads.length > 0 && metrics.styleReads.length <= 24);
  assert.equal(new Set(metrics.styleReads).size, metrics.styleReads.length, '每条正在移动的行最多读一次');
  assert.equal(table.rows[2].style.getPropertyValue('--row-shift-y'), '-44px', '承接上轮 -12px 残余位移');
  assert.equal(metrics.flushes, 1);
  const lastReadIdx = ops.lastIndexOf('read:style');
  const firstWriteIdx = ops.findIndex((op) => op.startsWith('write:'));
  assert.ok(lastReadIdx < firstWriteIdx, '所有样式读取必须严格先于写操作，严禁循环内交替读写');
});

test('用户滚动后插入顶部，锚点首帧留在原处且不重复让位', async (t) => {
  const table = await createRowShiftFixture(t, { scrolling: true });
  table.scroller.scrollTop = 30 * 32;
  const readingRow = table.rows[30];
  const previousTop = table.viewportTop(readingRow);
  table.prepend('new-1');
  assert.equal(table.scroller.scrollTop, 31 * 32);
  assert.equal(table.viewportTop(readingRow), previousTop);
  assert.equal(table.metrics.flushes, 0);
  assert.ok(table.rows.every((row) => row.dataset.rowShift === undefined));

  // 滚动位置取本轮提交的值，且关闭动效后仍保留阅读锚点。
  table.env.window.matchMedia = () => ({ matches: true });
  table.scroller.scrollTop = 40 * 32;
  const nextReadingRow = table.rows[40];
  const nextPreviousTop = table.viewportTop(nextReadingRow);
  table.prepend('new-2');
  assert.equal(table.scroller.scrollTop, 41 * 32);
  assert.equal(table.viewportTop(nextReadingRow), nextPreviousTop);
  assert.equal(table.metrics.flushes, 0);
});

test('上一批入场未结束时，仍从原视觉位置让位且可独立退出入场', async (t) => {
  const table = await createRowShiftFixture(t);
  const stillEntering = table.prepend('new-1');
  table.env.flushTimers(260);
  table.prepend('new-2');
  assert.match(stillEntering.className, /request-row-enter/);
  assert.equal(stillEntering.dataset.rowShift, 'run');
  assert.equal(stillEntering.style.getPropertyValue('--row-shift-y'), '-32px');
  stillEntering.className = 'group';
  assert.equal(stillEntering.dataset.rowShift, 'run');
  table.env.flushTimers(260);
  assert.equal(stillEntering.dataset.rowShift, undefined);
  assert.equal(stillEntering.style.getPropertyValue('--row-shift-y'), undefined);
});

test('顶部插入新行时既有行斑马纹归属零翻转（含顶部让位与滚动锚定两种场景）', async (t) => {
  const table = await createRowShiftFixture(t, { scrolling: true });
  const initialRow0 = table.rows[0];
  const initialRow1 = table.rows[1];
  const initialRow30 = table.rows[30];
  assert.equal(initialRow0.dataset.zebra, '0');
  assert.equal(initialRow1.dataset.zebra, '1');
  assert.equal(initialRow30.dataset.zebra, '0');

  // 在顶部（scrollTop=0）插入 1 行：既有行斑马纹全部保持原值，新行自动取反色
  const new1 = table.prepend('new-1');
  assert.equal(new1.dataset.zebra, '1');
  assert.equal(initialRow0.dataset.zebra, '0');
  assert.equal(initialRow1.dataset.zebra, '1');
  assert.equal(initialRow30.dataset.zebra, '0');

  // 滚动到下方阅读时再插入 1 行：阅读中的既有行底色与视口坐标均纹丝不动
  table.scroller.scrollTop = 30 * 32;
  const new2 = table.prepend('new-2');
  assert.equal(new2.dataset.zebra, '0');
  assert.equal(new1.dataset.zebra, '1');
  assert.equal(initialRow0.dataset.zebra, '0');
  assert.equal(initialRow30.dataset.zebra, '0');
  for (let i = 1; i < table.rows.length; i += 1) {
    assert.notEqual(table.rows[i].dataset.zebra, table.rows[i - 1].dataset.zebra, `第 ${i} 行应与上一行交替`);
  }
});

test('完成筛选中途补入旧请求时，视口上方增删保留阅读区底色并严格交替', async (t) => {
  const table = await createRowShiftFixture(t, { scrolling: true, height: 28 + 8 * 32 });
  table.scroller.scrollTop = 30 * 32;
  const readingRows = table.rows.slice(30, 38);
  const readingStripe = () => readingRows.map((row) => row.dataset.zebra);
  const readingPositions = () => readingRows.map(table.viewportTop);
  const assertAlternating = () => {
    for (let i = 1; i < table.rows.length; i += 1) {
      assert.notEqual(table.rows[i].dataset.zebra, table.rows[i - 1].dataset.zebra);
    }
  };
  const initialStripe = readingStripe();

  let positions = readingPositions();
  table.insert('completed-late', 2);
  assert.deepEqual(readingPositions(), positions);
  assert.deepEqual(readingStripe(), initialStripe);
  assertAlternating();

  table.remove([2]);
  assert.deepEqual(readingStripe(), initialStripe);
  assertAlternating();

  table.env.window.matchMedia = () => ({ matches: true });
  table.scroller.scrollTop = 30 * 32;
  positions = readingPositions();
  table.insert('completed-late-reduced-motion', 2);
  assert.deepEqual(readingPositions(), positions);
  assert.deepEqual(readingStripe(), initialStripe);
  assert.equal(table.metrics.flushes, 0);
  assertAlternating();
});

test('原生滚动锚定已补偿视口上方批量删除时，保留当前阅读区底色', async (t) => {
  const table = await createRowShiftFixture(t, { scrolling: true, height: 28 + 8 * 32 });
  table.scroller.scrollTop = 35 * 32;
  const readingRows = table.rows.slice(35, 43);
  const positions = readingRows.map(table.viewportTop);
  const stripes = readingRows.map((row) => row.dataset.zebra);

  table.remove([2, 4, 31, 32, 33], 30 * 32);

  assert.deepEqual(readingRows.map(table.viewportTop), positions);
  assert.deepEqual(readingRows.map((row) => row.dataset.zebra), stripes);
  assert.equal(table.ops.includes('scrollTo'), false);
  assert.equal(table.metrics.flushes, 0);
});

test('阅读区只能看到半行时，仍使用该旧行作为滚动和斑马纹锚点', async (t) => {
  const table = await createRowShiftFixture(t, { scrolling: true, height: 28 + 10 });
  table.scroller.scrollTop = 30 * 32 + 16;
  const readingRow = table.rows[30];
  const previousStripe = readingRow.dataset.zebra;
  const previousTop = table.viewportTop(readingRow);

  table.insert('completed-late', 2);
  assert.equal(readingRow.dataset.zebra, previousStripe);
  assert.equal(table.viewportTop(readingRow), previousTop);
});
