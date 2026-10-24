// ============================================
// 概览页数据快照 Hook
// 2026-09-09 概览改版：概览是快照页，不再是实时页。
// 进入页面加载一次，顶部刷新按钮统一重取；不订阅 SSE，也没有组件级定时器。
// 实时请求流由请求追踪页的 request:update 负责，全局 status/notification
// SSE 仍在 App.jsx 中维护（Header 代理状态灯与全局通知）。
// 2026-10-24 时段统计分区：KPI 组与时段组分开加载。切换时间范围只重取时段组，
// 重试只重取所属组；顶部刷新两组一起重取。性能详情的刷新信号改由页面持有。
// ============================================

import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import {
  fetchEndpointCostsData,
  fetchModelUsageStats,
  fetchRequestTrendData,
  fetchUsageStats
} from '@utils/api.js';
import { useTimezone } from '@contexts/TimezoneContext.jsx';
import { buildOverviewQuery } from '@pages/overview/utils/overviewViewModel.js';
import {
  KPI_GROUP_FALLBACKS,
  KPI_GROUP_KEYS,
  PERIOD_GROUP_FALLBACKS,
  PERIOD_GROUP_KEYS,
  isGroupClean,
  runLatest,
  settleGroup
} from './useOverviewData.helpers.js';

// 模型表取 Top 20，表内滚动查看并明确标注截断。
const MODEL_USAGE_LIMIT = 20;

/**
 * 概览页快照加载。
 * @param {Object} options
 * @param {string} options.period - 时段统计的时间范围（today/1h/1d/7d/30d）
 * @returns {{snapshot: Object, errors: Object, isInitialized: boolean,
 *   refreshing: boolean, lastUpdate: string|null, refresh: Function,
 *   retryKpis: Function, retryPeriodStats: Function,
 *   snapshotPeriod: string|null, periodPending: boolean}}
 */
const useOverviewData = ({ period = '1d' } = {}) => {
  const { formatTimeOnly, getTodayTimeRange, getRecentDaysRange } = useTimezone();

  const [kpiGroup, setKpiGroup] = useState(null);
  // 时段组连同所属时间范围一起提交：与当前 period 不一致时说明切换后还没取回新数据
  const [periodGroup, setPeriodGroup] = useState(null);
  const [periodPending, setPeriodPending] = useState(false);
  const [refreshing, setRefreshing] = useState(false);
  const [lastUpdate, setLastUpdate] = useState(null);

  const kpiVersionRef = useRef(0);
  const periodVersionRef = useRef(0);
  // 最近一次发起的时段窗口：挂载时由整页加载发起，切换时间范围的 effect 据此跳过重复请求
  const requestedPeriodRef = useRef(null);
  const periodRef = useRef(period);

  useEffect(() => {
    periodRef.current = period;
  }, [period]);

  // 今日统计与迷你趋势截止同一个时刻；昨日为完整业务日。
  const loadKpis = useCallback(async (now) => {
    const todayRange = { ...getTodayTimeRange(now), end_date: now.toISOString() };
    const twoDayRange = getRecentDaysRange(2, now);
    const yesterdayRange = { start_date: twoDayRange.start_date, end_date: todayRange.start_date };
    const outcome = await runLatest(kpiVersionRef, async () => settleGroup(KPI_GROUP_KEYS, await Promise.allSettled([
      fetchUsageStats({ ...todayRange, source_view: 'all' }),
      fetchUsageStats({ ...yesterdayRange, source_view: 'all' }),
      fetchRequestTrendData({ period: '1d', ...todayRange, source_view: 'all' })
    ]), KPI_GROUP_FALLBACKS));
    if (!outcome.current) return false;
    setKpiGroup(outcome.value);
    return isGroupClean(outcome.value);
  }, [getRecentDaysRange, getTodayTimeRange]);

  // 三个请求传同一个窗口，趋势、模型与成本的截止时刻一致。
  const loadPeriodStats = useCallback(async (targetPeriod, now) => {
    requestedPeriodRef.current = targetPeriod;
    setPeriodPending(true);
    const query = { ...buildOverviewQuery(targetPeriod, now, getTodayTimeRange(now)), source_view: 'all' };
    const outcome = await runLatest(periodVersionRef, async () => settleGroup(PERIOD_GROUP_KEYS, await Promise.allSettled([
      fetchRequestTrendData(query),
      fetchModelUsageStats({ ...query, limit: MODEL_USAGE_LIMIT }),
      fetchEndpointCostsData(query)
    ]), PERIOD_GROUP_FALLBACKS));
    if (!outcome.current) return false;
    setPeriodGroup({ period: targetPeriod, ...outcome.value });
    setPeriodPending(false);
    return isGroupClean(outcome.value);
  }, [getTodayTimeRange]);

  // 整页加载（挂载与顶部刷新）：两组共用同一个时刻。两组都成功、且没有被切换或重试取代，
  // 才更新快照时间；局部失败时保留上一次的成功时间，不谎报数据新鲜度。
  const loadAll = useCallback(async (targetPeriod) => {
    const now = new Date();
    const [kpiClean, periodClean] = await Promise.all([loadKpis(now), loadPeriodStats(targetPeriod, now)]);
    if (kpiClean && periodClean) setLastUpdate(formatTimeOnly(new Date()));
  }, [formatTimeOnly, loadKpis, loadPeriodStats]);

  useEffect(() => {
    loadAll(periodRef.current);
    return () => {
      kpiVersionRef.current += 1;
      periodVersionRef.current += 1;
    };
  }, [loadAll]);

  // 挂载时整页加载已发起同一窗口；之后切换时间范围只重取时段组。
  useEffect(() => {
    if (requestedPeriodRef.current === period) return;
    loadPeriodStats(period, new Date());
  }, [loadPeriodStats, period]);

  // 手动刷新：保留当前内容，只在页头显示刷新态
  const refresh = useCallback(async () => {
    setRefreshing(true);
    try {
      await loadAll(period);
    } finally {
      setRefreshing(false);
    }
  }, [loadAll, period]);

  const retryKpis = useCallback(() => loadKpis(new Date()), [loadKpis]);
  const retryPeriodStats = useCallback(() => loadPeriodStats(period, new Date()), [loadPeriodStats, period]);

  const snapshot = useMemo(() => ({
    ...KPI_GROUP_FALLBACKS,
    ...PERIOD_GROUP_FALLBACKS,
    ...kpiGroup?.data,
    ...periodGroup?.data
  }), [kpiGroup, periodGroup]);
  const errors = useMemo(() => ({ ...kpiGroup?.errors, ...periodGroup?.errors }), [kpiGroup, periodGroup]);

  return {
    snapshot,
    errors,
    isInitialized: kpiGroup !== null && periodGroup !== null,
    refreshing,
    lastUpdate,
    refresh,
    retryKpis,
    retryPeriodStats,
    snapshotPeriod: periodGroup?.period ?? null,
    periodPending
  };
};

export default useOverviewData;
