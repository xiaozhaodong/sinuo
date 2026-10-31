import React from 'react';
import { createPortal } from 'react-dom';
import { RotateCw, TriangleAlert } from 'lucide-react';
import LiveElapsedPill from './LiveElapsedPill.jsx';
import PendingSendPill from './PendingSendPill.jsx';
import { IN_FLIGHT_STATUSES } from '../utils/constants.js';
import { firstResponseWait, visibleRetryCount } from '../utils/retryTiming.js';
import { resolveGenerationTickAnchor } from '../utils/elapsedTicker.js';
import { firstContentDelayMs, requestSpeedMetrics, stopReasonView } from '../utils/streamPhases.js';
import {
  formatOptionalTimingBadge, formatTimingBadge,
  getTimingPillClassName, resolveCompletionMs, resolveFirstResponseMs, TIMING_PILL_BASE
} from '../utils/timing.js';

export const TimingTooltip = ({ anchorRect, items, notes = [] }) => {
  const tooltipRef = React.useRef(null);
  // 说明文字会改变卡片高度；绘制前测量实际尺寸，下方放不下时翻到上方。
  React.useLayoutEffect(() => {
    const tooltip = tooltipRef.current;
    if (!anchorRect || !tooltip) return;
    const margin = 8;
    const gap = 6;
    const { width, height } = tooltip.getBoundingClientRect();
    const below = anchorRect.bottom + gap;
    const top = below + height + margin <= window.innerHeight
      ? below : anchorRect.top - height - gap;
    const left = anchorRect.left + (anchorRect.width - width) / 2;
    tooltip.style.left = `${Math.max(margin, Math.min(left, window.innerWidth - width - margin))}px`;
    tooltip.style.top = `${Math.max(margin, Math.min(top, window.innerHeight - height - margin))}px`;
  }, [anchorRect, items, notes]);

  if (!anchorRect) return null;
  return createPortal(
    <div ref={tooltipRef} role="tooltip"
      className="pointer-events-none fixed z-[10001] w-max max-w-[calc(100vw-16px)] rounded-lg border border-line bg-surface px-3 py-2 text-xs text-fg-body shadow-lg ring-1 ring-hairline"
      style={{ left: 0, top: 0 }}>
      <div className="grid grid-cols-[auto_auto] gap-x-3 gap-y-1">
        {items.map((item) => <React.Fragment key={item.label}>
          <span className="text-fg-subtle">{item.label}</span>
          <span className={`text-right font-mono font-semibold ${item.className || 'text-fg-body'}`}>{item.value}</span>
        </React.Fragment>)}
      </div>
      {notes.length > 0 && <div className="mt-2 max-w-72 space-y-1 border-t border-line-soft pt-2 whitespace-normal text-fg-subtle">
        {notes.map((note) => <p key={note}>{note}</p>)}
      </div>}
    </div>, document.body
  );
};

export const TimingPill = ({ value, threshold, fallback }) => (
  <span className={`${TIMING_PILL_BASE} ${getTimingPillClassName(threshold, value ?? fallback)}`}>
    {formatOptionalTimingBadge(value)}
  </span>
);

// 进行中不挂悬停提示：总耗时与速度还没落定，胶囊已经在实时跑秒，提示没有增量信息。
const useTimingTooltip = (running) => {
  const [tooltipAnchor, setTooltipAnchor] = React.useState(null);
  const open = tooltipAnchor !== null;
  React.useEffect(() => {
    if (!open) return;
    // 滚动或缩放后旧锚点已失效，关闭提示，避免卡片停留在原坐标。
    const hide = () => setTooltipAnchor(null);
    window.addEventListener('scroll', hide, true);
    window.addEventListener('resize', hide);
    return () => {
      window.removeEventListener('scroll', hide, true);
      window.removeEventListener('resize', hide);
    };
  }, [open]);
  const show = (event) => setTooltipAnchor(event.currentTarget.getBoundingClientRect());
  const hide = () => setTooltipAnchor(null);
  return { tooltipAnchor, hoverHandlers: running ? undefined : {
    onMouseEnter: show, onMouseMove: show, onMouseLeave: hide, onFocus: show, onBlur: hide
  } };
};

