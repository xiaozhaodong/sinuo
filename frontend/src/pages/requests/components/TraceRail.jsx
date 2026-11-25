import { useCallback, useLayoutEffect, useRef } from 'react';
import { getStatusConfig } from '@utils/constants.js';
import { prefersReducedMotion } from '@utils/motion.js';
import { UNSETTLED_STATUSES } from '../utils/constants.js';
import { subscribeElapsedTick } from '../utils/elapsedTicker.js';
import { railSegmentColors } from '../utils/lifecycle.js';
import { getRequestPresentation } from '../utils/requestPresentation.js';
import { formatTimingBadge } from '../utils/timing.js';
import {
  getSettledRailSegments,
  getTraceRailPhase,
  quantizeSegmentsToCells,
  resolveStepRailCells,
  resolveStepRailColor,
  resolveStepRailFrameKey,
  resolveStepRailMeta,
  TRACE_RAIL_CELLS,
  TRACE_RAIL_SETTLE_MS,
  traceRailLabelTone
} from '../utils/traceRail.js';

const CELL_INDICES = Array.from({ length: TRACE_RAIL_CELLS }, (_, index) => index);
const EMPTY_CELL_KEYS = Array.from({ length: TRACE_RAIL_CELLS }, () => null);
const EMPTY_CELL_CLASS = 'trace-rail__cell trace-rail__cell--empty';
const LABEL_BASE_CLASS = 'trace-rail__label min-w-0 truncate text-[11px] font-medium';

const SETTLED_LABEL_CLASS = {
  completed: 'text-success',
  failed: 'text-danger',
  error: 'text-danger',
  timeout: 'text-danger'
};

// 进行中文字平时中性灰，重试与慢首响随轨道一起变黄、变红；终态换成状态语义色。
const liveLabelClassName = (colorKey) => `${LABEL_BASE_CLASS} ${traceRailLabelTone(colorKey)}`;
const settledLabelClassName = (status) => `${LABEL_BASE_CLASS} ${SETTLED_LABEL_CLASS[status] || 'text-fg-muted'}`;

function cellClassName(colorKey, dim = false) {
  if (!colorKey) return EMPTY_CELL_CLASS;
  const colorClass = railSegmentColors[colorKey] || railSegmentColors.total;
  return dim ? `trace-rail__cell ${colorClass} trace-rail__cell--dim` : `trace-rail__cell ${colorClass}`;
}

function paintCells(rail, classNames) {
  const nodes = rail.children;
  for (let i = 0; i < TRACE_RAIL_CELLS && i < nodes.length; i += 1) {
    if (nodes[i].className !== classNames[i]) {
      nodes[i].className = classNames[i];
    }
  }
}

function paintLabel(label, colorKey) {
  if (!label) return;
  const next = liveLabelClassName(colorKey);
  if (label.className !== next) label.className = next;
}

