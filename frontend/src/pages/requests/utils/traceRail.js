import { IN_FLIGHT_STATUSES } from './constants.js';
import { buildRailLifecycleSegments, parseTimestampOrNull } from './lifecycle.js';
import { TIMING_THRESHOLDS_MS } from './timing.js';

// 21 个 4px 色格 + 20 道 2px 间隙 = 精确 124px（零亚像素，首尾齐平）。
export const TRACE_RAIL_CELLS = 21;

// 进行中的阶段步进轨：21 格固定分三段，走过的段整段亮，没到的段是空槽，
// 当前段压暗、只有一道亮光从左往右扫过。只表达「走到了哪一段」，不按已耗时生长：
// 不知道真实进度时不画进度。思考不单独占段：进行中拿不准会不会有；结束后列表轨道也不拆思考
// （buildRailLifecycleSegments 关闭了 streamPhases），只按首响 / 生成等分段着色，思考段只在详情的生命周期条里显示。
export const TRACE_RAIL_STEPS = Object.freeze([
  { key: 'prep', label: '准备', cells: 5, color: 'send' },
  { key: 'first', label: '首响', cells: 8, color: 'first' },
  { key: 'gen', label: '生成', cells: 8, color: 'stream' }
]);
// 进行中阶段文字平时用中性灰，只在异常时上色：重试，以及首响超过 8 秒变黄、超过 15 秒变红（与轨道当前段同一判定）。
// 终态才上语义色，「生成中 → 完成」由灰变绿本身就是结束信号。黄 / 红与停止原因告警的文字色一致。
export const TRACE_RAIL_LABEL_NEUTRAL = 'text-fg-muted';
export const TRACE_RAIL_LABEL_TONES = Object.freeze({
  retry: 'text-amber-700 dark:text-amber-300',
  failure: 'text-rose-700 dark:text-rose-300'
});
export const traceRailLabelTone = (colorKey) => TRACE_RAIL_LABEL_TONES[colorKey] || TRACE_RAIL_LABEL_NEUTRAL;

// 亮光每 120ms 前进一格、宽 2 格，扫出段尾后停 5 拍再从段首开始。
export const TRACE_RAIL_SWEEP_STEP_MS = 120;
export const TRACE_RAIL_SWEEP_CELLS = 2;
export const TRACE_RAIL_SWEEP_REST_STEPS = 5;
// 结束换色：CSS 按格序号错开 14ms（index.css 的 transition-delay），总时长 = 错峰 + 单格过渡 + 余量。
export const TRACE_RAIL_SETTLE_STAGGER_MS = 14;
export const TRACE_RAIL_SETTLE_MS = TRACE_RAIL_CELLS * TRACE_RAIL_SETTLE_STAGGER_MS + 260;

const PHASE = {
  preparing: { key: 'preparing', label: '准备发送', step: 0 },
  forwarding: { key: 'forwarding', label: '发往上游', step: 0 },
  retry: { key: 'retry', label: '重试中', step: 1 },
  first: { key: 'first', label: '等首响', step: 1 },
  stream: { key: 'stream', label: '生成中', step: 2 },
  settling: { key: 'settling', label: '收尾', step: 2 }
};

const hasTiming = (ms) => Number.isFinite(ms) && ms >= 0;

export const isOutcomeSegmentKey = (key) => key === 'failure' || key === 'cancelled';

/**
 * 统一解析最新一次写完上游的偏移与绝对时刻：
 * 列表查询与实时计时事件可能错峰合并（例如查询已带回最新 upstreamWriteMs，
 * 但 carryLiveTiming 仍保留了上一次尝试的 firstTokenStartAt），
 * 阶段判断与慢首响预警必须共用同一份最新写完口径。
 */
function resolveLatestWriteInfo(request) {
  const startedAt = parseTimestampOrNull(request.timestamp)?.getTime();
  const sentAt = parseTimestampOrNull(request.firstTokenStartAt)?.getTime();
  const retryAt = parseTimestampOrNull(request.retryStartedAt)?.getTime();
  const sentOffset = Number.isFinite(startedAt) && Number.isFinite(sentAt)
    ? Math.max(0, sentAt - startedAt)
    : null;
  const writeOffset = hasTiming(request.upstreamWriteMs) && sentOffset !== null
    ? Math.max(request.upstreamWriteMs, sentOffset)
    : (hasTiming(request.upstreamWriteMs) ? request.upstreamWriteMs : sentOffset);
  const writeAt = Number.isFinite(startedAt) && writeOffset !== null
    ? startedAt + writeOffset
    : (Number.isFinite(sentAt) ? sentAt : null);
  return { startedAt, sentAt, retryAt, writeOffset, writeAt };
}

