// ============================================
// Overview 页面 - 健康状态与模型调用分析工作台
// 2025-11-28
// 2026-09-09 改版：快照式概览（进入加载一次 + 顶部统一刷新），
// 4 个今日 KPI、请求趋势、模型调用表、上游成本与 Token 分布、
// 折叠的响应时间与连接活动；不再渲染端点健康环形图
// 2026-10-24 时段统计分区：时间切换从工具栏移到分区标题，只控制分区内四块；
// 上游成本排行跟随切换，顶部刷新整页（含用量活动与已展开的性能详情）
// ============================================

import { useCallback, useState } from 'react';
import { AlertCircle, RefreshCw } from 'lucide-react';
import { LoadingSpinner } from '@components/ui';
import { PageToolbar } from '@components/layout/AppToolbar.jsx';
import { ToolbarButton, ToolbarSpacer } from '@components/layout/ToolbarButton.jsx';
import SegmentedControl from '@components/desktop/SegmentedControl.jsx';
import useOverviewData from '@hooks/useOverviewData.js';
import { isPeriodPanelLoading } from '@hooks/useOverviewData.helpers.js';
import { resolveErrorMessage } from '@utils/errors.js';
import { setPendingRequestsFilters } from '@utils/overviewHandoff.js';

// 子组件
import KPICardsGrid from './components/KPICardsGrid.jsx';
import RequestTrendChart from './components/RequestTrendChart.jsx';
import ModelUsageTable from './components/ModelUsageTable.jsx';
import UpstreamCostRanking from './components/UpstreamCostRanking.jsx';
import TokenDistributionChart from './components/TokenDistributionChart.jsx';
import PerformanceDetailsPanel from './components/PerformanceDetailsPanel.jsx';
import UsageHeatmap from './components/UsageHeatmap.jsx';
import useUsageHeatmap from './hooks/useUsageHeatmap.js';
import { DEFAULT_OVERVIEW_PERIOD, OVERVIEW_PERIODS, buildRequestsRangeFromSnapshot, periodCaption } from './utils/overviewViewModel.js';

// ============================================
// Overview 主页面
// ============================================

