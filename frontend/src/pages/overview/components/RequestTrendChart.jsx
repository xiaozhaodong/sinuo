// ============================================
// 请求趋势图组件
// 2025-11-28
// 2026-09-09 概览改版：改为受控组件（数据由概览快照统一加载），
// 移除组件内 30 秒定时器与 chartUpdate 监听；
// 数据源换成 request_logs 时间分桶（GetRequestTrendData），不再是进程内累计计数器
// 2026-10-07 桌面化：改为面板样式，时间范围选择移到窗口工具栏
// 2026-10-08 图表区随同行的模型调用表等高，不再在汇总行下留白
// ============================================

import { RefreshCw } from 'lucide-react';
import Panel from '@components/desktop/Panel.jsx';
import {
  BarChart,
  Bar,
  XAxis,
  YAxis,
  CartesianGrid,
  Tooltip,
  ResponsiveContainer
} from 'recharts';
import { useTimezone } from '@contexts/TimezoneContext.jsx';
import useChartTheme from '@hooks/useChartTheme.js';
import SectionError from './SectionError.jsx';
import {
  formatCount,
  buildTrendSummary,
  formatTokenCount,
  formatTrendBucketLabel,
  periodCaption,
  withTrendOther
} from '../utils/overviewViewModel.js';

const MONO_FONT = 'ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, "Liberation Mono", "Courier New", monospace';

const TrendTooltip = ({ active, payload, label, chart }) => {
  if (!active || !payload?.length) return null;

  const point = payload[0]?.payload || {};
  const values = [
    { label: '总请求', value: point.total, className: 'text-fg-body' },
    { label: '成功', value: point.success, className: 'text-success' },
    { label: '失败', value: point.fail, className: 'text-danger' }
  ];
  if ((Number(point.other) || 0) > 0) values.push({ label: '其他', value: point.other, className: 'text-fg-muted' });

  return (
    <div className="rounded-lg px-2.5 py-2" style={chart.tooltip.contentStyle}>
      <div className="mb-1 text-xs" style={chart.tooltip.labelStyle}>{label || point.time}</div>
      <div className="space-y-0.5">
        {values.map((item) => (
          <div key={item.label} className="flex items-center justify-between gap-5 text-xs">
            <span className={item.className}>{item.label}</span>
            <span className="font-mono tabular-nums text-fg-body">{formatCount(item.value)}</span>
          </div>
        ))}
      </div>
    </div>
  );
};

// 时间范围由概览页持有、放在时段统计分区标题，这里只按 period 显示口径
const RequestTrendChart = ({ data, period, loading, error, onRetry }) => {
  const { formatTimeOnly, formatMonthDayTime } = useTimezone();
  const chart = useChartTheme();

  const points = Array.isArray(data?.points) ? data.points : [];
  const granularity = data?.granularity || 'hour';
  const chartData = withTrendOther(points).map((point) => ({
    ...point,
    time: formatTrendBucketLabel(point.time || point.timestamp, granularity, { formatTimeOnly, formatMonthDayTime })
  }));
  const summary = buildTrendSummary(chartData);
  const caption = periodCaption(period);

  return (
    <Panel
      title="请求趋势"
      subtitle={caption}
      actions={(
        <div className="flex gap-3 text-[11.5px] text-fg-muted">
          <span className="flex items-center gap-1.5"><span className="h-2 w-2 rounded-full" style={{ backgroundColor: chart.series.total }} />成功</span>
          <span className="flex items-center gap-1.5"><span className="h-2 w-2 rounded-full" style={{ backgroundColor: chart.series.fail }} />失败</span>
          {summary.hasOther && <span className="flex items-center gap-1.5"><span className="h-2 w-2 rounded-full" style={{ backgroundColor: chart.series.trendOther }} />其他</span>}
        </div>
      )}
      bodyClassName="flex flex-col px-3 pb-2.5 pt-3"
    >
      {/* 与右侧模型表同行等高：图表区填满剩余高度、至少 180px。
          图表放在绝对定位层里，避免 SVG 的像素高度反过来撑住行高，模型变少后卡片缩不回去 */}
      <div className="relative min-h-[180px] w-full flex-1">
        <div className="absolute inset-0">
          {loading ? (
            <div className="h-full flex items-center justify-center text-fg-subtle">
              <RefreshCw size={24} className="animate-spin mr-2" />
              加载中...
            </div>
          ) : error ? (
            <div className="h-full flex items-center justify-center">
              <SectionError error={error} onRetry={onRetry} compact />
            </div>
          ) : chartData.length === 0 ? (
            <div className="h-full flex items-center justify-center text-fg-subtle">
              暂无请求数据
            </div>
          ) : (
            <ResponsiveContainer width="100%" height="100%">
              <BarChart data={chartData} margin={{ top: 8, right: 8, left: 0, bottom: 0 }} barCategoryGap="20%">
                <CartesianGrid vertical={false} stroke={chart.grid} />
                <XAxis
                  dataKey="time"
                  axisLine={false}
                  tickLine={false}
                  tick={{ fill: chart.axis, fontSize: 10.5, fontFamily: MONO_FONT }}
                  dy={8}
                  minTickGap={44}
                />
                <YAxis
                  axisLine={false}
                  tickLine={false}
                  tick={{ fill: chart.axis, fontSize: 10.5 }}
                  tickFormatter={formatTokenCount}
                  width={36}
                  allowDecimals={false}
                  tickCount={4}
                />
                <Tooltip content={<TrendTooltip chart={chart} />} cursor={{ fill: chart.cursor }} />
                <Bar dataKey="success" stackId="requests" fill={chart.series.total} fillOpacity={0.75} maxBarSize={22} isAnimationActive={false} name="成功" />
                <Bar dataKey="other" stackId="requests" fill={chart.series.trendOther} fillOpacity={0.75} maxBarSize={22} isAnimationActive={false} name="其他" />
                <Bar dataKey="fail" stackId="requests" fill={chart.series.fail} fillOpacity={0.75} maxBarSize={22} radius={[2, 2, 0, 0]} isAnimationActive={false} name="失败" />
              </BarChart>
            </ResponsiveContainer>
          )}
        </div>
      </div>

      {/* 分桶口径：各桶之和就是窗口内总量 */}
      {!loading && !error && summary.hasData && (
        <div className="mt-2 flex flex-wrap items-center gap-x-5 gap-y-1 border-t border-line-soft pt-2 text-[11px] text-fg-muted">
          <span>{caption}窗口内 请求 <span className="font-mono text-fg-body">{summary.requestsText}</span></span>
          <span>成功 <span className="font-mono text-success">{summary.successText}</span></span>
          <span>失败 <span className="font-mono text-danger">{summary.failedText}</span></span>
          {summary.hasOther && <span>其他 <span className="font-mono text-fg-muted">{summary.otherText}</span></span>}
        </div>
      )}
    </Panel>
  );
};

export default RequestTrendChart;
