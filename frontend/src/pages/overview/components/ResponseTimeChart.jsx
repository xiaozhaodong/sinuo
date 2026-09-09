// ============================================
// 响应时间图表组件
// 2025-11-28
// 2026-09-09 概览改版：随详细性能区展开时加载，移除 30 秒定时器与 chartUpdate 监听
// ============================================

import { useState, useEffect, useCallback } from 'react';
import { RefreshCw, Clock } from 'lucide-react';
import {
  AreaChart,
  Area,
  XAxis,
  YAxis,
  CartesianGrid,
  Tooltip,
  ResponsiveContainer
} from 'recharts';
import { fetchResponseTimeData } from '@utils/api.js';
import { CustomSelect } from '@components/ui';
import { useTimezone } from '@contexts/TimezoneContext.jsx';
import useChartTheme from '@hooks/useChartTheme.js';
import SectionError from './SectionError.jsx';

// 时间范围选项
const TIME_RANGE_OPTIONS = [
  { value: 15, label: '15 分钟' },
  { value: 30, label: '30 分钟' },
  { value: 60, label: '1 小时' }
];

// 自定义 Tooltip
const CustomTooltip = ({ active, payload, label }) => {
  if (!active || !payload || !payload.length) return null;

  return (
    <div className="bg-surface p-3 rounded-lg shadow-lg border border-line-soft text-sm">
      <p className="font-medium text-fg mb-2">{label}</p>
      {payload.map((entry, index) => (
        <div key={index} className="flex items-center justify-between gap-4">
          <span className="text-fg-muted">{entry.name}:</span>
          <span className="font-mono font-medium" style={{ color: entry.color }}>
            {entry.value}ms
          </span>
        </div>
      ))}
    </div>
  );
};

/**
 * @param {boolean} active - 详细性能区是否展开；折叠时不发起请求
 * @param {number} refreshKey - 顶部刷新按钮的信号，变化时重取
 */
