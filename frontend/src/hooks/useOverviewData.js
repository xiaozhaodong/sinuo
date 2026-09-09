// ============================================
// 概览页数据快照 Hook
// 2026-09-09 概览改版：概览是快照页，不再是实时页。
// 进入页面加载一次，顶部刷新按钮统一重取；不订阅 SSE，也没有组件级定时器。
// 实时请求流由请求追踪页的 request:update 负责，全局 status/notification
// SSE 仍在 App.jsx 中维护（Header 代理状态灯与全局通知）。
// ============================================

import { useCallback, useEffect, useRef, useState } from 'react';
import {
  fetchEndpointCostsData,
  fetchModelUsageStats,
  fetchRequestTrendData,
  fetchUsageStats
} from '@utils/api.js';
import { useTimezone } from '@contexts/TimezoneContext.jsx';
import { buildOverviewRange } from '@pages/overview/utils/overviewViewModel.js';

// 模型表取 Top 20，表内滚动查看并明确标注截断。
const MODEL_USAGE_LIMIT = 20;

const EMPTY_TREND = { period: null, granularity: 'hour', points: [] };

const INITIAL_SNAPSHOT = {
  today: {},
  yesterday: {},
  modelUsage: null,
  endpointCosts: [],
  trend: EMPTY_TREND,
  todayTrend: EMPTY_TREND
};

const EMPTY_ERRORS = {};

const buildSnapshotFromResults = (results) => {
  const [todayResult, yesterdayResult, modelUsageResult, endpointCostsResult, trendResult, todayTrendResult] = results;

  const snapshot = { ...INITIAL_SNAPSHOT };
  const errors = {};

  if (todayResult.status === 'fulfilled') snapshot.today = todayResult.value || {};
  else errors.today = todayResult.reason;

  if (yesterdayResult.status === 'fulfilled') snapshot.yesterday = yesterdayResult.value || {};
  else errors.yesterday = yesterdayResult.reason;

  if (modelUsageResult.status === 'fulfilled') snapshot.modelUsage = modelUsageResult.value;
  else errors.modelUsage = modelUsageResult.reason;

  if (endpointCostsResult.status === 'fulfilled') snapshot.endpointCosts = endpointCostsResult.value || [];
  else errors.endpointCosts = endpointCostsResult.reason;

  if (trendResult.status === 'fulfilled') snapshot.trend = trendResult.value || EMPTY_TREND;
  else errors.trend = trendResult.reason;

  if (todayTrendResult.status === 'fulfilled') snapshot.todayTrend = todayTrendResult.value;
  else errors.todayTrend = todayTrendResult.reason;

  return { snapshot, errors };
};

/**
 * 概览页快照加载。
 * @param {Object} options
 * @param {string} options.period - 页面统一时间范围（1h/1d/7d/30d）
 * @returns {{snapshot: Object, errors: Object, isInitialized: boolean,
 *   refreshing: boolean, lastUpdate: string|null, refresh: Function,
 *   refreshKey: number, snapshotPeriod: string|null}}
 */
const useOverviewData = ({ period = '1d' } = {}) => {
  const { formatTimeOnly, getTodayTimeRange, getRecentDaysRange } = useTimezone();

  const [snapshot, setSnapshot] = useState(INITIAL_SNAPSHOT);
  const [errors, setErrors] = useState(EMPTY_ERRORS);
  const [isInitialized, setIsInitialized] = useState(false);
  const [refreshing, setRefreshing] = useState(false);
  const [lastUpdate, setLastUpdate] = useState(null);
  const [refreshKey, setRefreshKey] = useState(0);
  // 当前快照对应的页面时间范围：与 period 不一致时说明切换后还没取回新数据
  const [snapshotPeriod, setSnapshotPeriod] = useState(null);

  const requestIdRef = useRef(0);
  const loadSnapshot = useCallback(async () => {
    const requestId = ++requestIdRef.current;

    const now = new Date();
    const range = buildOverviewRange(period, now);
    // 今日统计与迷你趋势截止同一个快照时刻；昨日为完整业务日。
    const todayRange = { ...getTodayTimeRange(now), end_date: now.toISOString() };
    const twoDayRange = getRecentDaysRange(2, now);
    const yesterdayRange = { start_date: twoDayRange.start_date, end_date: todayRange.start_date };

    const results = await Promise.allSettled([
      fetchUsageStats({ ...todayRange, source_view: 'all' }),
      fetchUsageStats({ ...yesterdayRange, source_view: 'all' }),
      fetchModelUsageStats({ period, ...range, source_view: 'all', limit: MODEL_USAGE_LIMIT }),
      fetchEndpointCostsData(),
      fetchRequestTrendData({ period, ...range, source_view: 'all' }),
      fetchRequestTrendData({ period: '1d', ...todayRange, source_view: 'all' })
    ]);

    // 期间又发起了新请求（切换时间范围 / 连续点击刷新），丢弃过期结果。
    if (requestIdRef.current !== requestId) return;

    const { snapshot: nextSnapshot, errors: nextErrors } = buildSnapshotFromResults(results);

    setSnapshot(nextSnapshot);
    setErrors(nextErrors);
    setRefreshKey((current) => current + 1);
    setSnapshotPeriod(period);
    setIsInitialized(true);

    // 局部失败时保留上一次的成功时间，不谎报数据新鲜度。
    if (Object.keys(nextErrors).length === 0) {
      setLastUpdate(formatTimeOnly(new Date()));
    }
  }, [formatTimeOnly, getRecentDaysRange, getTodayTimeRange, period]);

  useEffect(() => {
    loadSnapshot();
    return () => { requestIdRef.current += 1; };
  }, [loadSnapshot]);

  // 手动刷新：保留当前内容，只在页头显示刷新态
  const refresh = useCallback(async () => {
    setRefreshing(true);
    try {
      await loadSnapshot();
    } finally {
      setRefreshing(false);
    }
  }, [loadSnapshot]);

  return {
    snapshot,
    errors,
    isInitialized,
    refreshing,
    lastUpdate,
    refresh,
    refreshKey,
    snapshotPeriod
  };
};

export default useOverviewData;
