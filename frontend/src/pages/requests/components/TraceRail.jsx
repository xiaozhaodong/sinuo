import { useCallback, useLayoutEffect, useRef } from 'react';
import { getStatusConfig } from '@utils/constants.js';
import { prefersReducedMotion } from '@utils/motion.js';
import { UNSETTLED_STATUSES } from '../utils/constants.js';
import { resolveElapsedMs, subscribeElapsedTick } from '../utils/elapsedTicker.js';
import { parseTimestampOrNull, railSegmentColors } from '../utils/lifecycle.js';
import { getRequestPresentation } from '../utils/requestPresentation.js';
import { formatTimingBadge } from '../utils/timing.js';
import {
  applyLiveEnterRamp,
  easeTraceRailSettle,
  formatTraceElapsedBadge,
  getSettledRailSegments,
  getTraceRailPhase,
  quantizeSegmentsToCells,
  resolveEffectiveLiveElapsed,
  resolveLiveRailState,
  resolveTraceRailSettleCells,
  resolveTraceRailSettleDuration,
  TRACE_RAIL_CELLS,
  TRACE_RAIL_ENTER_RAMP_MS,
  TRACE_RAIL_FRESH_SETTLE_LEAD_MS,
  TRACE_RAIL_SETTLE_BLEND_MS
} from '../utils/traceRail.js';

const CELL_INDICES = Array.from({ length: TRACE_RAIL_CELLS }, (_, index) => index);
const EMPTY_CELL_KEYS = Array.from({ length: TRACE_RAIL_CELLS }, () => null);

const LABEL_CLASS = {
  forwarding: 'text-accent-fg',
  processing: 'text-accent-fg',
  retry: 'text-warn',
  completed: 'text-success',
  failed: 'text-danger',
  error: 'text-danger',
  timeout: 'text-danger'
};

function resolveCellClassName(colorKey, { isCursor = false } = {}) {
  if (!colorKey) return 'trace-rail__cell trace-rail__cell--empty';
  const colorClass = isCursor && (colorKey === 'queue' || colorKey === 'tail')
    ? 'bg-fg-muted'
    : (railSegmentColors[colorKey] || railSegmentColors.total);
  return isCursor
    ? `trace-rail__cell ${colorClass} trace-rail__cell--cursor`
    : `trace-rail__cell ${colorClass}`;
}

function paintCells(rail, cellKeys, cursorIndex = -1) {
  const nodes = rail.children;
  for (let i = 0; i < TRACE_RAIL_CELLS && i < nodes.length; i += 1) {
    const nextClass = resolveCellClassName(cellKeys[i], { isCursor: i === cursorIndex });
    if (nodes[i].className !== nextClass) {
      nodes[i].className = nextClass;
    }
  }
}