const ResponseTimeChart = ({ active = false, refreshKey = 0 }) => {
  const { formatTimeOnly } = useTimezone();
  const chart = useChartTheme();
  const [chartData, setChartData] = useState([]);
  const [isRefreshing, setIsRefreshing] = useState(false);
  const [error, setError] = useState(null);
  const [timeRange, setTimeRange] = useState(30);

  const loadData = useCallback(async (showRefreshing = false) => {
    if (showRefreshing) setIsRefreshing(true);
    try {
      const data = await fetchResponseTimeData(timeRange);
      setChartData(data.map((point) => ({ ...point, time: formatTimeOnly(point.timestamp || point.time) })));
      setError(null);
    } catch (loadError) {
      setError(loadError);
    } finally {
      setIsRefreshing(false);
    }
  }, [formatTimeOnly, timeRange]);

  // 首次展开加载，之后由刷新按钮与时间范围变化触发
  useEffect(() => {
    if (!active) return;
    loadData(chartData.length > 0);
  }, [active, refreshKey, loadData]); // eslint-disable-line react-hooks/exhaustive-deps

  const showPlaceholder = isRefreshing && chartData.length === 0;

  // 计算平均响应时间
  const avgResponseTime = chartData.length > 0
    ? Math.round(chartData.reduce((sum, d) => sum + (d.avg || 0), 0) / chartData.length)
    : 0;

  return (
    <div className="bg-surface-sub p-5 rounded-xl border border-line-soft flex flex-col h-full">
      <div className="flex justify-between items-start mb-1">
        <div className="flex items-center space-x-2">
          {/* tone-cyan 给底色，inline color 覆到 series 色（tone 在 components 层，inline 优先） */}
          <div className="p-1.5 tone-cyan rounded-md" style={{ color: chart.series.avg }}>
            <Clock size={16} />
          </div>
          <h3 className="font-semibold text-fg text-sm">响应时间</h3>
        </div>
        <div className="flex items-center space-x-2">
          <CustomSelect
            options={TIME_RANGE_OPTIONS}
            value={timeRange}
            onChange={setTimeRange}
            size="xs"
          />
          <button
            type="button"
            onClick={() => loadData(true)}
            disabled={isRefreshing}
            className="p-1.5 text-fg-subtle hover:text-fg-muted hover:bg-surface-mut rounded-md transition-colors disabled:opacity-50"
            title="刷新数据"
          >
            <RefreshCw size={14} className={isRefreshing ? 'animate-spin' : ''} />
          </button>
        </div>
      </div>
      <p className="text-xs text-fg-muted mb-4">
        平均响应: <span className="font-mono font-medium" style={{ color: chart.series.avg }}>{avgResponseTime}ms</span>
      </p>

      <div className="flex-1 min-h-[200px]">
        {showPlaceholder ? (
          <div className="h-full flex items-center justify-center text-fg-subtle">
            <RefreshCw size={20} className="animate-spin mr-2" />
            加载中...
          </div>
        ) : error ? (
          <div className="h-full flex items-center justify-center">
            <SectionError error={error} onRetry={() => loadData(true)} compact />
          </div>
        ) : chartData.length === 0 ? (
          <div className="h-full flex items-center justify-center text-fg-subtle text-sm">
            暂无数据
          </div>
        ) : (
          <ResponsiveContainer width="100%" height="100%">
            <AreaChart data={chartData} margin={{ top: 10, right: 10, left: 0, bottom: 0 }}>
              <defs>
                <linearGradient id="colorAvg" x1="0" y1="0" x2="0" y2="1">
                  <stop offset="5%" stopColor={chart.series.avg} stopOpacity={0.3} />
                  <stop offset="95%" stopColor={chart.series.avg} stopOpacity={0} />
                </linearGradient>
              </defs>
              <CartesianGrid strokeDasharray="3 3" vertical={false} stroke={chart.grid} />
              <XAxis
                dataKey="time"
                axisLine={false}
                tickLine={false}
                tick={{ fill: chart.axis, fontSize: 10 }}
                interval="preserveStartEnd"
              />
              <YAxis
                axisLine={false}
                tickLine={false}
                tick={{ fill: chart.axis, fontSize: 10 }}
                tickFormatter={(v) => `${v}ms`}
                width={45}
              />
              <Tooltip content={<CustomTooltip />} />
              <Area
                type="monotone"
                dataKey="max"
                stroke={chart.series.max}
                strokeWidth={1}
                fill="none"
                strokeDasharray="3 3"
                name="最大"
              />
              <Area
                type="monotone"
                dataKey="avg"
                stroke={chart.series.avg}
                strokeWidth={2}
                fill="url(#colorAvg)"
                name="平均"
              />
              <Area
                type="monotone"
                dataKey="min"
                stroke={chart.series.min}
                strokeWidth={1}
                fill="none"
                strokeDasharray="3 3"
                name="最小"
              />
            </AreaChart>
          </ResponsiveContainer>
        )}
      </div>

      {/* 图例：色块用 inline style 引色板，保证与曲线严格同色 */}
      {!error && chartData.length > 0 && (
        <div className="flex justify-center space-x-6 mt-3 pt-3 border-t border-line-soft">
          <div className="flex items-center text-xs text-fg-muted">
            <span className="w-3 h-0.5 mr-2" style={{ backgroundColor: chart.series.max, borderStyle: 'dashed' }} />
            最大
          </div>
          <div className="flex items-center text-xs text-fg-muted">
            <span className="w-3 h-0.5 mr-2" style={{ backgroundColor: chart.series.avg }} />
            平均
          </div>
          <div className="flex items-center text-xs text-fg-muted">
            <span className="w-3 h-0.5 mr-2" style={{ backgroundColor: chart.series.min, borderStyle: 'dashed' }} />
            最小
          </div>
        </div>
      )}
    </div>
  );
};

export default ResponseTimeChart;
