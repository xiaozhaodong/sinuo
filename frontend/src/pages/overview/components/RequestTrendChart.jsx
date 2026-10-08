// ============================================
// 请求趋势图组件
// 2025-11-28
// 2026-09-09 概览改版：改为受控组件（数据由概览快照统一加载），
// 移除组件内 30 秒定时器与 chartUpdate 监听；
// 数据源换成 request_logs 时间分桶（GetRequestTrendData），不再是进程内累计计数器
// 2026-10-07 桌面化：改为面板样式，时间范围选择移到窗口工具栏
// ============================================

import { RefreshCw } from 'lucide-react';
import Panel from '@components/desktop/Panel.jsx';
import {
  AreaChart,
  Area,
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
  buildTrendSummary,
  formatTokenCount,
  formatTrendBucketLabel,
  periodCaption
} from '../utils/overviewViewModel.js';

const GRANULARITY_LABELS = {
  minute: '5 分钟聚合',
  hour: '按小时聚合',
  day: '按天聚合'
};

// 时间范围由概览页统一持有并放在工具栏，这里只按 period 显示口径
const RequestTrendChart = ({ data, period, loading, error, onRetry }) => {
  const { formatTimeOnly, formatMonthDayTime } = useTimezone();
  const chart = useChartTheme();

  const points = Array.isArray(data?.points) ? data.points : [];
  const granularity = data?.granularity || 'hour';
  const chartData = points.map((point) => ({
    ...point,
    time: formatTrendBucketLabel(point.time || point.timestamp, granularity, { formatTimeOnly, formatMonthDayTime })
  }));
  const summary = buildTrendSummary(chartData);
  const caption = periodCaption(period);

  return (
    <Panel
      title="请求趋势"
      subtitle={`${caption} · ${GRANULARITY_LABELS[granularity] || '按时间聚合'}`}
      actions={(
        <div className="flex gap-3 text-[11.5px] text-fg-muted">
          <span className="flex items-center gap-1.5"><span className="h-2 w-2 rounded-full" style={{ backgroundColor: chart.series.total }} />总请求</span>
          <span className="flex items-center gap-1.5"><span className="h-2 w-2 rounded-full" style={{ backgroundColor: chart.series.success }} />成功</span>
          <span className="flex items-center gap-1.5"><span className="h-2 w-2 rounded-full" style={{ backgroundColor: chart.series.fail }} />失败</span>
        </div>
      )}
      bodyClassName="flex flex-col px-3 pb-2.5 pt-3"
    >
      <div className="h-[220px] w-full">
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
            <AreaChart data={chartData} margin={{ top: 10, right: 10, left: 0, bottom: 0 }}>
              <defs>
                <linearGradient id="colorTotal" x1="0" y1="0" x2="0" y2="1">
                  <stop offset="5%" stopColor={chart.series.total} stopOpacity={0.1} />
                  <stop offset="95%" stopColor={chart.series.total} stopOpacity={0} />
                </linearGradient>
              </defs>
              <CartesianGrid strokeDasharray="3 3" vertical={false} stroke={chart.grid} />
              <XAxis dataKey="time" axisLine={false} tickLine={false} tick={{ fill: chart.axis, fontSize: 12 }} dy={10} minTickGap={24} />
              <YAxis
                axisLine={false}
                tickLine={false}
                tick={{ fill: chart.axis, fontSize: 12 }}
                tickFormatter={formatTokenCount}
                width={48}
              />
              <Tooltip {...chart.tooltip} />
              <Area type="monotone" dataKey="total" stroke={chart.series.total} strokeWidth={2} fillOpacity={1} fill="url(#colorTotal)" name="总请求" />
              <Area type="monotone" dataKey="success" stroke={chart.series.success} strokeWidth={2} fillOpacity={0} fill="transparent" name="成功" />
              <Area type="monotone" dataKey="fail" stroke={chart.series.fail} strokeWidth={2} fillOpacity={0} fill="transparent" name="失败" />
            </AreaChart>
          </ResponsiveContainer>
        )}
      </div>

      {/* 分桶口径：各桶之和就是窗口内总量 */}
      {!loading && !error && summary.hasData && (
        <div className="mt-2 flex flex-wrap items-center gap-x-5 gap-y-1 border-t border-line-soft pt-2 text-[11px] text-fg-muted">
          <span>{caption}窗口内 请求 <span className="font-mono text-fg-body">{summary.requestsText}</span></span>
          <span>成功 <span className="font-mono text-success">{summary.successText}</span></span>
          <span>失败 <span className="font-mono text-danger">{summary.failedText}</span></span>
        </div>
      )}
    </Panel>
  );
};

export default RequestTrendChart;