// 进行中：共享时钟直写 DOM 推进步进轨与阶段文字颜色，不进 React state，行引用与 DOM 节点保持稳定。
// 结束：按真实比例着色；只有从进行中切过来的那一次加 data-settling，由 CSS 逐格错开换色。
// 首次出现即已结束的行不补播（它已有整行入场）；挂起不算结束、也不播；终态与挂起零订阅。
function useTraceRail({ request, status, isLive, settledCellKeys }) {
  const railRef = useRef(null);
  const labelRef = useRef(null);
  const snapshotRef = useRef({ request: null, status: null });
  const metaRef = useRef(null);
  const lastFrameKeyRef = useRef(null);
  const wasLiveRequestIdRef = useRef(null);
  const requestKey = request.requestId ?? '';
  const settledKey = settledCellKeys.join('|');
  // 挂起既不在进行中、也没有结束：不播结束换色，保留「进行过」的记录，等真正结束再播。
  const settled = !isLive && !UNSETTLED_STATUSES.has(status);

  const paint = useCallback(() => {
    const rail = railRef.current;
    const meta = metaRef.current;
    if (!rail || !meta) return;
    const now = Date.now();
    const options = { sweep: !prefersReducedMotion(), meta };
    const frameKey = resolveStepRailFrameKey(meta, now, options);
    if (frameKey === lastFrameKeyRef.current) return;
    lastFrameKeyRef.current = frameKey;
    const { request: current, status: currentStatus } = snapshotRef.current;
    const state = resolveStepRailCells(current, currentStatus, now, options);
    if (!state) return;
    paintCells(rail, state.cells.map(({ key, dim }) => cellClassName(key, dim)));
    // 慢首响变色只随时间发生、没有新的渲染：文字和格子在同一拍改色
    paintLabel(labelRef.current, state.color);
  }, []);

  // 每次渲染同步最新快照；仅当请求或状态变化时重算元数据，进行中阶段变化立即重绘。
  useLayoutEffect(() => {
    const prev = snapshotRef.current;
    snapshotRef.current = { request, status };
    if (!isLive) {
      metaRef.current = null;
      return;
    }
    if (prev.request !== request || prev.status !== status || !metaRef.current) {
      metaRef.current = resolveStepRailMeta(request, status);
    }
    paint();
  });

  useLayoutEffect(() => {
    if (!isLive) return undefined;
    wasLiveRequestIdRef.current = requestKey;
    paint();
    const unsubscribe = subscribeElapsedTick(paint);
    return () => {
      unsubscribe();
      lastFrameKeyRef.current = null;
    };
  }, [isLive, requestKey, paint]);

  // 只在真正结束的那一次播：绑定 requestId 防组件复用串行；换色期间计时晚到不打断、不重播。
  useLayoutEffect(() => {
    const rail = railRef.current;
    if (!rail || !settled || wasLiveRequestIdRef.current !== requestKey) return undefined;
    wasLiveRequestIdRef.current = null;
    if (prefersReducedMotion()) return undefined;
    rail.dataset.settling = '';
    const timer = window.setTimeout(() => {
      delete rail.dataset.settling;
    }, TRACE_RAIL_SETTLE_MS);
    return () => {
      window.clearTimeout(timer);
      delete rail.dataset.settling;
    };
  }, [settled, requestKey]);

  // 终态（含挂起）配色；计时晚到时原地改 class，正在播的逐格过渡自然承接。
  // 文字也要明确写回：进行中时钟直接改过 DOM，终态 class 若与渲染初值同串，React 不会去更新它。
  useLayoutEffect(() => {
    if (isLive) return;
    const rail = railRef.current;
    if (rail) paintCells(rail, settledKey.split('|').map((key) => cellClassName(key || null)));
    const label = labelRef.current;
    const next = settledLabelClassName(status);
    if (label && label.className !== next) label.className = next;
  }, [isLive, settledKey, status]);

  return { railRef, labelRef };
}

export const RequestTraceCell = ({ request }) => {
  const { status, description } = getRequestPresentation(request);
  const phase = getTraceRailPhase(request, status);
  const isLive = Boolean(phase);
  const finalMs = !UNSETTLED_STATUSES.has(status) && Number.isFinite(request.duration) ? request.duration : null;
  const segments = isLive ? [] : getSettledRailSegments(request, finalMs);
  const settledCellKeys = segments.length > 0 ? quantizeSegmentsToCells(segments, TRACE_RAIL_CELLS) : EMPTY_CELL_KEYS;
  const railTitle = phase?.label ?? segments.map(({ label, ms }) => `${label} ${formatTimingBadge(ms)}`).join(' · ');
  const label = isLive ? phase.label : getStatusConfig(status).label;
  // 渲染期不读时钟，进行中先按阶段给初值；真实等待时长下的颜色由共享时钟在同一帧改写。
  const labelClassName = isLive ? liveLabelClassName(resolveStepRailColor(request, phase)) : settledLabelClassName(status);
  const { railRef, labelRef } = useTraceRail({ request, status, isLive, settledCellKeys });

  // 进行中总耗时显示 —（跑秒只留在计时胶囊），结束才到货；轨道 DOM 恒定不卸载。
  return (
    <span className="inline-flex w-[124px] h-6 flex-col justify-center gap-[3px] pb-[1px] align-middle" title={description || undefined}>
      <span className="flex items-baseline justify-between gap-1.5 leading-[13px]">
        <span ref={labelRef} className={labelClassName}>
          {label}
        </span>
        <span className="shrink-0" title="总耗时">
          <span className="sr-only select-none">总耗时 </span>
          <span className="font-mono text-[10.5px] tabular-nums text-fg-muted">
            {finalMs === null ? '—' : formatTimingBadge(finalMs)}
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
            className={isLive ? EMPTY_CELL_CLASS : cellClassName(settledCellKeys[index])}
            style={{ '--i': index }}
          />
        ))}
      </span>
    </span>
  );
};
