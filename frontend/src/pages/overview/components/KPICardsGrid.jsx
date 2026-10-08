// 今日指标与按小时聚合的迷你趋势，数据仅随概览快照刷新。
// 2026-10-07 桌面化：4 张浮起的卡片改为一条分格摘要条（细边框、无阴影、无装饰色块），数字 22px。
import { LineChart, Line, ResponsiveContainer, YAxis } from 'recharts';
import useChartTheme from '@hooks/useChartTheme.js';
import { buildKpiCards, buildKpiTrends } from '../utils/overviewViewModel.js';

const SERIES_KEYS = { requests: 'total', 'success-rate': 'success', latency: 'avg', failed: 'fail' };
const DELTA_TONE_CLASSES = { success: 'text-success', danger: 'text-danger', neutral: 'text-fg-muted' };

const KPICardsGrid = ({ today, yesterday, todayUnavailable = false, comparisonUnavailable = false, trend, trendUnavailable = false, onRetry }) => {
  const cards = buildKpiCards(today, yesterday, { comparisonUnavailable, todayUnavailable });
  const trends = buildKpiTrends(trend);
  const chart = useChartTheme();

  return (
    <section aria-label="今日核心指标" className="grid grid-cols-4 divide-x divide-line-soft overflow-hidden rounded-lg border border-line bg-surface">
      {cards.map((card) => {
        const { comparison } = card;
        const points = trends[card.key];
        const hasTrend = points.filter((point) => Number.isFinite(point.value)).length >= 2;
        const color = chart.series[SERIES_KEYS[card.key]];
        return (
          <article key={card.key} className="flex min-w-0 items-end gap-3 px-3.5 pb-3 pt-2.5">
            <div className="min-w-0 flex-1">
              <h3 className="text-xs text-fg-muted">{card.title}</h3>
              <div className="mt-0.5 truncate text-[22px] font-semibold leading-tight tracking-tight text-fg tabular-nums" title={card.value}>{card.value}</div>
              <div className="mt-0.5 flex min-w-0 items-center gap-1.5 truncate text-[11px] text-fg-muted">
                {comparison.deltaText && <span className={DELTA_TONE_CLASSES[comparison.tone]}>{comparison.deltaText}</span>}
                <span className="truncate">{comparison.label}</span>
                {card.attention && <span className={`ml-auto shrink-0 ${Number(today?.failed_requests) > 0 ? 'text-danger' : 'text-fg-muted'}`}>{card.attention}</span>}
              </div>
            </div>
            {trendUnavailable ? (
              <button type="button" onClick={onRetry} className="mb-1 shrink-0 text-[11px] text-danger hover:underline">趋势重试</button>
            ) : hasTrend ? (
              <div className="mb-1 h-7 w-[72px] shrink-0" role="img" aria-label={`${card.title}：今日按小时趋势`} title="今日按小时趋势 · 随快照更新">
                <ResponsiveContainer width="100%" height="100%">
                  <LineChart data={points} margin={{ top: 3, right: 1, bottom: 3, left: 1 }} accessibilityLayer={false}>
                    <YAxis hide domain={card.key === 'success-rate' ? [0, 100] : ['auto', 'auto']} />
                    <Line type="linear" dataKey="value" stroke={color} strokeWidth={1.5} dot={false} connectNulls={false} isAnimationActive={false} />
                  </LineChart>
                </ResponsiveContainer>
              </div>
            ) : (
              <span className="mb-1 shrink-0 text-[11px] text-fg-subtle">暂无趋势</span>
            )}
          </article>
        );
      })}
    </section>
  );
};

export default KPICardsGrid;
