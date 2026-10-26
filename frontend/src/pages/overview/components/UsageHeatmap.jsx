import { useId, useLayoutEffect, useMemo, useRef, useState } from 'react';
import { Area, AreaChart, Bar, BarChart, CartesianGrid, Cell, ResponsiveContainer, Tooltip, XAxis, YAxis } from 'recharts';
import Panel from '@components/desktop/Panel.jsx';
import SegmentedControl from '@components/desktop/SegmentedControl.jsx';
import useChartTheme from '@hooks/useChartTheme.js';
import SectionError from './SectionError.jsx';
import {
  HEATMAP_METRICS, HEATMAP_TOOLTIP_WIDTH, HEATMAP_VIEWS,
  buildHeatmapCumulative, buildHeatmapDays, buildHeatmapWeeks,
  formatHeatmapValue, heatmapCellBackground, heatmapKeyboardTarget, heatmapLayout,
  heatmapMonthLabels, heatmapSummary, heatmapTooltipPosition
} from '../utils/usageHeatmap.js';

const UsageTooltip = ({ content, chart, id }) => (
  <div id={id} role="tooltip" className="pointer-events-none rounded-lg px-2.5 py-2 text-[11px]" style={chart.tooltip.contentStyle}>
    <div className="mb-1 font-medium">{content.heading}</div>
    {content.note && <div className="mb-1 text-fg-muted">{content.note}</div>}
    {content.status && <div>{content.status}</div>}
    {content.rows.map((row) => (
      <div key={row.label} className={`flex items-center justify-between gap-4 leading-[17px] ${row.strong ? 'font-semibold' : ''}`}>
        <span className={row.indent ? 'pl-2 text-fg-muted' : ''}>{row.label}</span>
        <span className="font-mono tabular-nums">{row.value}</span>
      </div>
    ))}
  </div>
);

const ChartTooltip = ({ active, payload, chart }) => active && payload?.[0]?.payload?.tooltip
  ? <UsageTooltip content={payload[0].payload.tooltip} chart={chart} />
  : null;

