import test from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { dirname, join } from 'node:path';

import { buildLifecycleSegments, railSegmentColors, lifecycleSegmentColors } from '../utils/lifecycle.js';
import { carryLiveTiming } from '../utils/liveTiming.js';
import { TABLE_COLUMNS, DEFAULT_VISIBLE_COLUMNS, UNSETTLED_STATUSES } from '../utils/constants.js';
import {
  formatTraceElapsedBadge,
  getSettledRailSegments,
  getTraceRailPhase,
  quantizeSegmentsToCells,
  resolveAsymptoticFilledCells,
  resolveLiveRailState,
  TRACE_RAIL_CELLS,
  TRACE_RAIL_HALF_SATURATION_MS,
  TRACE_RAIL_MAX_LIVE_FILLED
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

test('renderCell 每个分支都返回单一根元素', () => {
  // 入场位移挂在 `> td > *` 上（<tr> 横移会被 overflow-x 裁掉首列，
  // <td> 是 table-cell 吃不下 transform）。返回 Fragment 会让该列静默失去动画。
  const body = TABLE_SOURCE.match(/const renderCell = [\s\S]*?\n};/)?.[0];
  assert.ok(body, 'renderCell 未找到，测试与实现已脱节');

  assert.doesNotMatch(body, /return\s*\(\s*<>/, 'renderCell 不能返回 Fragment');
  assert.doesNotMatch(body, /return\s*<>/, 'renderCell 不能返回 Fragment');
  assert.doesNotMatch(body, /return\s*\[/, 'renderCell 不能返回数组');
});

test('单元格根元素不能是裸行内盒 —— transform 对它无效', () => {
  // 入场位移动的是 transform，而 transform 对非替换的行内元素完全无效。
  // 写成裸 <span> 那一列就只会淡入、不会跟着上浮，且失效是静默的。
  const body = TABLE_CODE.match(/const renderCell = [\s\S]*?\n};/)?.[0];
  assert.ok(body, 'renderCell 未找到，测试与实现已脱节');
  const valueCell = TABLE_CODE.match(/const ValueCell = [\s\S]*?\n\);/)?.[0];
  assert.ok(valueCell, 'ValueCell 未找到，测试与实现已脱节');

  // 只认根元素（return 后紧跟的那个），嵌套的 span 不受这条约束
  const roots = [
    ...body.matchAll(/return <span className=(?:\{`|")([^`"]*)/g),
    ...valueCell.matchAll(/<span className=(?:\{`|")([^`"]*)/g)
  ].map(match => match[1]);
  assert.ok(roots.length >= 4, `只扫到 ${roots.length} 个根 span，正则与实现已脱节`);

  for (const cls of roots) {
    assert.match(cls, /\b(?:inline-block|inline-flex|block|flex)\b/,
      `「${cls}」是裸行内盒，入场只会淡入不会上浮`);
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
  assert.match(TABLE_CODE, /const costPending = pending \|\| request\.costKnown === false/,
    'cost_known=false 的记录必须显示占位符');

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

test('入场 class 的摘除认 animationName，且与 CSS 里的名字对得上', () => {
  const finalName = TABLE_SOURCE.match(/const FINAL_ENTER_ANIMATION = '([^']+)'/)?.[1];
  assert.ok(finalName, 'FINAL_ENTER_ANIMATION 未找到');

  // 必须是 CSS 里真实存在的 @keyframes，否则 animationend 永不匹配，
  // class 只能靠 setTimeout 兜底摘掉。
  assert.match(CSS_SOURCE, new RegExp(`@keyframes ${finalName}\\b`),
    `${finalName} 在 index.css 里没有对应的 @keyframes`);

  // 且必须是收尾最晚的那条：认早结束的动画会把亮线截断在半路。
  const riseMs = Number(CSS_SOURCE.match(/animation: request-row-rise (\d+)ms/)?.[1]);
  const tickMs = Number(CSS_SOURCE.match(/animation: request-row-edge-tick (\d+)ms/)?.[1]);
  assert.ok(Number.isFinite(riseMs) && Number.isFinite(tickMs), '两条入场动画的时长未能解析');
  assert.ok(tickMs > riseMs,
    `摘 class 认的是 ${finalName}，但它 ${tickMs}ms 并不比单元格位移 ${riseMs}ms 晚结束`);
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
