// ============================================
// 请求趋势图组件
// 2025-11-28
// 2026-09-09 概览改版：改为受控组件（数据由概览快照统一加载），
// 移除组件内 30 秒定时器与 chartUpdate 监听；
// 数据源换成 request_logs 时间分桶（GetRequestTrendData），不再是进程内累计计数器
// ============================================

import { Activity, RefreshCw } from 'lucide-react';
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
  OVERVIEW_PERIODS,
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

const RequestTrendChart = ({ data, period, onPeriodChange, loading, error, onRetry }) => {
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
    <div className="bg-surface p-6 rounded-2xl border border-line/60 shadow-sm flex flex-col">
      <div className="flex flex-col gap-3 mb-5 lg:flex-row lg:items-center lg:justify-between">
        <div className="flex items-center space-x-2">
          <div className="p-1.5 bg-accent-soft text-accent rounded-md">
            <Activity size={18} />
          </div>
          <div>
            <h3 className="font-semibold text-fg">请求趋势</h3>
            <p className="text-xs text-fg-muted mt-0.5">
              {caption} · {GRANULARITY_LABELS[granularity] || '按时间聚合'}
            </p>
          </div>
        </div>

        <div className="flex flex-wrap items-center gap-3">
          {/* 时间范围由页面统一持有，趋势图与模型表共用同一个范围 */}
          <div className="inline-flex gap-0.5 rounded-lg bg-surface-mut p-1" role="group" aria-label="时间范围">
            {OVERVIEW_PERIODS.map((option) => {
              const active = option.value === period;
              return (
                <button
                  key={option.value}
                  type="button"
                  onClick={() => onPeriodChange?.(option.value)}
                  aria-pressed={active}
                  className={`rounded-md px-2.5 py-1 text-xs font-medium transition-colors ${
                    active ? 'bg-surface text-fg shadow-sm' : 'text-fg-muted hover:text-fg'
                  }`}
                >
                  {option.label}
                </button>
              );
            })}
          </div>

          <div className="flex space-x-4 text-xs font-medium">
            <div className="flex items-center text-accent">
              <span className="w-2 h-2 rounded-full mr-2" style={{ backgroundColor: chart.series.total }}></span>总请求
            </div>
            <div className="flex items-center text-success">
              <span className="w-2 h-2 rounded-full mr-2" style={{ backgroundColor: chart.series.success }}></span>成功
            </div>
            <div className="flex items-center text-danger">
              <span className="w-2 h-2 rounded-full mr-2" style={{ backgroundColor: chart.series.fail }}></span>失败
            </div>
          </div>
        </div>
      </div>

      <div className="h-[260px] w-full">
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
        <div className="flex flex-wrap items-center gap-x-5 gap-y-1 mt-4 pt-3 border-t border-line-soft text-[11px] text-fg-muted">
          <span>{caption}窗口内 请求 <span className="font-mono text-fg-body">{summary.requestsText}</span></span>
          <span>成功 <span className="font-mono text-success">{summary.successText}</span></span>
          <span>失败 <span className="font-mono text-danger">{summary.failedText}</span></span>
        </div>
      )}
    </div>
  );
};

export default RequestTrendChart;