// 状态列复用共享时钟直写 DOM：渐近生长格数、实时阶段弹性着色、连续延展收尾与降频总耗时
// 都不进 React state，保持请求行引用与 DOM 节点稳定；终态与挂起零订阅。
function useTraceRail({
  request,
  status,
  startedAt,
  isLive,
  hasSegments,
  segments,
  settledCellKeys,
  enterAnimation
}) {
  const timeRef = useRef(null);
  const railRef = useRef(null);
  const snapshotRef = useRef({ request, status, startedAt, segments, settledCellKeys });
  // 固化挂载时的实时入场标记：只有实时新增行（enterAnimation != null）
  // 才在首现即终态或刚起跑时播延展，首屏 / 翻页 / 筛选历史行保持完全静止。
  const enterMetaRef = useRef(
    enterAnimation
      ? {
          requestId: request.requestId,
          delayMs: enterAnimation.delayMs || 0,
          mountedAt: null,
          consumedSettled: false
        }
      : null
  );
  const lastLiveRef = useRef(null);
  const settleCancelRef = useRef(null);
  // 只记录收尾当前推进到的格数（count）而非静态颜色快照，确保收尾中途或尾段过渡窗口里
  // 晚到的分段计时（如首响/流式输出）能立刻按最新 segments 重算配色，不残留旧灰格。
  const activeSettleRef = useRef({ active: false, count: TRACE_RAIL_CELLS });
  const canTick = isLive && Number.isFinite(startedAt);

  const paint = useCallback(() => {
    const rail = railRef.current;
    if (!rail) return;
    const { request: current, status: currentStatus, startedAt: currentStart } = snapshotRef.current;
    const now = Date.now();
    const hasClock = Number.isFinite(currentStart);
    const rawElapsedMs = hasClock ? resolveElapsedMs(currentStart, 0, now) : 0;
    const phase = getTraceRailPhase(current, currentStatus);
    const { elapsedMs, keepStreamCursor } = resolveEffectiveLiveElapsed(current, phase, rawElapsedMs, currentStart);

    const time = timeRef.current;
    if (time && hasClock) {
      const label = formatTraceElapsedBadge(elapsedMs, { live: true });
      if (time.textContent !== label) time.textContent = label;
    }

    let state = resolveLiveRailState(current, currentStatus, elapsedMs, now);
    if (!state) return;

    const enterMeta = enterMetaRef.current;
    if (enterMeta && enterMeta.requestId === current.requestId && !prefersReducedMotion()) {
      const perfNow = performance.now();
      if (enterMeta.mountedAt === null) {
        enterMeta.mountedAt = perfNow;
      }
      const sinceMount = perfNow - enterMeta.mountedAt - enterMeta.delayMs;
      if (sinceMount < TRACE_RAIL_ENTER_RAMP_MS) {
        state = applyLiveEnterRamp(state, sinceMount, TRACE_RAIL_ENTER_RAMP_MS);
      }
    }

    const cells = state.cells;
    if (keepStreamCursor && state.cursorIndex >= 0 && cells[state.cursorIndex] === 'tail') {
      cells[state.cursorIndex] = 'stream';
    }

    paintCells(rail, cells, state.cursorIndex);
    lastLiveRef.current = {
      requestId: current.requestId,
      litCount: Math.max(1, Math.min(TRACE_RAIL_CELLS, state.cursorIndex + 1)),
      cells: [...cells]
    };
  }, []);

  useLayoutEffect(() => {
    snapshotRef.current = { request, status, startedAt, segments, settledCellKeys };
    const rail = railRef.current;
    if (!rail) return;
    if (isLive) {
      paint();
    } else if (activeSettleRef.current.active) {
      // 收尾推进或尾段色块过渡期间若晚到了分段计时（例如从全灰总耗时补全为首响+流式），
      // 立即按当前推进格数与最新 segments 重绘，既不提前跳满，也不会用旧快照把新配色覆盖回灰色。
      paintCells(rail, resolveTraceRailSettleCells(segments, settledCellKeys, activeSettleRef.current.count));
    } else {
      paintCells(rail, settledCellKeys);
    }
  }, [request, status, startedAt, isLive, segments, settledCellKeys, paint]);

  useLayoutEffect(() => {
    const rail = railRef.current;
    if (!rail) return undefined;

    if (settleCancelRef.current) {
      settleCancelRef.current();
      settleCancelRef.current = null;
    }

    if (!isLive) {
      const liveSnap = lastLiveRef.current;
      const cameFromLive = Boolean(liveSnap && liveSnap.requestId === request.requestId);
      lastLiveRef.current = null;

      const enterMeta = enterMetaRef.current;
      const isFreshSettledArrival = Boolean(
        !cameFromLive
        && enterMeta
        && enterMeta.requestId === request.requestId
        && !enterMeta.consumedSettled
      );

      const shouldSettle = hasSegments
        && (cameFromLive || isFreshSettledArrival)
        && !prefersReducedMotion();

      if (!shouldSettle) {
        activeSettleRef.current = { active: false, count: TRACE_RAIL_CELLS };
        delete rail.dataset.settling;
        paintCells(rail, snapshotRef.current.settledCellKeys);
        return undefined;
      }

      const startCount = cameFromLive
        ? Math.max(1, Math.min(TRACE_RAIL_CELLS, liveSnap.litCount))
        : 0;
      const initialCells = cameFromLive ? liveSnap.cells : EMPTY_CELL_KEYS;
      const remainingCells = TRACE_RAIL_CELLS - startCount;
      const sweepMs = resolveTraceRailSettleDuration(startCount);
      const startDelayMs = isFreshSettledArrival
        ? (enterMeta?.delayMs || 0) + TRACE_RAIL_FRESH_SETTLE_LEAD_MS
        : 0;

      rail.dataset.settling = '';
      // Frame 0（绘制前）：原样承接最后一帧的亮格与空槽（极速新行则从全空槽起步），零跳变、零闪暗。
      activeSettleRef.current = { active: true, count: startCount };
      paintCells(rail, initialCells, -1);

      let rafId = 0;
      let teardownTimer = 0;
      let lastPaintedCount = -1;
      let lastSettledKeys = null;
      const startTime = performance.now() + startDelayMs;

      const step = (now) => {
        const elapsed = now - startTime;
        if (elapsed < 0) {
          rafId = window.requestAnimationFrame(step);
          return;
        }
        // 等真实动画帧开始推进后再标记已消费，避免 React StrictMode 挂载→清理→重挂
        // 同步双跑 effect 时提前把 consumedSettled 置 true 导致开发模式跳过收尾动画。
        if (isFreshSettledArrival && enterMeta) {
          enterMeta.consumedSettled = true;
        }
        const progress = sweepMs > 0 ? Math.min(1, elapsed / sweepMs) : 1;
        const count = Math.min(TRACE_RAIL_CELLS, startCount + Math.round(remainingCells * easeTraceRailSettle(progress)));
        const latest = snapshotRef.current;

        if (count !== lastPaintedCount || latest.settledCellKeys !== lastSettledKeys || progress >= 1) {
          lastPaintedCount = count;
          lastSettledKeys = latest.settledCellKeys;
          const nextCells = resolveTraceRailSettleCells(latest.segments, latest.settledCellKeys, count);
          activeSettleRef.current = { active: true, count };
          paintCells(rail, nextCells);
        }

        if (progress < 1) {
          rafId = window.requestAnimationFrame(step);
        } else {
          rafId = 0;
          teardownTimer = window.setTimeout(() => {
            teardownTimer = 0;
            activeSettleRef.current = { active: false, count: TRACE_RAIL_CELLS };
            paintCells(rail, snapshotRef.current.settledCellKeys);
            delete rail.dataset.settling;
          }, TRACE_RAIL_SETTLE_BLEND_MS + 40);
        }
      };

      // 下一帧起步：保证浏览器先稳定提交起点色，后续每帧推进的 CSS background-color 过渡都有真实起点。
      rafId = window.requestAnimationFrame(step);

      const cancel = () => {
        if (rafId) {
          window.cancelAnimationFrame(rafId);
          rafId = 0;
        }
        if (teardownTimer) {
          window.clearTimeout(teardownTimer);
          teardownTimer = 0;
        }
        activeSettleRef.current = { active: false, count: TRACE_RAIL_CELLS };
        delete rail.dataset.settling;
      };
      settleCancelRef.current = cancel;
      return cancel;
    }

    activeSettleRef.current = { active: false, count: TRACE_RAIL_CELLS };
    delete rail.dataset.settling;
    paint();
    if (!canTick) return undefined;
    return subscribeElapsedTick(paint);
  }, [request.requestId, isLive, canTick, hasSegments, paint]);

  return { timeRef, railRef };
}

