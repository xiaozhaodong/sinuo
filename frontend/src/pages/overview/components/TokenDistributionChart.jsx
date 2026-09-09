// ============================================
// Token 使用分布（概览页次级网格右侧）
// 2025-11-28
// 2026-09-09 概览改版：改为受控组件，数据取模型聚合接口的四类 Token 汇总
// （与模型表同一时间范围），不再使用进程内累计值，也没有定时器与 chartUpdate 监听
// ============================================

import { PieChart as PieChartIcon } from 'lucide-react';
import {
  PieChart,
  Pie,
  Cell,
  Tooltip,
  ResponsiveContainer
} from 'recharts';
import useChartTheme from '@hooks/useChartTheme.js';
import SectionError from './SectionError.jsx';
import { buildTokenBreakdown } from '../utils/overviewViewModel.js';

// 自定义 Tooltip
const CustomTooltip = ({ active, payload }) => {
  if (!active || !payload || !payload.length) return null;

  const data = payload[0].payload;
  return (
    <div className="bg-surface p-3 rounded-lg shadow-lg border border-line-soft text-sm">
      <div className="flex items-center gap-2 mb-1">
        <span className="w-3 h-3 rounded-full" style={{ backgroundColor: data.color }} />
        <span className="font-medium text-fg">{data.name}</span>
      </div>
      <div className="text-fg-body">
        <span className="font-mono">{data.valueText}</span>
        <span className="text-fg-subtle ml-2">({data.percent.toFixed(1)}%)</span>
      </div>
    </div>
  );
};

const TokenDistributionChart = ({ modelUsage, periodCaption = '', loading, error, onRetry }) => {
  const chart = useChartTheme();
  const breakdown = buildTokenBreakdown(modelUsage);
  const chartData = breakdown.items
    .map((item) => ({ ...item, color: chart.series[item.key] }))
    .filter((item) => item.value > 0);

  return (
    <div className="bg-surface p-6 rounded-2xl border border-line/60 shadow-sm flex flex-col h-full">
      <div className="flex justify-between items-start mb-1">
        <div className="flex items-center space-x-2">
          <div className="p-1.5 bg-accent-soft text-accent-ring rounded-md">
            <PieChartIcon size={16} />
          </div>
          <h3 className="font-semibold text-fg">Token 使用分布</h3>
        </div>
        {periodCaption && <span className="text-[11px] text-fg-subtle">{periodCaption}</span>}
      </div>
      <p className="text-xs text-fg-muted mb-4">各类 Token 消耗占比</p>

      <div className="flex-1 min-h-[180px] flex items-center justify-center relative overflow-visible">
        {loading ? (
          <div className="h-32 w-32 animate-pulse rounded-full bg-surface-mut" />
        ) : error ? (
          <SectionError error={error} onRetry={onRetry} compact />
        ) : !breakdown.hasData ? (
          <div className="text-fg-subtle text-sm">暂无 Token 数据</div>
        ) : (
          <>
            <ResponsiveContainer width="100%" height="100%">
              <PieChart>
                <Pie
                  data={chartData}
                  innerRadius={50}
                  outerRadius={75}
                  paddingAngle={3}
                  dataKey="value"
                  animationBegin={0}
                  animationDuration={500}
                >
                  {chartData.map((entry, index) => (
                    <Cell
                      key={`cell-${index}`}
                      fill={entry.color}
                      stroke="none"
                      className="transition-opacity hover:opacity-80"
                    />
                  ))}
                </Pie>
                <Tooltip
                  content={<CustomTooltip />}
                  wrapperStyle={{ zIndex: 1000, pointerEvents: 'none' }}
                />
              </PieChart>
            </ResponsiveContainer>
            <div className="absolute inset-0 flex flex-col items-center justify-center pointer-events-none">
              <span className="text-2xl font-bold text-fg-body">{breakdown.totalText}</span>
              <span className="text-xs text-fg-subtle">总计</span>
            </div>
          </>
        )}
      </div>

      {/* 图例 */}
      {!loading && !error && breakdown.hasData && (
        <div className="grid grid-cols-2 gap-x-4 gap-y-2 mt-4 pt-3 border-t border-line-soft">
          {breakdown.items.map((item) => (
            <div key={item.key} className="flex items-center justify-between text-xs">
              <div className="flex items-center text-fg-body">
                <span
                  className="w-2.5 h-2.5 rounded-full mr-2 flex-shrink-0"
                  style={{ backgroundColor: chart.series[item.key] }}
                />
                <span className="truncate">{item.name}</span>
              </div>
              <span className="font-mono text-fg-muted ml-2">{item.percentText}</span>
            </div>
          ))}
        </div>
      )}
    </div>
  );
};

export default TokenDistributionChart;
