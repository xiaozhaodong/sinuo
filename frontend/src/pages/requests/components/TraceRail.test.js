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
  applyLiveEnterRamp,
  easeTraceRailSettle,
  formatTraceElapsedBadge,
  getSettledRailSegments,
  getTraceRailPhase,
  quantizeSegmentsToCells,
  resolveAsymptoticFilledCells,
  resolveEffectiveLiveElapsed,
  resolveLiveRailState,
  resolveTraceRailSettleCells,
  resolveTraceRailSettleDuration,
  TRACE_RAIL_CELLS,
  TRACE_RAIL_ENTER_RAMP_MS,
  TRACE_RAIL_HALF_SATURATION_MS,
  TRACE_RAIL_MAX_LIVE_FILLED,
  TRACE_RAIL_SETTLE_BASE_MS,
  TRACE_RAIL_SETTLE_PER_CELL_MS,
  TRACE_RAIL_TAIL_GRACE_MS
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

test('阶段游标只用于正在执行的请求', () => {
  for (const status of Object.keys(STATUS_CONFIG)) {
    assert.equal(Boolean(getTraceRailPhase({ status })), IN_FLIGHT.includes(status), status);
  }
});

test('准备、转发、首响、生成和收尾按记录推进', () => {
  const timestamp = '2026-11-19T02:00:00Z';
  const sentAt = '2026-11-19T02:00:01Z';
  for (const [request, key, color] of [
    [{ status: 'pending' }, 'preparing', 'queue'],
    [{ status: 'forwarding' }, 'forwarding', 'send'],
    [{ status: 'processing', upstreamFirstDispatchMs: 0 }, 'forwarding', 'send'],
    [{ status: 'processing', firstTokenStartAt: sentAt, duration: 900000 }, 'first', 'first'],
    [{ status: 'processing', upstreamWriteMs: 0 }, 'first', 'first'],
    [{ status: 'processing', firstTokenMs: 0 }, 'stream', 'stream'],
    [{ status: 'processing', completionMs: 0 }, 'settling', 'tail']
  ]) {
    const phase = getTraceRailPhase({ timestamp, ...request });
    assert.equal(phase.key, key);
    assert.equal(phase.color, color);
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
  assert.equal(getTraceRailPhase(request).color, 'retry');
  assert.equal(getTraceRailPhase({ ...request, status: 'forwarding' }).color, 'retry');
  assert.equal(getTraceRailPhase({ ...request, firstTokenStartAt: '2026-11-19T02:00:03Z' }).color, 'first');
  assert.equal(getTraceRailPhase({ ...request, status: 'suspended' }), null);
});

test('已记录的首响和收尾优先于未更新的重试等待信息', () => {
  for (const status of ['processing', 'retry']) {
    for (const firstTokenStartAt of [undefined, '2026-11-19T02:00:01Z']) {
      const request = { status, firstTokenStartAt, retryStartedAt: '2026-11-19T02:00:02Z' };
      assert.equal(getTraceRailPhase(request).color, 'retry');
      assert.equal(getTraceRailPhase({ ...request, firstTokenMs: 0 }).color, 'stream');
      assert.equal(getTraceRailPhase({ ...request, firstTokenMs: 0, completionMs: 200 }).color, 'tail');
    }
  }
});

test('渐近生长时间尺随耗时单调增长且永不顶爆，始终留 1 格游标', () => {
  assert.equal(TRACE_RAIL_CELLS, 21);
  assert.equal(TRACE_RAIL_MAX_LIVE_FILLED, 20);
  assert.equal(resolveAsymptoticFilledCells(0), 0);
  assert.equal(resolveAsymptoticFilledCells(-100), 0);
  assert.equal(resolveAsymptoticFilledCells(TRACE_RAIL_HALF_SATURATION_MS), 10);

  let prev = 0;
  for (const ms of [400, 1500, 4000, 8000, 15000, 30000, 120000, 600000, 3600000]) {
    const filled = resolveAsymptoticFilledCells(ms);
    assert.ok(filled >= prev, `${ms}ms 的格数 ${filled} 不应小于前档 ${prev}`);
    assert.ok(filled <= TRACE_RAIL_MAX_LIVE_FILLED, `${ms}ms 超过进行中上限`);
    prev = filled;
  }
  assert.equal(resolveAsymptoticFilledCells(3600000), TRACE_RAIL_MAX_LIVE_FILLED);
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

test('进行中状态快照支持弹性阶段占比与慢首响预警，进入生成后恢复首响紫', () => {
  const startMs = Date.parse('2026-11-19T02:00:00Z');
  const base = {
    status: 'processing',
    timestamp: '2026-11-19T02:00:00Z',
    upstreamWriteMs: 300,
    firstTokenStartAt: '2026-11-19T02:00:00.300Z'
  };

  // 常规等首响（3s）
  const normalWait = resolveLiveRailState(base, 'processing', 3000, startMs + 3000);
  assert.equal(normalWait.cells.length, TRACE_RAIL_CELLS);
  assert.equal(normalWait.cursorIndex, normalWait.filled);
  assert.equal(normalWait.cursorColorKey, 'first');
  assert.equal(normalWait.cells[normalWait.cursorIndex], 'first');

  // 慢首响 >8s -> 琥珀预警（retry）
  const warnWait = resolveLiveRailState(base, 'processing', 9500, startMs + 9500);
  assert.equal(warnWait.cursorColorKey, 'retry');
  assert.ok(warnWait.filled > normalWait.filled, '等待越久已生长格数应越多');

  // 极慢首响 >15s -> 玫瑰红预警（failure）
  const critWait = resolveLiveRailState(base, 'processing', 18000, startMs + 18000);
  assert.equal(critWait.cursorColorKey, 'failure');

  // 收到首响进入生成 -> 首响定格紫罗兰，游标与新增部分为翠绿
  const streaming = resolveLiveRailState(
    { ...base, firstTokenMs: 14000, firstTokenAt: '2026-11-19T02:00:14.300Z' },
    'processing',
    22000,
    startMs + 22000
  );
  assert.equal(streaming.cursorColorKey, 'stream');
  assert.ok(streaming.cells.slice(0, streaming.filled).includes('first'));
  assert.ok(streaming.cells.slice(0, streaming.filled).includes('stream'));
});

test('最新一次写完距今很短时，滞后的 firstTokenStartAt 不会误报慢首响', () => {
  const startMs = Date.parse('2026-11-19T02:00:00Z');
  const state = resolveLiveRailState({
    status: 'processing',
    timestamp: '2026-11-19T02:00:00Z',
    // 旧的写入时间戳停在 100ms 处，但最新一次写完已是 16000ms，距今才 100ms
    firstTokenStartAt: '2026-11-19T02:00:00.100Z',
    upstreamWriteMs: 16000
  }, 'processing', 16100, startMs + 16100);
  assert.equal(state.cursorColorKey, 'first');
  assert.equal(state.cells[state.cursorIndex], 'first');
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
  const state = resolveLiveRailState(merged, merged.status, 30000, startMs + 30000);
  assert.equal(state.phase.key, 'first');
  assert.equal(state.cursorColorKey, 'first');
  const byKey = Object.fromEntries(state.segments.map((seg) => [seg.key, seg.ms]));
  assert.equal(byKey.retry, 29900);
  assert.equal(byKey.first, 100);
});

test('重试后首响事件先到且写入时间未刷新时，优先按 firstTokenAt 分配生成段与重试段', () => {
  const startMs = Date.parse('2026-11-19T02:00:00Z');
  // 第 1 次写出停在 200ms，18s 后重试并于 20s 收到首响（首响 1.2s），当前 25s（实际生成 5s）
  const state = resolveLiveRailState({
    status: 'processing',
    timestamp: '2026-11-19T02:00:00Z',
    upstreamWriteMs: 200,
    firstTokenStartAt: '2026-11-19T02:00:00.200Z',
    retryStartedAt: '2026-11-19T02:00:18.000Z',
    firstTokenMs: 1200,
    firstTokenAt: '2026-11-19T02:00:20.000Z'
  }, 'processing', 25000, startMs + 25000);

  const byKey = Object.fromEntries(state.segments.map((seg) => [seg.key, seg.ms]));
  assert.equal(byKey.retry, 18800);
  assert.equal(byKey.first, 1200);
  assert.equal(byKey.stream, 5000);
});

test('状态列总耗时进行中降频，终态保持与 timing 同口径', () => {
  assert.equal(formatTraceElapsedBadge(37, { live: true }), '0ms');
  assert.equal(formatTraceElapsedBadge(382, { live: true }), '350ms');
  assert.equal(formatTraceElapsedBadge(1432, { live: true }), '1.4s');
  assert.equal(formatTraceElapsedBadge(12340, { live: true }), '12.3s');
  assert.equal(formatTraceElapsedBadge(1432, { live: false }), '1.43s');
  assert.equal(formatTraceElapsedBadge(0, { live: false }), '0ms');
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
  assert.match(RAIL_CODE, /resolveLiveRailState\(/);
  assert.match(RAIL_CODE, /resolveElapsedMs\(currentStart, 0, now\)/);
  assert.match(RAIL_CODE, /subscribeElapsedTick\(paint\)/);
  assert.match(RAIL_CODE, /if \(!isLive\) \{[\s\S]*?return undefined;/);
  assert.doesNotMatch(RAIL_CODE, /useState|setInterval/);
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

test('21 格独立网格无亚像素，生长前沿游标独立呼吸', () => {
  const rail = CSS_SOURCE.match(/\.trace-rail \{[^}]*\}/)?.[0];
  assert.match(rail, /grid-template-columns: repeat\(21, 4px\)/);
  assert.match(rail, /gap: 2px/);
  assert.match(rail, /width: 124px/);
  assert.match(rail, /height: 4px/);
  const cursor = CSS_SOURCE.match(/\.trace-rail__cell--cursor \{[^}]*\}/)?.[0];
  assert.match(cursor, /animation: trace-rail-cursor [^;]*infinite/);
  // 空槽背景必须使用独立类，不能写在基础 .trace-rail__cell 上覆盖 Tailwind bg-*
  const baseCell = CSS_SOURCE.match(/\.trace-rail__cell \{[^}]*\}/)?.[0];
  assert.doesNotMatch(baseCell, /background/);
  assert.doesNotMatch(RAIL_CODE, /key=\{request\.timestamp\}/);
});

test('减弱动效保留静态生长前沿，关闭呼吸与过渡动画', () => {
  const reducedBlock = CSS_SOURCE.match(/@media \(prefers-reduced-motion: reduce\)[\s\S]*?\n}\n/)?.[0];
  assert.ok(reducedBlock, 'reduced-motion 段落未找到');
  assert.match(reducedBlock, /\.trace-rail__cell--cursor,[^}]*animation: none/);
  assert.match(reducedBlock, /\.trace-rail--live \.trace-rail__cell \{[^}]*transition: none/);
  assert.match(reducedBlock, /\.trace-rail__cell--cursor \{ opacity: \.68; \}/);
  assert.match(RAIL_CODE, /!prefersReducedMotion\(\)/);
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

test('收尾延展缓动柔和起步并单调收敛到 1，自适应时长按剩余空槽计算', () => {
  assert.equal(easeTraceRailSettle(0), 0);
  assert.equal(easeTraceRailSettle(1), 1);
  // 首帧（~5% 进度）柔和起速，避免像 easeOutExpo 一样单帧猛跳多格
  assert.ok(easeTraceRailSettle(0.05) < 0.05, '起速过猛会在首帧产生跳跃感');
  assert.ok(easeTraceRailSettle(0.5) > 0.7, '中后段应平稳减速贴近终态');

  let prev = 0;
  for (let i = 1; i <= 20; i += 1) {
    const val = easeTraceRailSettle(i / 20);
    assert.ok(val >= prev && val <= 1);
    prev = val;
  }

  assert.equal(resolveTraceRailSettleDuration(TRACE_RAIL_CELLS), 0);
  assert.equal(
    resolveTraceRailSettleDuration(0),
    TRACE_RAIL_SETTLE_BASE_MS + TRACE_RAIL_CELLS * TRACE_RAIL_SETTLE_PER_CELL_MS
  );
  assert.ok(resolveTraceRailSettleDuration(4) > resolveTraceRailSettleDuration(16));
});

test('收尾中间帧只填充前 visibleCount 格，右侧未扫过格保持空槽并顺滑推移分段边界', () => {
  const segments = [
    { key: 'first', ms: 1400 },
    { key: 'stream', ms: 2600 }
  ];
  const settled = quantizeSegmentsToCells(segments, TRACE_RAIL_CELLS);

  const empty = resolveTraceRailSettleCells(segments, settled, 0);
  assert.equal(empty.length, TRACE_RAIL_CELLS);
  assert.ok(empty.every((cell) => cell === null));

  const mid = resolveTraceRailSettleCells(segments, settled, 9);
  assert.equal(mid.length, TRACE_RAIL_CELLS);
  assert.ok(mid.slice(0, 9).every((cell) => cell !== null));
  assert.ok(mid.slice(9).every((cell) => cell === null));
  assert.ok(mid.slice(0, 9).includes('first') && mid.slice(0, 9).includes('stream'));

  const full = resolveTraceRailSettleCells(segments, settled, TRACE_RAIL_CELLS);
  assert.deepEqual(full, settled);
});

test('短防抖尾窗内锁定流式结束耗时并保留流式绿游标，真实慢尾段再切灰恢复计秒', () => {
  const startMs = Date.parse('2026-11-19T02:00:00.000Z');
  const req = {
    status: 'processing',
    isStreaming: true,
    timestamp: '2026-11-19T02:00:00.000Z',
    firstTokenAt: '2026-11-19T02:00:02.000Z',
    firstTokenMs: 1800,
    completionMs: 3000
  };
  const phase = getTraceRailPhase(req, 'processing');
  assert.equal(phase.key, 'settling');

  // 250ms 防抖窗口内（超过 streamEndOffset=5000ms 仅 220ms <= 350ms）
  const inGrace = resolveEffectiveLiveElapsed(req, phase, 5220, startMs);
  assert.equal(inGrace.elapsedMs, 5000);
  assert.equal(inGrace.keepStreamCursor, true);

  // 超过宽限窗口（滞后 500ms > 350ms）：确属真实慢收尾，恢复计秒并允许游标切灰
  const slowTail = resolveEffectiveLiveElapsed(req, phase, 5000 + TRACE_RAIL_TAIL_GRACE_MS + 150, startMs);
  assert.equal(slowTail.elapsedMs, 5000 + TRACE_RAIL_TAIL_GRACE_MS + 150);
  assert.equal(slowTail.keepStreamCursor, false);
});

test('真实首响的 0ms 生成耗时，仅流式请求可在短尾窗保留绿游标', () => {
  const timestamp = '2026-11-19T02:00:00.000Z';
  const startMs = Date.parse(timestamp);
  const request = {
    status: 'processing', timestamp, firstTokenMs: 1800,
    firstTokenAt: '2026-11-19T02:00:02.000Z', completionMs: 0
  };
  for (const isStreaming of [false, true]) {
    const req = { ...request, isStreaming };
    const phase = getTraceRailPhase(req);
    assert.equal(phase.key, 'settling');
    assert.deepEqual(resolveEffectiveLiveElapsed(req, phase, 2220, startMs), {
      elapsedMs: isStreaming ? 2000 : 2220,
      keepStreamCursor: isStreaming
    });
  }
});

test('新入场在途行在前 240ms 内从 0 格顺滑追上真实格数，避免挂载首帧跳格', () => {
  const startMs = Date.parse('2026-11-19T02:00:00.000Z');
  const liveState = resolveLiveRailState({
    status: 'processing',
    timestamp: '2026-11-19T02:00:00.000Z',
    upstreamWriteMs: 180,
    firstTokenStartAt: '2026-11-19T02:00:00.180Z'
  }, 'processing', 2400, startMs + 2400);
  assert.ok(liveState.filled >= 4);

  const atZero = applyLiveEnterRamp(liveState, 0, TRACE_RAIL_ENTER_RAMP_MS);
  assert.equal(atZero.filled, 0);
  assert.equal(atZero.cursorIndex, 0);

  const atMid = applyLiveEnterRamp(liveState, TRACE_RAIL_ENTER_RAMP_MS * 0.4, TRACE_RAIL_ENTER_RAMP_MS);
  assert.ok(atMid.filled > 0 && atMid.filled <= liveState.filled);

  const atEnd = applyLiveEnterRamp(liveState, TRACE_RAIL_ENTER_RAMP_MS, TRACE_RAIL_ENTER_RAMP_MS);
  assert.equal(atEnd, liveState);
});

test('轨道与耗时节点恒定不卸载重建，收尾走平滑色块过渡而非闪暗关键帧', () => {
  assert.doesNotMatch(RAIL_CODE, /key=\{isLive \? 'live' : 'static'\}/, '切换终态不能卸载重建轨道 DOM');
  assert.doesNotMatch(RAIL_CODE, /key=\{canTick \? 'live' : 'static'\}/, '切换终态不能卸载重建耗时节点');
  assert.doesNotMatch(CSS_SOURCE, /@keyframes trace-rail-settle\b/, '旧版闪暗+缩放关键帧应彻底移除');
  assert.match(CSS_SOURCE, /\.trace-rail\[data-settling\] \.trace-rail__cell \{[^}]*transition: background-color 220ms/);
  assert.match(CSS_SOURCE, /\.trace-rail__label \{[^}]*transition: color/);
  assert.match(TABLE_CODE, /<RequestTraceCell request=\{request\} enterAnimation=\{enterAnimation\} \/>/);
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

test('计时数据晚到（收尾刚铺满进入过渡窗或中途补入）时，轨道立即更新为最新分段配色不残留灰格', async (t) => {
  const runner = createHookRunner();
  globalThis.__traceHookHost = runner.hooks;
  t.after(() => {
    runner.unmount();
    delete globalThis.__traceHookHost;
  });
  const env = createFakeBrowserEnv(t);
  const { useTraceRail } = await loadTraceRailHook();

  const rail = createFakeRailNode();
  const timeNode = { textContent: '' };
  const bindRefs = (result) => {
    result.railRef.current = rail;
    result.timeRef.current = timeNode;
  };

  const renderReq = (req) => {
    const status = req.status;
    const isLive = Boolean(getTraceRailPhase(req, status));
    const finalMs = !UNSETTLED_STATUSES.has(status) && Number.isFinite(req.duration) ? req.duration : null;
    const segments = isLive ? [] : getSettledRailSegments(req, finalMs);
    const settledCellKeys = segments.length > 0 ? quantizeSegmentsToCells(segments, TRACE_RAIL_CELLS) : Array.from({ length: TRACE_RAIL_CELLS }, () => null);
    runner.render(() => {
      const refs = useTraceRail({
        request: req,
        status,
        startedAt: isLive ? Date.parse(req.timestamp) : undefined,
        isLive,
        hasSegments: segments.length > 0,
        segments,
        settledCellKeys,
        enterAnimation: null
      });
      bindRefs(refs);
    });
  };

  // 1. 先在途运行，再以仅含总耗时（全灰）的状态落定
  renderReq({ requestId: 'req-late', status: 'processing', timestamp: '2026-11-19T02:00:00.000Z' });
  renderReq({ requestId: 'req-late', status: 'completed', timestamp: '2026-11-19T02:00:00.000Z', duration: 1000 });
  assert.equal(rail.dataset.settling, '');

  // 2. 推完收尾 rAF（进入 260ms 尾段混合窗，teardownTimer 尚未触发）
  env.advanceRaf(1000);
  assert.equal(rail.dataset.settling, '', '应仍处于尾段色块混合窗');
  assert.equal(rail.children.filter((n) => n.className.includes('bg-slate-300')).length, 21);

  // 3. 此时补入首响 200ms + 流式输出 800ms（应为 5 格紫 + 16 格绿）
  renderReq({
    requestId: 'req-late',
    status: 'completed',
    timestamp: '2026-11-19T02:00:00.000Z',
    duration: 1000,
    firstTokenMs: 200,
    completionMs: 800,
    isStreaming: true
  });
  assert.equal(rail.children.filter((n) => n.className.includes('bg-violet-400')).length, 5);
  assert.equal(rail.children.filter((n) => n.className.includes('bg-emerald-400')).length, 16);

  // 4. 过渡窗结束后，21 格依然保持 5 紫 + 16 绿，绝不回退全灰
  env.flushTimers(400);
  assert.equal(rail.dataset.settling, undefined);
  assert.equal(rail.children.filter((n) => n.className.includes('bg-violet-400')).length, 5);
  assert.equal(rail.children.filter((n) => n.className.includes('bg-emerald-400')).length, 16);
});

test('React StrictMode 双跑 effect 时，极速新请求不会跳过从 0 到 21 格的收尾动画', async (t) => {
  const runner = createHookRunner();
  globalThis.__traceHookHost = runner.hooks;
  t.after(() => {
    runner.unmount();
    delete globalThis.__traceHookHost;
  });
  const env = createFakeBrowserEnv(t);
  const { useTraceRail } = await loadTraceRailHook();

  const rail = createFakeRailNode();
  const fastReq = {
    requestId: 'req-fast-strict',
    status: 'completed',
    timestamp: '2026-11-19T02:00:00.000Z',
    duration: 180,
    firstTokenMs: 60,
    completionMs: 120,
    isStreaming: true
  };
  const segments = getSettledRailSegments(fastReq, 180);
  const settledCellKeys = quantizeSegmentsToCells(segments, TRACE_RAIL_CELLS);

  runner.render(() => {
    const refs = useTraceRail({
      request: fastReq,
      status: 'completed',
      startedAt: undefined,
      isLive: false,
      hasSegments: true,
      segments,
      settledCellKeys,
      enterAnimation: { delayMs: 0 }
    });
    refs.railRef.current = rail;
  });

  // 模拟 StrictMode：在浏览器触发 rAF 之前同步执行 cleanup + 重新执行 effect
  runner.simulateStrictRemount();

  assert.equal(rail.dataset.settling, '', 'StrictMode 重挂后仍应保持收尾动画激活');
  assert.equal(
    rail.children.filter((n) => n.className.includes('trace-rail__cell--empty')).length,
    21,
    'StrictMode 重挂首帧应从全空槽起步，而不是直接跳成满格'
  );

  // 推进 rAF 到中段与终点：应先部分填充，再满格落位
  env.advanceRaf(220);
  const midFilled = rail.children.filter((n) => !n.className.includes('trace-rail__cell--empty')).length;
  assert.ok(midFilled > 0 && midFilled < 21, `中段应处于平滑推进中，实际 ${midFilled} 格`);

  env.advanceRaf(600);
  assert.equal(rail.children.filter((n) => !n.className.includes('trace-rail__cell--empty')).length, 21);
  env.flushTimers(300);
  assert.equal(rail.dataset.settling, undefined);

  // 播完后即便再次模拟重跑 effect，也不应重播收尾动画
  runner.simulateStrictRemount();
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
