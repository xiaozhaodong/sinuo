// ============================================
// 概览数据加载的纯函数（供 useOverviewData 与 node 测试共用）
// 2026-10-24 时段统计分区：KPI 组与时段组分开加载，切换时间范围只重取时段组
// ============================================

export const EMPTY_TREND = { period: null, granularity: 'hour', points: [] };

// KPI 组：今日统计、昨日对比与今日迷你趋势，固定为今日，不随时间范围切换
export const KPI_GROUP_KEYS = ['today', 'yesterday', 'todayTrend'];
export const KPI_GROUP_FALLBACKS = { today: {}, yesterday: {}, todayTrend: EMPTY_TREND };

// 时段组：请求趋势、模型调用（Token 分布共用）与上游成本，三个请求共用同一个窗口
export const PERIOD_GROUP_KEYS = ['trend', 'modelUsage', 'endpointCosts'];
export const PERIOD_GROUP_FALLBACKS = { trend: EMPTY_TREND, modelUsage: null, endpointCosts: [] };

/**
 * 按键归并一组 Promise.allSettled 结果：一项失败不丢弃同组其他成功结果，
 * 失败项写入 errors 并回落到缺省值，不保留旧数字冒充新结果。
 */
export const settleGroup = (keys, results, fallbacks = {}) => {
  const data = {};
  const errors = {};
  keys.forEach((key, index) => {
    const result = results[index];
    if (result?.status === 'fulfilled') {
      data[key] = result.value ?? fallbacks[key];
      return;
    }
    data[key] = fallbacks[key];
    errors[key] = result?.reason ?? new Error('加载失败');
  });
  return { data, errors };
};

export const isGroupClean = (group) => Object.keys(group?.errors || {}).length === 0;

/**
 * 只认同组最后一次发起的请求：切换时间范围、连续重试或卸载后，旧请求的结果直接丢弃。
 * @param {{current: number}} versionRef - 组内请求版本
 * @param {Function} task - 返回 Promise 的请求
 * @returns {Promise<{current: boolean, value?: any}>}
 */
export const runLatest = async (versionRef, task) => {
  const version = ++versionRef.current;
  const value = await task();
  return versionRef.current === version ? { current: true, value } : { current: false };
};

/**
 * 时段组某块是否显示加载态。切换时间范围后旧数据不属于新窗口，四块都显示加载；
 * 同一窗口刷新或重试时保留已有内容，只有原本失败的那块显示加载。
 */
export const isPeriodPanelLoading = ({ period, committedPeriod, pending, error }) =>
  committedPeriod !== period || (pending && Boolean(error));
