// 今日指标与按小时聚合的迷你趋势，数据仅随概览快照刷新。
import { AlertTriangle, CheckCircle2, Clock, Network } from 'lucide-react';
import { LineChart, Line, ResponsiveContainer, YAxis } from 'recharts';
import useChartTheme from '@hooks/useChartTheme.js';
import { buildKpiCards, buildKpiTrends } from '../utils/overviewViewModel.js';

const CARD_ICONS = {
  requests: Network,
  'success-rate': CheckCircle2,
  latency: Clock,
  failed: AlertTriangle
};

const SERIES_KEYS = { requests: 'total', 'success-rate': 'success', latency: 'avg', failed: 'fail' };
const DELTA_TONE_CLASSES = { success: 'text-success', danger: 'text-danger', neutral: 'text-fg-muted' };

const KPICardsGrid = ({ today, yesterday, comparisonUnavailable = false, trend, trendUnavailable = false, onRetry }) => {
  const cards = buildKpiCards(today, yesterday, { comparisonUnavailable });
  const trends = buildKpiTrends(trend);
  const chart = useChartTheme();

  return (
    <section aria-label="今日核心指标" className="grid grid-cols-1 min-[420px]:grid-cols-2 xl:grid-cols-4 gap-4">
      {cards.map((card) => {
        const Icon = CARD_ICONS[card.key];
        const { comparison } = card;
        const points = trends[card.key];
        const hasTrend = points.filter((point) => Number.isFinite(point.value)).length >= 2;
        const color = chart.series[SERIES_KEYS[card.key]];
        return (
          <article key={card.key} className="relative isolate overflow-hidden rounded-2xl border border-line/60 bg-surface p-5 shadow-sm transition-shadow hover:shadow-md">
            <div aria-hidden="true" className="pointer-events-none absolute -bottom-14 -right-7 -z-10 h-32 w-32 rounded-full opacity-[0.07]" style={{ backgroundColor: color }} />
            <div className="flex items-center gap-2 text-fg-muted">
              <span className={`rounded-md p-1.5 ${card.tone}`}><Icon size={15} /></span>
              <h3 className="text-xs font-semibold tracking-wide">{card.title}</h3>
            </div>
            <div className="mt-3 flex items-center justify-between gap-3">
              <div className="min-w-0 text-[28px] leading-tight font-semibold tracking-tight text-fg tabular-nums truncate" title={card.value}>{card.value}</div>
              {trendUnavailable ? (
                <button type="button" onClick={onRetry} className="shrink-0 text-[11px] text-danger hover:underline">趋势重试</button>
              ) : hasTrend ? (
                <div className="h-10 w-24 shrink-0" role="img" aria-label={`${card.title}：今日按小时趋势`} title="今日按小时趋势 · 随快照更新">
                  <ResponsiveContainer width="100%" height="100%">
                    <LineChart data={points} margin={{ top: 4, right: 2, bottom: 4, left: 2 }} accessibilityLayer={false}>
                      <YAxis hide domain={card.key === 'success-rate' ? [0, 100] : ['auto', 'auto']} />
                      <Line type="linear" dataKey="value" stroke={color} strokeWidth={2} dot={false} connectNulls={false} isAnimationActive={false} />
                    </LineChart>
                  </ResponsiveContainer>
                </div>
              ) : (
                <span className="text-[11px] text-fg-subtle">暂无趋势</span>
              )}
            </div>
            <div className="mt-3 flex flex-wrap items-center gap-x-1.5 gap-y-1 text-[11px] font-medium text-fg-muted">
              {comparison.deltaText && <span className={DELTA_TONE_CLASSES[comparison.tone]}>{comparison.deltaText}</span>}
              <span>{comparison.label}</span>
              {card.attention && <span className={`ml-auto ${Number(today?.failed_requests) > 0 ? 'text-danger' : 'text-fg-muted'}`}>{card.attention}</span>}
            </div>
          </article>
        );
      })}
    </section>
  );
};

export default KPICardsGrid;
