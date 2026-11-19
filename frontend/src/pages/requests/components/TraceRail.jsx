import { useCallback, useLayoutEffect, useRef } from 'react';
import { getStatusConfig } from '@utils/constants.js';
import { prefersReducedMotion } from '@utils/motion.js';
import { UNSETTLED_STATUSES } from '../utils/constants.js';
import { resolveElapsedMs, subscribeElapsedTick } from '../utils/elapsedTicker.js';
import { parseTimestampOrNull, railSegmentColors } from '../utils/lifecycle.js';
import { getRequestPresentation } from '../utils/requestPresentation.js';
import { formatTimingBadge } from '../utils/timing.js';
import {
  formatTraceElapsedBadge,
  getSettledRailSegments,
  getTraceRailPhase,
  quantizeSegmentsToCells,
  resolveLiveRailState,
  TRACE_RAIL_CELLS
} from '../utils/traceRail.js';

// 21 格波纹落位总时长（20 × 8ms 延迟 + 300ms 动画 + 余量）。
const SETTLE_ANIMATION_MS = 500;
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

// 状态列复用共享时钟直写 DOM：渐近生长格数、实时阶段弹性着色与降频总耗时
// 都不进 React state，保持请求行引用与动画相位稳定；终态与挂起零订阅。
function useTraceRail(request, status, startedAt, isLive, hasSegments) {
  const timeRef = useRef(null);
  const railRef = useRef(null);
  const snapshotRef = useRef({ request, status, startedAt });
  const wasLiveRef = useRef(null);
  const canTick = isLive && Number.isFinite(startedAt);

  const paint = useCallback(() => {
    const rail = railRef.current;
    if (!rail) return;
    const { request: current, status: currentStatus, startedAt: currentStart } = snapshotRef.current;
    const now = Date.now();
    const hasClock = Number.isFinite(currentStart);
    const elapsedMs = hasClock ? resolveElapsedMs(currentStart, 0, now) : 0;

    const time = timeRef.current;
    if (time && hasClock) {
      const label = formatTraceElapsedBadge(elapsedMs, { live: true });
      if (time.textContent !== label) time.textContent = label;
    }

    const state = resolveLiveRailState(current, currentStatus, elapsedMs, now);
    if (!state) return;

    const nodes = rail.children;
    for (let i = 0; i < TRACE_RAIL_CELLS && i < nodes.length; i += 1) {
      const nextClass = resolveCellClassName(state.cells[i], { isCursor: i === state.cursorIndex });
      if (nodes[i].className !== nextClass) {
        nodes[i].className = nextClass;
      }
    }
  }, []);

  useLayoutEffect(() => {
    snapshotRef.current = { request, status, startedAt };
    if (isLive) {
      paint();
    }
  }, [request, status, startedAt, isLive, paint]);

  useLayoutEffect(() => {
    const rail = railRef.current;
    if (!rail) return undefined;

    if (!isLive) {
      const shouldSettle = hasSegments
        && wasLiveRef.current === request.requestId
        && !prefersReducedMotion();
      wasLiveRef.current = null;
      if (!shouldSettle) return undefined;

      rail.dataset.settling = '';
      const timer = window.setTimeout(() => {
        delete rail.dataset.settling;
      }, SETTLE_ANIMATION_MS);
      return () => {
        window.clearTimeout(timer);
        delete rail.dataset.settling;
      };
    }

    wasLiveRef.current = request.requestId;
    if (!canTick) return undefined;
    return subscribeElapsedTick(paint);
  }, [request.requestId, isLive, canTick, hasSegments, paint]);

  return { timeRef, railRef };
}

export const RequestTraceCell = ({ request }) => {
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
  const { timeRef, railRef } = useTraceRail(request, status, startedAt, isLive, hasSegments);

  // 跑秒节点与在途格由时钟原地更新，结束或挂起后换成由 React 渲染的静态节点。
  return (
    <span className="inline-flex w-[124px] h-6 flex-col justify-center gap-[3px] pb-[1px] align-middle" title={description || undefined}>
      <span className="flex items-baseline justify-between gap-1.5 leading-[13px]">
        <span className={`min-w-0 truncate text-[11px] font-medium ${LABEL_CLASS[status] || 'text-fg-muted'}`}>
          {getStatusConfig(status).label}
        </span>
        <span className="shrink-0" title="总耗时">
          <span className="sr-only select-none">总耗时 </span>
          <span key={canTick ? 'live' : 'static'} ref={canTick ? timeRef : undefined} className="font-mono text-[10.5px] tabular-nums text-fg-muted">
            {canTick ? null : finalMs === null ? '—' : formatTraceElapsedBadge(finalMs, { live: false })}
          </span>
        </span>
      </span>
      <span
        key={isLive ? 'live' : 'static'}
        ref={railRef}
        className={isLive ? 'trace-rail trace-rail--live' : 'trace-rail'}
        title={railTitle || undefined}
        aria-hidden="true"
      >
        {isLive ? CELL_INDICES.map((index) => (
          <span key={index} className="trace-rail__cell trace-rail__cell--empty" />
        )) : CELL_INDICES.map((index) => (
          <span
            key={index}
            className={resolveCellClassName(settledCellKeys[index])}
            style={hasSegments ? { '--cell-index': index } : undefined}
          />
        ))}
      </span>
    </span>
  );
};
