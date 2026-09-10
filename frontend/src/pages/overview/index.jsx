// ============================================
// Overview 页面 - 健康状态与模型调用分析工作台
// 2025-11-28
// 2026-09-09 改版：快照式概览（进入加载一次 + 顶部统一刷新），
// 4 个今日 KPI、请求趋势、模型调用表、上游成本与 Token 分布、
// 折叠的响应时间与连接活动；不再渲染端点健康环形图
// ============================================

import { useCallback, useState } from 'react';
import { AlertCircle, RefreshCw } from 'lucide-react';
import { LoadingSpinner } from '@components/ui';
import useOverviewData from '@hooks/useOverviewData.js';
import { resolveErrorMessage } from '@utils/errors.js';
import { setPendingRequestsFilters } from '@utils/overviewHandoff.js';

// 子组件
import KPICardsGrid from './components/KPICardsGrid.jsx';
import RequestTrendChart from './components/RequestTrendChart.jsx';
import ModelUsageTable from './components/ModelUsageTable.jsx';
import UpstreamCostRanking from './components/UpstreamCostRanking.jsx';
import TokenDistributionChart from './components/TokenDistributionChart.jsx';
import PerformanceDetailsPanel from './components/PerformanceDetailsPanel.jsx';
import { DEFAULT_OVERVIEW_PERIOD, buildRequestsRangeFromSnapshot, periodCaption } from './utils/overviewViewModel.js';

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
    refreshKey,
    snapshotPeriod
  } = useOverviewData({ period });

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

  // 时间范围切换后，旧快照仍属于上一个窗口：这段时间给主网格显示加载态，
  // 而不是让用户对着「近 1 小时」的标题看「近 24 小时」的数字。
  const sectionLoading = snapshotPeriod !== null && snapshotPeriod !== period;
  const caption = periodCaption(period);

  return (
    <div className="animate-fade-in space-y-6">
      {/* 页头 */}
      <div className="flex flex-col gap-4 sm:flex-row sm:items-end sm:justify-between">
        <div>
          <h1 className="text-2xl font-bold text-fg">概览</h1>
          <p className="text-fg-muted text-sm mt-1">系统健康状态与请求流量，先看异常，再看趋势。</p>
        </div>

        <div className="flex flex-wrap items-center gap-3">
          <span className="font-mono text-xs text-fg-subtle">
            最后更新 {lastUpdate || '—'}
          </span>

          <button
            type="button"
            onClick={refresh}
            disabled={refreshing}
            className="inline-flex items-center gap-1.5 rounded-lg bg-inverted px-3.5 py-1.5 text-xs font-semibold text-fg-inverted shadow-sm transition-opacity hover:opacity-90 disabled:opacity-60"
          >
            <RefreshCw size={14} className={refreshing ? 'animate-spin' : ''} />
            {refreshing ? '刷新中' : '刷新数据'}
          </button>
        </div>
      </div>

      {/* 今日统计局部失败：只降级提示，不把整页变成错误页 */}
      {errors.today && (
        <div className="flex flex-wrap items-center gap-3 rounded-xl border border-danger-line bg-danger-soft px-4 py-3 text-xs text-danger">
          <AlertCircle size={14} />
          <span>今日统计加载失败：{resolveErrorMessage(errors.today, '未知错误')}</span>
          <button
            type="button"
            onClick={refresh}
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
        onRetry={refresh}
      />

      {/* 二级信息：请求趋势 + 模型调用情况 */}
      <div className="grid grid-cols-1 gap-6 xl:grid-cols-[minmax(0,1.4fr)_minmax(420px,1fr)]">
        <RequestTrendChart
          data={snapshot.trend}
          period={period}
          onPeriodChange={setPeriod}
          loading={sectionLoading}
          error={errors.trend}
          onRetry={refresh}
        />
        <ModelUsageTable
          modelUsage={snapshot.modelUsage}
          periodCaption={caption}
          loading={sectionLoading}
          error={errors.modelUsage}
          onRetry={refresh}
          onOpenRequests={openRequests}
        />
      </div>

      {/* 三级信息：上游成本排行 + Token 使用分布 */}
      <div className="grid grid-cols-1 gap-6 xl:grid-cols-[minmax(0,1.4fr)_minmax(420px,1fr)]">
        <UpstreamCostRanking
          data={snapshot.endpointCosts}
          loading={false}
          error={errors.endpointCosts}
          onRetry={refresh}
        />
        <TokenDistributionChart
          modelUsage={snapshot.modelUsage}
          periodCaption={caption}
          loading={sectionLoading}
          error={errors.modelUsage}
          onRetry={refresh}
        />
      </div>

      {/* 按需信息：响应时间与连接活动（默认折叠，首次展开才加载） */}
      <PerformanceDetailsPanel refreshKey={refreshKey} />
    </div>
  );
};

export default OverviewPage;
