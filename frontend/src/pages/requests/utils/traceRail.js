import { IN_FLIGHT_STATUSES } from './constants.js';
import { buildRailLifecycleSegments, parseTimestampOrNull } from './lifecycle.js';
import { formatTimingBadge, TIMING_THRESHOLDS_MS } from './timing.js';

// 21 个 4px 色格 + 20 道 2px 间隙 = 精确 124px（零亚像素，首尾齐平）。
export const TRACE_RAIL_CELLS = 21;
// 进行中最多填满 20 格，末尾始终留 1 格给呼吸前沿游标，避免顶死或与终态混淆。
export const TRACE_RAIL_MAX_LIVE_FILLED = TRACE_RAIL_CELLS - 1;
// 渐近生长半饱和点（与首响警告阈值对齐：8s 刚好长到一半）。
export const TRACE_RAIL_HALF_SATURATION_MS = TIMING_THRESHOLDS_MS.first.warning;

const PHASE = {
  preparing: { key: 'preparing', label: '准备发送', color: 'queue' },
  forwarding: { key: 'forwarding', label: '发往上游', color: 'send' },
  retry: { key: 'retry', label: '重试中', color: 'retry' },
  first: { key: 'first', label: '等待首响', color: 'first' },
  stream: { key: 'stream', label: '生成中', color: 'stream' },
  settling: { key: 'settling', label: '收尾', color: 'tail' }
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

/**
 * 渐近时间尺：按已耗时将活跃格数平滑映射到 [0, 20]。
 * 短请求精悍、长请求持续拉长但越久越缓，永远留至少 1 格游标，不会撞顶卡死。
 */
export function resolveAsymptoticFilledCells(elapsedMs) {
  if (!Number.isFinite(elapsedMs) || elapsedMs <= 0) return 0;
  return Math.min(
    TRACE_RAIL_MAX_LIVE_FILLED,
    Math.round((TRACE_RAIL_MAX_LIVE_FILLED * elapsedMs) / (elapsedMs + TRACE_RAIL_HALF_SATURATION_MS))
  );
}

/**
 * 状态列右上角总耗时格式：
 *   - 终态沿用 formatTimingBadge，与详情/提示口径完全一致；
 *   - 进行中适当降频（1s 内按 50ms 步进、1s 以上保留 1 位小数），
 *     作为宏观参考，不与右侧「首响 / 生成」列的 0.01s 精密秒表抢焦点。
 */
export function formatTraceElapsedBadge(ms, { live = false } = {}) {
  if (!live) return formatTimingBadge(ms);
  const value = Number.isFinite(ms) ? Math.max(ms, 0) : 0;
  if (value < 1000) {
    const stepped = Math.min(950, Math.floor(value / 50) * 50);
    return `${stepped}ms`;
  }
  return `${(value / 1000).toFixed(1)}s`;
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

const resolveFirstWaitColorKey = (waitMs) => {
  if (waitMs > TIMING_THRESHOLDS_MS.first.critical) return 'failure';
  if (waitMs > TIMING_THRESHOLDS_MS.first.warning) return 'retry';
  return 'first';
};

/**
 * 推导进行中请求截至此刻的真实已耗时分段（与终态 buildRailLifecycleSegments 同源），
 * 用于驱动 A2 方案已生长格内的弹性阶段着色与慢首响预警。
 */
export function buildLiveRailSegments(request, phase, elapsedMs, now) {
  const total = Number.isFinite(elapsedMs) ? Math.max(0, elapsedMs) : 0;
  if (!phase) return { segments: [], cursorColorKey: null };

  const { startedAt, sentAt, retryAt, writeOffset: rawWriteOffset } = resolveLatestWriteInfo(request);
  const routeAt = parseTimestampOrNull(request.routeDecisionAt)?.getTime();
  const firstTokenAt = parseTimestampOrNull(request.firstTokenAt)?.getTime();

  const localOffset = hasTiming(request.upstreamFirstDispatchMs)
    ? Math.min(request.upstreamFirstDispatchMs, total)
    : (Number.isFinite(startedAt) && Number.isFinite(routeAt)
      ? Math.min(Math.max(0, routeAt - startedAt), total)
      : null);

  const writeOffset = rawWriteOffset !== null ? Math.min(rawWriteOffset, total) : null;

  const hadRetry = phase.key === 'retry' || Number.isFinite(retryAt);

  const pushPrefix = (segments, prefixMs) => {
    const bounded = Math.max(0, prefixMs);
    if (bounded <= 0) return;
    const queueMs = localOffset !== null ? Math.min(localOffset, bounded) : 0;
    if (queueMs > 0) segments.push({ key: 'queue', ms: queueMs });
    const restMs = Math.max(0, bounded - queueMs);
    if (restMs > 0) segments.push({ key: hadRetry ? 'retry' : 'send', ms: restMs });
  };

  if (phase.key === 'preparing') {
    return {
      segments: total > 0 ? [{ key: 'queue', ms: total }] : [],
      cursorColorKey: 'queue'
    };
  }

  if (phase.key === 'forwarding') {
    const segments = [];
    if (total > 0) {
      const queueMs = localOffset !== null ? Math.min(localOffset, total) : 0;
      if (queueMs > 0) segments.push({ key: 'queue', ms: queueMs });
      const sendMs = Math.max(0, total - queueMs);
      if (sendMs > 0) segments.push({ key: hadRetry ? 'retry' : 'send', ms: sendMs });
    }
    return { segments, cursorColorKey: hadRetry ? 'retry' : 'send' };
  }

  if (phase.key === 'retry') {
    const segments = [];
    if (total > 0) {
      const queueMs = localOffset !== null ? Math.min(localOffset, total) : 0;
      if (queueMs > 0) segments.push({ key: 'queue', ms: queueMs });
      const retryMs = Math.max(0, total - queueMs);
      if (retryMs > 0) segments.push({ key: 'retry', ms: retryMs });
    }
    return { segments, cursorColorKey: 'retry' };
  }

  if (phase.key === 'first') {
    const preWriteMs = writeOffset ?? 0;
    // 有请求起点时 writeOffset 已取最新写出偏移，不能再用可能滞后的 sentAt 取 max 误报慢首响。
    const waitMs = Number.isFinite(startedAt)
      ? Math.max(0, total - preWriteMs)
      : (Number.isFinite(sentAt) && Number.isFinite(now) ? Math.max(0, now - sentAt) : 0);
    const waitColorKey = resolveFirstWaitColorKey(waitMs);
    const segments = [];
    pushPrefix(segments, preWriteMs);
    if (waitMs > 0) segments.push({ key: waitColorKey, ms: waitMs });
    return { segments, cursorColorKey: waitColorKey };
  }

  // stream / settling：首响已定格，保持常规紫罗兰，与终态色彩衔接一致。
  const firstMs = hasTiming(request.firstTokenMs) ? Math.max(0, request.firstTokenMs) : 0;
  const completionMs = hasTiming(request.completionMs) ? Math.max(0, request.completionMs) : null;
  const retryOffset = Number.isFinite(startedAt) && Number.isFinite(retryAt)
    ? Math.min(Math.max(0, retryAt - startedAt), total)
    : null;

  // 首响绝对时刻已到时，以 firstTokenAt 为权威交接点反推前置与生成时长，
  // 避免重试后 upstreamWriteMs 尚未刷新时把前置重试等待误算进绿色生成段。
  const hasFirstAnchor = Number.isFinite(startedAt) && Number.isFinite(firstTokenAt);
  let preWriteMs;
  let effectiveFirstMs = firstMs;
  let streamEndOffset;

  if (hasFirstAnchor) {
    const firstOffset = Math.min(total, Math.max(0, firstTokenAt - startedAt));
    preWriteMs = Math.max(0, firstOffset - firstMs);
    effectiveFirstMs = Math.min(firstMs, firstOffset);
    streamEndOffset = firstOffset;
  } else {
    const maxPreWrite = Math.max(0, total - firstMs - (completionMs ?? 0));
    if (writeOffset !== null) {
      const floorOffset = retryOffset !== null ? Math.max(writeOffset, retryOffset) : writeOffset;
      preWriteMs = Math.min(floorOffset, Math.max(0, total - firstMs));
    } else {
      preWriteMs = maxPreWrite;
    }
    streamEndOffset = Math.min(total, preWriteMs + effectiveFirstMs);
  }

  const segments = [];
  pushPrefix(segments, preWriteMs);
  if (effectiveFirstMs > 0) segments.push({ key: 'first', ms: effectiveFirstMs });

  if (phase.key === 'stream') {
    const streamMs = Math.max(0, total - streamEndOffset);
    if (streamMs > 0) segments.push({ key: 'stream', ms: streamMs });
    return { segments, cursorColorKey: 'stream' };
  }

  if (completionMs !== null && completionMs > 0) {
    segments.push({ key: 'stream', ms: completionMs });
  }
  const tailMs = Math.max(0, total - streamEndOffset - (completionMs ?? 0));
  if (tailMs > 0) segments.push({ key: 'tail', ms: tailMs });
  return { segments, cursorColorKey: 'tail' };
}

/**
 * 计算进行中请求的完整 21 格状态快照（方案 A2：渐近生长 + 内部弹性占比 + 活游标）。
 */
export function resolveLiveRailState(request, status, elapsedMs, now) {
  const phase = getTraceRailPhase(request, status);
  if (!phase) return null;

  const filled = resolveAsymptoticFilledCells(elapsedMs);
  const { segments, cursorColorKey } = buildLiveRailSegments(request, phase, elapsedMs, now);
  const bodyKeys = filled > 0
    ? quantizeSegmentsToCells(segments.length > 0 ? segments : [{ key: cursorColorKey || phase.color, ms: 1 }], filled)
    : [];

  const cells = Array.from({ length: TRACE_RAIL_CELLS }, () => null);
  for (let i = 0; i < bodyKeys.length; i += 1) {
    cells[i] = bodyKeys[i];
  }
  const cursorIndex = Math.min(filled, TRACE_RAIL_CELLS - 1);
  cells[cursorIndex] = cursorColorKey || phase.color;

  return {
    phase,
    filled,
    cursorIndex,
    cursorColorKey: cursorColorKey || phase.color,
    cells,
    segments
  };
}