const OverviewPage = ({ onNavigate }) => {
  const [period, setPeriod] = useState(DEFAULT_OVERVIEW_PERIOD);
  const {
    snapshot,
    errors,
    isInitialized,
    refreshing,
    lastUpdate,
    refresh,
    retryKpis,
    retryPeriodStats,
    snapshotPeriod,
    periodPending
  } = useOverviewData({ period });
  const { data: heatmapData, loading: heatmapLoading, error: heatmapError, reload: reloadHeatmap } = useUsageHeatmap();

  // 性能详情的刷新信号只由顶部刷新递增：切换时间范围、局部重试都不牵动它
  const [performanceRefreshKey, setPerformanceRefreshKey] = useState(0);

  const refreshAll = useCallback(() => {
    refresh();
    reloadHeatmap();
    setPerformanceRefreshKey((current) => current + 1);
  }, [refresh, reloadHeatmap]);

  const openRequestsRange = useCallback((filters) => {
    setPendingRequestsFilters(filters);
    onNavigate?.('requests');
  }, [onNavigate]);

  // 带筛选条件跳到请求追踪页（模型行 / 来源标签）。
  // 时间范围必须跟着走，否则请求页停在默认「今天」，和模型表的窗口对不上账。
  const openRequests = useCallback((filter) => {
    if (!snapshot.modelUsage || snapshotPeriod !== period || errors.modelUsage) return;
    // 口径随入口 filter 走：次数入口显式带 includeCompaction=false（仅生成），
    // 用量、成本与模型明细不带，保留独立压缩记录。
    setPendingRequestsFilters({
      ...filter,
      ...buildRequestsRangeFromSnapshot(snapshot.modelUsage)
    });
    if (onNavigate) onNavigate('requests');
  }, [onNavigate, period, snapshot.modelUsage, snapshotPeriod, errors.modelUsage]);

  if (!isInitialized) {
    return <LoadingSpinner text="加载概览数据..." />;
  }

  // 时间范围切换后，旧数据仍属于上一个窗口：这段时间给时段统计显示加载态，
  // 而不是让用户对着「近 1 小时」的标题看「近 24 小时」的数字。
  const periodPanelLoading = (error) => isPeriodPanelLoading({ period, committedPeriod: snapshotPeriod, pending: periodPending, error });
  const caption = periodCaption(period);

  return (
    <div className="motion-page flex-1 space-y-3 bg-pane p-4">
      <PageToolbar subtitle={`快照 · ${lastUpdate || '—'} 更新`}>
        <ToolbarSpacer />
        <ToolbarButton icon={RefreshCw} title="刷新数据" onClick={refreshAll} loading={refreshing} />
      </PageToolbar>

      {/* 今日统计局部失败：只降级提示，不把整页变成错误页 */}
      {errors.today && (
        <div role="alert" className="selectable flex flex-wrap items-center gap-3 rounded-lg border border-danger-line bg-danger-soft px-3 py-2 text-xs text-danger">
          <AlertCircle size={14} />
          <span>今日统计加载失败：{resolveErrorMessage(errors.today, '未知错误')}</span>
          <button
            type="button"
            onClick={retryKpis}
            className="rounded-md border border-danger-line px-2 py-0.5 font-medium hover:bg-danger-soft/70"
          >
            重试
          </button>
        </div>
      )}

      {/* 一级信息：今日请求 / 成功率 / 平均延迟 / 失败请求 */}
      <KPICardsGrid
        today={snapshot.today}
        yesterday={snapshot.yesterday}
        todayUnavailable={Boolean(errors.today)}
        comparisonUnavailable={Boolean(errors.yesterday)}
        trend={snapshot.todayTrend}
        trendUnavailable={Boolean(errors.todayTrend)}
        onRetry={retryKpis}
      />

      {/* 二级信息：近一年用量活动（固定近一年，不随时段统计的时间范围切换） */}
      <UsageHeatmap data={heatmapData} loading={heatmapLoading} error={heatmapError} onRetry={reloadHeatmap} onOpenRequests={openRequestsRange} />

      {/* 三级信息：时段统计。时间切换只控制分区内四块，标题在分区内吸顶；
          分区底边会把标题推走，不会盖到下方的性能详情 */}
      <section aria-labelledby="overview-period-title">
        <div className="sticky top-0 z-20 mb-2 flex h-8 items-center justify-between gap-3 border-b border-line-soft bg-pane">
          <h2 id="overview-period-title" className="text-[13px] font-semibold text-fg">时段统计</h2>
          <SegmentedControl options={OVERVIEW_PERIODS} value={period} onChange={setPeriod} ariaLabel="时段统计时间范围" />
        </div>
        <div className="space-y-3">
          {/* 请求趋势 + 模型调用：整行定高，模型表在面板内滚动，首屏能看到这一行 */}
          <div className="grid h-[312px] grid-cols-2 gap-3">
            <RequestTrendChart
              data={snapshot.trend}
              period={period}
              loading={periodPanelLoading(errors.trend)}
              error={errors.trend}
              onRetry={retryPeriodStats}
            />
            <ModelUsageTable
              modelUsage={snapshot.modelUsage}
              periodCaption={caption}
              loading={periodPanelLoading(errors.modelUsage)}
              error={errors.modelUsage}
              onRetry={retryPeriodStats}
              onOpenRequests={openRequests}
            />
          </div>

          {/* 上游成本排行 + Token 使用分布 */}
          <div className="grid grid-cols-2 gap-3">
            <UpstreamCostRanking
              data={snapshot.endpointCosts}
              periodCaption={caption}
              loading={periodPanelLoading(errors.endpointCosts)}
              error={errors.endpointCosts}
              onRetry={retryPeriodStats}
            />
            <TokenDistributionChart
              modelUsage={snapshot.modelUsage}
              periodCaption={caption}
              loading={periodPanelLoading(errors.modelUsage)}
              error={errors.modelUsage}
              onRetry={retryPeriodStats}
            />
          </div>
        </div>
      </section>

      {/* 按需信息：响应时间与连接活动（默认折叠，首次展开才加载；有自己的时间选择） */}
      <PerformanceDetailsPanel refreshKey={performanceRefreshKey} />
    </div>
  );
};

export default OverviewPage;
