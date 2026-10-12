// ============================================
// Token 使用分布（概览页次级网格右侧）
// 2025-11-28
// 2026-09-09 概览改版：改为受控组件，数据取模型聚合接口的四类 Token 汇总
// （与模型表同一时间范围），不再使用进程内累计值，也没有定时器与 chartUpdate 监听
// 2026-10-07 桌面化：面板样式
// ============================================

import Panel from '@components/desktop/Panel.jsx';
import useChartTheme from '@hooks/useChartTheme.js';
import SectionError from './SectionError.jsx';
import { buildTokenBreakdown } from '../utils/overviewViewModel.js';

const TokenDistributionChart = ({ modelUsage, periodCaption = '', loading, error, onRetry }) => {
  const chart = useChartTheme();
  const breakdown = buildTokenBreakdown(modelUsage);
  const chartData = breakdown.items.filter((item) => item.value > 0);
  const subtitle = !loading && !error && breakdown.hasData
    ? `${periodCaption ? `${periodCaption} · ` : ''}共 ${breakdown.totalText}`
    : periodCaption;

  return (
    <Panel
      title="Token 分布"
      subtitle={subtitle}
      className="h-full"
      bodyClassName="flex flex-col px-3 py-3"
    >
      {loading ? (
        <div className="min-h-[180px] pt-2">
          <div className="h-2.5 w-full animate-pulse rounded-full bg-surface-mut" />
        </div>
      ) : error ? (
        <div className="flex min-h-[180px] items-center justify-center">
          <SectionError error={error} onRetry={onRetry} compact />
        </div>
      ) : !breakdown.hasData ? (
        <div className="flex min-h-[180px] items-center justify-center text-sm text-fg-subtle">暂无 Token 数据</div>
      ) : (
        <>
          <div className="flex h-2.5 w-full gap-0.5 overflow-hidden rounded-full" aria-label="Token 分布条">
            {chartData.map((item) => (
              <span
                key={item.key}
                className="block min-w-0"
                style={{ flex: `${item.value} 1 0%`, backgroundColor: chart.series[item.key] }}
                title={`${item.name} ${item.valueText}（${item.percentText}）`}
              />
            ))}
          </div>

          <div className="mt-3 grid grid-cols-2 gap-x-4 gap-y-2 border-t border-line-soft pt-2.5">
            {breakdown.items.map((item) => (
              <div key={item.key} className="flex min-w-0 items-center gap-1.5 text-xs">
                <span className="h-2 w-2 shrink-0 rounded-full" style={{ backgroundColor: chart.series[item.key] }} />
                <span className="min-w-0 truncate text-fg-body">{item.name}</span>
                <span className="ml-auto shrink-0 font-mono tabular-nums text-fg-body">{item.valueText}</span>
                <span className="w-8 shrink-0 text-right text-fg-muted">{item.percentText}</span>
              </div>
            ))}
          </div>
        </>
      )}
    </Panel>
  );
};

export default TokenDistributionChart;