export const RequestTraceCell = ({ request, enterAnimation = null }) => {
  const { status, description } = getRequestPresentation(request);
  const phase = getTraceRailPhase(request, status);
  const isLive = Boolean(phase);
  const startedAt = isLive ? parseTimestampOrNull(request.timestamp)?.getTime() : undefined;
  const canTick = isLive && Number.isFinite(startedAt);
  const finalMs = !UNSETTLED_STATUSES.has(status) && Number.isFinite(request.duration) ? request.duration : null;
  const segments = isLive ? [] : getSettledRailSegments(request, finalMs);
  const hasSegments = segments.length > 0;
  const settledCellKeys = hasSegments ? quantizeSegmentsToCells(segments, TRACE_RAIL_CELLS) : EMPTY_CELL_KEYS;
  const railTitle = phase?.label ?? segments.map(({ label, ms }) => `${label} ${formatTimingBadge(ms)}`).join(' · ');
  const { timeRef, railRef } = useTraceRail({
    request,
    status,
    startedAt,
    isLive,
    hasSegments,
    segments,
    settledCellKeys,
    enterAnimation
  });

  // 跑秒节点与轨道 DOM 保持恒定不卸载，结束时原地承接在途亮格平滑延展至 21 格。
  return (
    <span className="inline-flex w-[124px] h-6 flex-col justify-center gap-[3px] pb-[1px] align-middle" title={description || undefined}>
      <span className="flex items-baseline justify-between gap-1.5 leading-[13px]">
        <span className={`trace-rail__label min-w-0 truncate text-[11px] font-medium ${LABEL_CLASS[status] || 'text-fg-muted'}`}>
          {getStatusConfig(status).label}
        </span>
        <span className="shrink-0" title="总耗时">
          <span className="sr-only select-none">总耗时 </span>
          <span ref={timeRef} className="font-mono text-[10.5px] tabular-nums text-fg-muted">
            {canTick ? '' : finalMs === null ? '—' : formatTraceElapsedBadge(finalMs, { live: false })}
          </span>
        </span>
      </span>
      <span
        ref={railRef}
        className={isLive ? 'trace-rail trace-rail--live' : 'trace-rail'}
        title={railTitle || undefined}
        aria-hidden="true"
      >
        {CELL_INDICES.map((index) => (
          <span
            key={index}
            className={isLive ? 'trace-rail__cell trace-rail__cell--empty' : resolveCellClassName(settledCellKeys[index])}
          />
        ))}
      </span>
    </span>
  );
};