const timingCellData = (request, running, first) => {
  const contentDelayMs = firstContentDelayMs(request);
  const completion = Number.isFinite(first)
    ? resolveCompletionMs(request.completionMs, request.duration, first, request.isStreaming) : null;
  const stop = stopReasonView(request.stopReason);
  const retryCount = visibleRetryCount(request, running);
  const metrics = requestSpeedMetrics(request);
  const notes = [...new Set(metrics.map((metric) => metric.note).filter(Boolean))];
  const items = [
    { label: '总耗时', value: formatTimingBadge(request.duration) },
    ...metrics.map(({ label, value, tone }) => ({ label, value,
      className: tone === 'tone-amber' ? 'text-amber-700 dark:text-amber-300' : '' }))
  ];
  if (contentDelayMs !== null) {
    items.unshift({ label: '首字', value: formatTimingBadge(contentDelayMs) });
  }
  if (retryCount > 0) items.push({ label: '重试', value: `${retryCount} 次` });
  if (stop.warning) items.push({ label: '结束', value: stop.label, className: stop.className });
  return { first, completion, stop, retryCount, items, notes };
};

// 首响位从上游写完开始跑，在首个事件定格；定格与跑秒共用同一起点。
// 尚未发到上游时首响计时没有开始，只放等待点、不给数字；重试等待规则也由此复用。
const RunningFirstResponsePill = ({ request }) => {
  const waiting = firstResponseWait(request);
  return waiting
    ? <PendingSendPill {...waiting} />
    : <LiveElapsedPill timestamp={request.firstTokenStartAt} threshold="first" />;
};

const FirstTimingPill = ({ request, data, timingRunning }) => {
  if (Number.isFinite(data.first)) {
    return <TimingPill value={data.first} threshold="first" />;
  }
  return timingRunning ? <RunningFirstResponsePill request={request} /> : null;
};

// 收到首个事件时，首响位定格、生成位从同一事件时刻起跑；首内容不参与交接。
// 首响未到时不画生成位，避免把“还没开始”画成“-”；完成计时到达就定格。
const GenerationTimingPill = ({ request, data, timingRunning, firstResponseMs }) => {
  if (!timingRunning) {
    return <TimingPill value={data.completion} threshold="duration" fallback={request.duration} />;
  }
  if (!Number.isFinite(data.first)) return null;
  return <LiveElapsedPill {...resolveGenerationTickAnchor(request, firstResponseMs)} threshold="duration" />;
};

const RequestTimingCell = ({ request }) => {
  const running = IN_FLIGHT_STATUSES.has(request.status);
  const { tooltipAnchor, hoverHandlers } = useTimingTooltip(running);
  // 计时可以比状态先定格：completionMs随事件先到，状态与token要等下一次查询。
  const timingRunning = running && !Number.isFinite(request.completionMs);
  // 进行中只认后端真实写入的firstTokenMs，由首个事件决定两格计时交接。
  // 非流式的duration兜底只适用于已结束请求：进行中duration仍为0，会先显示假的0.0s。
  const firstResponseMs = running
    ? (Number.isFinite(request.firstTokenMs) ? Math.max(request.firstTokenMs, 0) : null)
    : resolveFirstResponseMs(request.firstTokenMs, request.duration, request.isStreaming);
  const data = timingCellData(request, running, firstResponseMs);
  return <div className="inline-flex items-center gap-1.5" {...hoverHandlers}>
    <FirstTimingPill request={request} data={data} timingRunning={timingRunning} />
    <GenerationTimingPill request={request} data={data} timingRunning={timingRunning} firstResponseMs={firstResponseMs} />
    {data.retryCount > 0 && <span className="inline-flex items-center gap-0.5 font-mono text-[11px] leading-4 font-medium text-amber-700 dark:text-amber-300" title={`重试 ${data.retryCount} 次`}>
      <RotateCw size={11} strokeWidth={2.5} aria-hidden="true" />{data.retryCount}
    </span>}
    {data.stop.warning && <span className={`inline-flex items-center ${data.stop.className}`} title={data.stop.label}>
      <TriangleAlert size={11} strokeWidth={2.5} aria-hidden="true" />
    </span>}
    {!running && <TimingTooltip anchorRect={tooltipAnchor} items={data.items} notes={data.notes} />}
  </div>;
};

export default RequestTimingCell;