export function getTraceRailPhase(request, status = request.status) {
  if (!IN_FLIGHT_STATUSES.has(status)) return null;
  // 首响和生成耗时一旦落定，就优先于可能还未更新的重试状态与写入起点。
  if (hasTiming(request.completionMs)) return PHASE.settling;
  if (hasTiming(request.firstTokenMs)) return PHASE.stream;

  const { retryAt, writeOffset, writeAt } = resolveLatestWriteInfo(request);
  const wroteAfterRetry = Number.isFinite(retryAt) && writeAt !== null && writeAt > retryAt;
  const retryWaiting = Number.isFinite(retryAt) && (writeAt === null || retryAt > writeAt);
  if ((status === 'retry' && !wroteAfterRetry) || retryWaiting) {
    return PHASE.retry;
  }
  if (writeAt !== null || writeOffset !== null) return PHASE.first;
  if (status === 'forwarding' || hasTiming(request.upstreamFirstDispatchMs)) return PHASE.forwarding;
  return PHASE.preparing;
}

const firstWaitStartAtOf = ({ startedAt, sentAt, writeOffset }) => (
  Number.isFinite(startedAt)
    ? startedAt + (writeOffset ?? 0)
    : (Number.isFinite(sentAt) ? sentAt : null)
);

/**
 * 等首响已经多久：从最新一次写完上游算起。有请求起点时用最新写完偏移，
 * 不拿可能滞后的 firstTokenStartAt 误报慢首响；没有起点才退回 firstTokenStartAt。
 */
export function resolveFirstWaitMs(request, now) {
  if (!Number.isFinite(now)) return 0;
  const startAt = firstWaitStartAtOf(resolveLatestWriteInfo(request));
  return startAt !== null ? Math.max(0, now - startAt) : 0;
}

// 与首响计时胶囊同一把尺子：超过 8 秒变黄、超过 15 秒变红
const firstWaitColorKey = (waitMs) => {
  if (waitMs > TIMING_THRESHOLDS_MS.first.critical) return 'failure';
  if (waitMs > TIMING_THRESHOLDS_MS.first.warning) return 'retry';
  return 'first';
};

// 各行亮光错开起点，避免整列同步扫动显得机械
export const traceRailSweepSeed = (requestId) => {
  let hash = 0;
  for (const ch of String(requestId ?? '')) hash = (hash * 31 + ch.charCodeAt(0)) % 100_000;
  return hash;
};

/**
 * 轨道当前段的颜色键，进行中阶段文字也用它：重试黄；等首响按等待时长紫 → 黄 → 红；其余取所在段颜色。
 * 不传 now 时按刚开始等待处理，供渲染期取初值（渲染里不读时钟）。
 */
export function resolveStepRailColor(request, phase, now) {
  if (phase.key === 'retry') return 'retry';
  if (phase.key === 'first') return firstWaitColorKey(resolveFirstWaitMs(request, now));
  return TRACE_RAIL_STEPS[phase.step].color;
}

/**
 * 预解析进行中请求的静态元数据（阶段、分段长度、扫光周期与相位偏移、首响等待起点），
 * 供高频时钟帧做轻量签名比对，避免在 60/120Hz 回调里重复解析时间戳与重建 21 格数组。
 */
export function resolveStepRailMeta(request, status = request.status, seed = traceRailSweepSeed(request.requestId)) {
  const phase = getTraceRailPhase(request, status);
  if (!phase) return null;
  const length = TRACE_RAIL_STEPS[phase.step].cells;
  return {
    phase,
    length,
    cycle: length + TRACE_RAIL_SWEEP_CELLS + TRACE_RAIL_SWEEP_REST_STEPS,
    seed,
    firstWaitStartAt: phase.key === 'first' ? firstWaitStartAtOf(resolveLatestWriteInfo(request)) : null
  };
}

const resolveMetaColor = (meta, now) => {
  if (meta.phase.key === 'retry') return 'retry';
  if (meta.phase.key === 'first') {
    const waitMs = Number.isFinite(now) && meta.firstWaitStartAt !== null
      ? Math.max(0, now - meta.firstWaitStartAt)
      : 0;
    return firstWaitColorKey(waitMs);
  }
  return TRACE_RAIL_STEPS[meta.phase.step].color;
};

// 扫光休止期（当前段全暗）的多个 head 视觉完全一致，折叠为同一值以跳过休止期重绘。
const resolveMetaSweepHead = (meta, now, sweep) => {
  if (!sweep) return -1;
  const raw = Math.floor((now + meta.seed) / TRACE_RAIL_SWEEP_STEP_MS) % meta.cycle;
  const restStart = meta.length + TRACE_RAIL_SWEEP_CELLS - 1;
  return raw >= restStart ? restStart : raw;
};

/**
 * 当前时钟帧的视觉签名：阶段、段色与有效扫光位均未变化时，调用方可跳过格子计算与 DOM 遍历。
 * 键中保留 phase.key（而非仅 step），避免 retry → 慢首响 切换时 React 重写文字 class 后误命中缓存。
 */