const UsageHeatmap = ({ data, loading, error, onRetry, onOpenRequests }) => {
  const [metric, setMetric] = useState('tokens');
  const [view, setView] = useState('daily');
  const [width, setWidth] = useState(0);
  const [hover, setHover] = useState(null);
  const [focusDate, setFocusDate] = useState(null);
  const bodyRef = useRef(null);
  const tooltipRef = useRef(null);
  const tooltipId = useId();
  const chart = useChartTheme();

  useLayoutEffect(() => {
    const body = bodyRef.current;
    // ResizeObserver 首次回调是异步的，先同步测量，避免首帧因宽度为 0 留空。
    setWidth(body.getBoundingClientRect().width);
    const observer = new ResizeObserver(([entry]) => setWidth(entry.contentRect.width));
    observer.observe(body);
    return () => observer.disconnect();
  }, []);

  // 浮层先渲染在面板左上角，量出实际尺寸后在绘制前直接改 DOM 位置（不经过 state，避免二次渲染）
  useLayoutEffect(() => {
    const tooltip = tooltipRef.current;
    const body = bodyRef.current;
    if (!hover || !tooltip || !body) return;
    const position = heatmapTooltipPosition(hover.anchor, body.getBoundingClientRect(), tooltip.getBoundingClientRect());
    tooltip.style.left = `${position.left}px`;
    tooltip.style.top = `${position.top}px`;
  }, [hover]);

  const days = useMemo(() => buildHeatmapDays(data, metric), [data, metric]);
  const weeks = useMemo(() => buildHeatmapWeeks(days, data?.end_date, metric), [days, data?.end_date, metric]);
  const cumulative = useMemo(() => buildHeatmapCumulative(days, metric), [days, metric]);
  const summary = heatmapSummary(days, metric);
  const layout = heatmapLayout(width, weeks.length || 53);
  const visibleWeeks = weeks.slice(weeks.length - layout.visibleWeeks);
  const monthLabels = heatmapMonthLabels(visibleWeeks);
  const allMonthLabels = heatmapMonthLabels(weeks);
  const monthLabelMap = new Map(allMonthLabels.map((label) => [label.date, label.label]));
  const cumulativeLabelMap = new Map(allMonthLabels.map((label) => [label.dayDate, label.label]));

  // 首次加载才显示骨架；手动刷新期间保留上一份数据，与概览其他模块一致
  const showSkeleton = loading && !data;
  const visibleDays = visibleWeeks.flatMap((week) => week.days);
  // 日历只有一个 Tab 位：默认落在今天，之后跟随最近一次聚焦的格子
  const tabDate = visibleDays.some((day) => day.date === focusDate) ? focusDate : visibleDays[visibleDays.length - 1]?.date;

  const showDayTooltip = (event, day) => {
    const bounds = bodyRef.current.getBoundingClientRect();
    const cell = event.currentTarget.getBoundingClientRect();
    setHover({
      key: day.date, content: day.tooltip,
      anchor: { left: cell.left - bounds.left, right: cell.right - bounds.left, centerY: cell.top - bounds.top + cell.height / 2 }
    });
  };
  const moveFocus = (event) => {
    const target = heatmapKeyboardTarget(visibleDays, event.target.dataset?.date, event.key);
    if (!target) return;
    event.preventDefault();
    setFocusDate(target);
    bodyRef.current.querySelector(`[data-date="${target}"]`)?.focus();
  };
  const openWeek = (entry) => {
    if (entry.filters) onOpenRequests?.(entry.filters);
  };

  return (
    <Panel
      title="用量活动"
      subtitle={showSkeleton || error ? '近一年' : data?.data_start_date ? summary.subtitle : '近一年'}
      className="[&_header]:h-auto [&_header]:min-h-[34px] [&_header]:flex-wrap [&_header]:py-1"
      bodyClassName="px-3 py-3"
      actions={(
        <div className="flex flex-wrap justify-end gap-2">
          <SegmentedControl options={HEATMAP_METRICS} value={metric} ariaLabel="用量活动指标" onChange={(next) => { setHover(null); setMetric(next); }} />
          <SegmentedControl options={HEATMAP_VIEWS} value={view} ariaLabel="用量活动视图" onChange={(next) => { setHover(null); setView(next); }} />
        </div>
      )}
    >
      {/* 最大日历格 7×16 + 6×3，加月份和图例共 176px；三种视图共用，不随切换跳动。 */}
      <div ref={bodyRef} className="relative h-[176px] min-w-0" aria-busy={loading}>
        {showSkeleton ? (
          <div className="flex h-full items-start justify-center" role="status" aria-label="加载用量活动">
            <div className="grid gap-[3px]" style={{ gridTemplateColumns: `repeat(${layout.visibleWeeks}, ${layout.cellSize}px)` }}>
              {Array.from({ length: layout.visibleWeeks }, (_, index) => (
                <div key={index} className="grid grid-rows-7 gap-[3px]">
                  {Array.from({ length: 7 }, (_, row) => <span key={row} className="rounded-[2px] bg-surface-mut" style={{ width: layout.cellSize, height: layout.cellSize }} />)}
                </div>
              ))}
            </div>
          </div>
        ) : error ? (
          <div className="flex h-full items-center justify-center"><SectionError error={error} onRetry={onRetry} compact /></div>
        ) : !data?.data_start_date ? (
          <div className="flex h-full items-center justify-center text-sm text-fg-subtle">暂无用量记录</div>
        ) : view === 'daily' ? (
          <div className="flex h-full flex-col">
            <div className="mx-auto" style={{ width: layout.gridWidth }}>
              <div
                role="group"
                aria-label="每日用量，方向键切换日期"
                className="grid gap-[3px]"
                style={{ gridTemplateColumns: `repeat(${layout.visibleWeeks}, ${layout.cellSize}px)` }}
                onKeyDown={moveFocus}
              >
                {visibleWeeks.map((week) => (
                  <div key={week.date} className="grid gap-[3px]" style={{ gridTemplateRows: `repeat(7, ${layout.cellSize}px)` }}>
                    {week.days.map((day) => {
                      const Tag = day.filters ? 'button' : 'span';
                      return (
                        <Tag
                          key={day.date}
                          type={day.filters ? 'button' : undefined}
                          role={day.filters ? undefined : 'img'}
                          tabIndex={day.date === tabDate ? 0 : -1}
                          aria-label={day.ariaLabel}
                          aria-describedby={hover?.key === day.date ? tooltipId : undefined}
                          data-date={day.date}
                          data-state={day.state}
                          data-level={day.level}
                          className={`block rounded-[2px] outline-none hover:ring-1 hover:ring-accent-ring focus-visible:ring-1 focus-visible:ring-accent-ring ${day.filters ? 'cursor-pointer' : ''} ${day.state === 'no-record' ? 'border border-dashed border-line' : ''}`}
                          style={{ gridRow: day.row + 1, backgroundColor: day.state === 'no-record' ? 'transparent' : heatmapCellBackground(day.level) }}
                          onClick={day.filters ? () => onOpenRequests?.(day.filters) : undefined}
                          onMouseEnter={(event) => showDayTooltip(event, day)}
                          onFocus={(event) => { setFocusDate(day.date); showDayTooltip(event, day); }}
                          onMouseLeave={() => setHover(null)}
                          onBlur={() => setHover(null)}
                        />
                      );
                    })}
                  </div>
                ))}
              </div>
              <div className="relative mt-1 h-[20px] text-[10px] text-fg-muted">
                {monthLabels.map((label) => <span key={label.date} className="absolute whitespace-nowrap" style={{ left: label.index * (layout.cellSize + 3) }}>{label.label}</span>)}
              </div>
            </div>
            <div className="mt-auto flex items-center justify-between gap-2 text-[10px] text-fg-muted">
              <span className="min-w-0 truncate">{days.some((day) => day.state === 'no-record') ? `${data.data_start_date} 之前无记录` : ''}</span>
              <div className="flex shrink-0 items-center gap-[3px]" aria-label="用量由少到多，四档强调色">
                <span className="mr-1">少</span>
                {[0, 1, 2, 3, 4].map((level) => <span key={level} className="h-[10px] w-[10px] rounded-[2px]" style={{ backgroundColor: heatmapCellBackground(level) }} />)}
                <span className="ml-1">多</span>
              </div>
            </div>
            {hover && (
              <div ref={tooltipRef} className="pointer-events-none absolute left-0 top-0 z-10" style={{ width: Math.min(HEATMAP_TOOLTIP_WIDTH, width) }}>
                <UsageTooltip id={tooltipId} content={hover.content} chart={chart} />
              </div>
            )}
          </div>
        ) : view === 'weekly' ? (
          <ResponsiveContainer width="100%" height="100%" initialDimension={{ width, height: 176 }}>
            <BarChart data={weeks} margin={{ top: 4, right: 10, left: 0, bottom: 0 }} barCategoryGap="20%" accessibilityLayer>
              <CartesianGrid stroke={chart.grid} vertical={false} />
              <XAxis dataKey="date" ticks={allMonthLabels.map((label) => label.date)} tickFormatter={(date) => monthLabelMap.get(date) || ''} tick={{ fill: chart.axis, fontSize: 10 }} axisLine={false} tickLine={false} interval="preserveStartEnd" />
              <YAxis tickFormatter={(value) => formatHeatmapValue(value, metric)} tick={{ fill: chart.axis, fontSize: 10 }} width={64} tickCount={4} axisLine={false} tickLine={false} />
              <Tooltip filterNull={false} content={<ChartTooltip chart={chart} />} cursor={{ fill: chart.cursor }} wrapperStyle={{ pointerEvents: 'none' }} />
              <Bar dataKey="value" fill={chart.series.activity} maxBarSize={20} isAnimationActive={false} onClick={openWeek} radius={[2, 2, 0, 0]}>
                {weeks.map((week) => <Cell key={week.date} fillOpacity={week.inProgress ? .5 : 1} cursor={week.filters ? 'pointer' : 'default'} />)}
              </Bar>
            </BarChart>
          </ResponsiveContainer>
        ) : (
          <ResponsiveContainer width="100%" height="100%" initialDimension={{ width, height: 176 }}>
            <AreaChart data={cumulative} margin={{ top: 4, right: 10, left: 0, bottom: 0 }} accessibilityLayer>
              <CartesianGrid stroke={chart.grid} vertical={false} />
              <XAxis dataKey="date" ticks={allMonthLabels.map((label) => label.dayDate)} tickFormatter={(date) => cumulativeLabelMap.get(date) || ''} tick={{ fill: chart.axis, fontSize: 10 }} axisLine={false} tickLine={false} interval="preserveStartEnd" />
              <YAxis tickFormatter={(value) => formatHeatmapValue(value, metric)} tick={{ fill: chart.axis, fontSize: 10 }} width={64} tickCount={4} axisLine={false} tickLine={false} />
              <Tooltip filterNull={false} content={<ChartTooltip chart={chart} />} cursor={{ stroke: chart.axis }} wrapperStyle={{ pointerEvents: 'none' }} />
              <Area dataKey="value" type="linear" stroke={chart.series.activity} fill={chart.series.activity} fillOpacity={.15} strokeWidth={1.5} dot={false} activeDot={false} connectNulls={false} isAnimationActive={false} />
            </AreaChart>
          </ResponsiveContainer>
        )}
      </div>
    </Panel>
  );
};

export default UsageHeatmap;