export function resolveStepRailFrameKey(meta, now, { sweep = true } = {}) {
  if (!meta) return null;
  return `${meta.phase.key}:${resolveMetaColor(meta, now)}:${resolveMetaSweepHead(meta, now, sweep)}`;
}

/**
 * 进行中请求此刻的 21 格：cells[i] = { key, dim }，key 为 null 是空槽。
 * sweep=false（减弱动效）时当前段整段静止点亮；seed 让各行亮光错开起点。
 * 终态与挂起返回 null。
 */
export function resolveStepRailCells(request, status, now, { sweep = true, seed, meta } = {}) {
  const resolved = meta ?? resolveStepRailMeta(request, status, seed ?? 0);
  if (!resolved) return null;
  const effectiveMeta = seed !== undefined && seed !== resolved.seed ? { ...resolved, seed } : resolved;
  const color = resolveMetaColor(effectiveMeta, now);
  const head = resolveMetaSweepHead(effectiveMeta, now, sweep);
  const cells = [];
  TRACE_RAIL_STEPS.forEach((step, index) => {
    for (let offset = 0; offset < step.cells; offset += 1) {
      if (index < effectiveMeta.phase.step) {
        cells.push({ key: step.color, dim: false });
      } else if (index > effectiveMeta.phase.step) {
        cells.push({ key: null, dim: false });
      } else {
        const lit = !sweep || (offset <= head && offset > head - TRACE_RAIL_SWEEP_CELLS);
        cells.push({ key: color, dim: !lit });
      }
    }
  });
  return { phase: effectiveMeta.phase, color, cells };
}

/**
 * 获取终态分段列表：保留 0ms 失败/取消/总耗时段，避免 0ms 终态退化成空灰轨。
 */
export function getSettledRailSegments(request, finalMs) {
  if (finalMs === null) return [];
  const segments = buildRailLifecycleSegments(request).filter(({ key, ms }) => (
    (finalMs > 0 && ms > 0) || key === 'total' || isOutcomeSegmentKey(key)
  ));
  // 计时都为 0 的成功记录也显示总耗时段。
  return segments.length > 0 ? segments : buildRailLifecycleSegments({ duration: finalMs });
}

/**
 * 最大余数法整格量化：把连续毫秒分段严格分配到指定数量的整数格中，
 * 彻底消除「半格劈色」与亚像素失真，并给失败/取消终态段保底可见格数。
 */
export function quantizeSegmentsToCells(segments, budget = TRACE_RAIL_CELLS, { minFailureCells = 2 } = {}) {
  if (budget <= 0 || !Array.isArray(segments) || segments.length === 0) return [];

  const active = segments.filter(({ key, ms }) => (
    (Number.isFinite(ms) && ms > 0) || key === 'total' || isOutcomeSegmentKey(key)
  ));
  if (active.length === 0) {
    const fallbackKey = segments[segments.length - 1]?.key || 'total';
    return Array.from({ length: budget }, () => fallbackKey);
  }

  const totalMs = active.reduce((sum, seg) => sum + (Number.isFinite(seg.ms) && seg.ms > 0 ? seg.ms : 0), 0);
  if (totalMs <= 0) {
    const terminal = active.find((seg) => isOutcomeSegmentKey(seg.key)) || active[active.length - 1];
    return Array.from({ length: budget }, () => terminal.key || 'total');
  }

  let mins = active.map(({ key }) => {
    if (key === 'failure' && budget >= 4) return Math.min(minFailureCells, budget);
    if (isOutcomeSegmentKey(key)) return 1;
    return budget >= active.length ? 1 : 0;
  });

  if (mins.reduce((a, b) => a + b, 0) > budget) {
    mins = active.map(({ key }) => (isOutcomeSegmentKey(key) ? 1 : 0));
    if (mins.reduce((a, b) => a + b, 0) > budget) {
      mins = active.map(() => 0);
    }
  }

  const minSum = mins.reduce((a, b) => a + b, 0);
  const remaining = Math.max(0, budget - minSum);
  const exact = active.map(({ ms }) => ((Number.isFinite(ms) && ms > 0 ? ms : 0) / totalMs) * remaining);
  const counts = active.map((_, index) => mins[index] + Math.floor(exact[index]));
  const leftover = budget - counts.reduce((a, b) => a + b, 0);

  const order = exact
    .map((value, index) => ({ index, remainder: value - Math.floor(value) }))
    .sort((a, b) => b.remainder - a.remainder || a.index - b.index);

  for (let i = 0; i < leftover; i += 1) {
    counts[order[i % order.length].index] += 1;
  }

  const cells = [];
  for (let i = 0; i < active.length; i += 1) {
    const key = active[i].key || 'total';
    for (let c = 0; c < counts[i] && cells.length < budget; c += 1) {
      cells.push(key);
    }
  }
  return cells;
}
