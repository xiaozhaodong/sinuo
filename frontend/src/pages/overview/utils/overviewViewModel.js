// ============================================
// 概览页视图模型 —— 纯函数，便于单测
// 2026-09-09 概览改版：KPI 环比、模型调用表、成本排行、Token 分布
// ============================================

import { parseTimestamp } from '../../../utils/timezone.js';

// 页面统一时间范围。period 同时用于 request_logs 聚合（模型表 / Token 分布）
// 与趋势分桶接口（GetRequestTrendData）。
export const OVERVIEW_PERIODS = [
  { value: '1h', label: '1h', caption: '近 1 小时' },
  { value: '1d', label: '24h', caption: '近 24 小时' },
  { value: '7d', label: '7d', caption: '近 7 天' },
  { value: '30d', label: '30d', caption: '近 30 天' }
];

export const DEFAULT_OVERVIEW_PERIOD = '1d';

export const resolveOverviewPeriod = (period) =>
  OVERVIEW_PERIODS.find((item) => item.value === period) || OVERVIEW_PERIODS[1];

export const periodCaption = (period) => resolveOverviewPeriod(period).caption;

// 刷新开始时固定绝对时间。所有概览查询共用这个窗口，不受宿主时区和 DST 影响。
const PERIOD_HOURS = { '1h': 1, '1d': 24, '7d': 168, '30d': 720 };

export const buildOverviewRange = (period, now = new Date()) => {
  const end = parseTimestamp(now);
  const hours = PERIOD_HOURS[resolveOverviewPeriod(period).value];
  return {
    start_date: new Date(end.getTime() - hours * 3600000).toISOString(),
    end_date: end.toISOString()
  };
};

// 直接交接已经显示的快照时间，保留 UTC 和微秒；日期输入仅负责转换显示。
export const buildRequestsRangeFromSnapshot = (snapshot) => {
  const start = parseTimestamp(snapshot?.start_time);
  const end = parseTimestamp(snapshot?.end_time);
  if (end <= start) throw new Error('概览快照时间范围无效');
  return { startDate: snapshot.start_time, endDate: snapshot.end_time };
};

/** 今日各小时的真实趋势；成功率和耗时无样本时保留断点，不补造 0。 */
export const buildKpiTrends = (trend) => {
  const points = Array.isArray(trend?.points) ? trend.points : [];
  return {
    requests: points.map((point) => ({ time: point.time, value: point.total })),
    'success-rate': points.map((point) => ({ time: point.time, value: point.total > 0 ? point.success / point.total * 100 : null })),
    latency: points.map((point) => ({ time: point.time, value: point.duration_count > 0 ? point.avg_duration_ms : null })),
    failed: points.map((point) => ({ time: point.time, value: point.fail }))
  };
};

// ============================================
// 格式化
// ============================================

const EMPTY_VALUE = '—';

export const formatCount = (value) => {
  const number = Number(value);
  if (!Number.isFinite(number)) return '0';
  return number.toLocaleString('en-US');
};

export const formatTokenCount = (value) => {
  const number = Number(value) || 0;
  if (number >= 1000000) return `${(number / 1000000).toFixed(1)}M`;
  if (number >= 1000) return `${(number / 1000).toFixed(1)}K`;
  return String(number);
};

export const formatDurationMs = (value) => {
  const number = Number(value);
  if (!Number.isFinite(number) || number <= 0) return EMPTY_VALUE;
  if (number >= 1000) return `${(number / 1000).toFixed(1)}s`;
  return `${Math.round(number)}ms`;
};

export const formatCostUSD = (value) => {
  const number = Number(value) || 0;
  if (number >= 1) return `$${number.toFixed(2)}`;
  if (number >= 0.01) return `$${number.toFixed(3)}`;
  return `$${number.toFixed(4)}`;
};

export const formatSuccessRate = (rate, totalRequests) => {
  if (!Number.isFinite(Number(totalRequests)) || Number(totalRequests) <= 0) return EMPTY_VALUE;
  const number = Number(rate);
  if (!Number.isFinite(number)) return EMPTY_VALUE;
  return `${number.toFixed(2)}%`;
};

// ============================================
// KPI 卡片
// ============================================

const buildDelta = (current, previous, formatter, { lowerIsBetter = false, neutral = false } = {}) => {
  if (!Number.isFinite(current) || !Number.isFinite(previous)) return null;
  const diff = current - previous;
  const text = formatter({ diff, current, previous });
  if (!text) return null;
  const improved = lowerIsBetter ? diff < 0 : diff > 0;
  return {
    text,
    direction: diff === 0 ? 'flat' : diff > 0 ? 'up' : 'down',
    // 请求量这类指标只描述变化，不带好坏色彩；其余按方向判定好坏。
    tone: neutral || diff === 0 ? 'neutral' : improved ? 'success' : 'danger'
  };
};

const percentDeltaFormatter = ({ diff, previous }) => {
  if (!Number.isFinite(previous) || previous <= 0) return null;
  const percent = (diff / previous) * 100;
  const abs = Math.abs(percent);
  if (abs < 0.05) return '持平';
  return `${percent > 0 ? '↑' : '↓'} ${abs.toFixed(1)}%`;
};

const pointDeltaFormatter = ({ diff }) => {
  const abs = Math.abs(diff);
  if (abs < 0.005) return '持平';
  return `${diff > 0 ? '↑' : '↓'} ${abs.toFixed(2)}pt`;
};

const msDeltaFormatter = ({ diff }) => {
  const abs = Math.abs(diff);
  if (abs < 0.5) return '持平';
  // 与数值列同口径：跨秒的差值也要读成 15.0s，而不是 14953ms
  return `${diff > 0 ? '↑' : '↓'} ${formatDurationMs(abs)}`;
};

const countDeltaFormatter = ({ diff }) => {
  if (diff === 0) return '持平';
  return `${diff > 0 ? '↑' : '↓'} ${formatCount(Math.abs(diff))}`;
};

const KPI_CARD_META = [
  { key: 'requests', title: '今日请求', tone: 'tone-indigo' },
  { key: 'success-rate', title: '成功率', tone: 'tone-emerald' },
  { key: 'latency', title: '平均延迟', tone: 'tone-cyan' },
  { key: 'failed', title: '失败请求', tone: 'tone-slate' }
];

/** 今日统计本身没查到：四张卡都给占位符，不能把「查询失败」画成「0 请求」。 */
const buildUnavailableKpiCards = () => KPI_CARD_META.map((meta) => ({
  ...meta,
  value: EMPTY_VALUE,
  comparison: { label: '今日数据不可用', deltaText: null, tone: 'neutral' },
  attention: null
}));

/**
 * 构建 4 个 KPI 卡片（今日口径 + 与昨日环比）。
 * 昨日无请求时不编造百分比，直接说明「较昨日无数据」；
 * 昨日查询失败时用「较昨日数据不可用」区分「没有」和「没查到」；
 * 今日查询失败时整体降级为占位符。
 */
export const buildKpiCards = (today = {}, yesterday = {}, { comparisonUnavailable = false, todayUnavailable = false } = {}) => {
  if (todayUnavailable) return buildUnavailableKpiCards();

  const todayRequests = Number(today.total_requests) || 0;
  const yesterdayRequests = Number(yesterday.total_requests) || 0;
  const comparable = yesterdayRequests > 0;

  const comparison = (delta) => {
    if (!comparable) {
      return {
        label: comparisonUnavailable ? '较昨日数据不可用' : '较昨日无数据',
        deltaText: null,
        tone: 'neutral'
      };
    }
    if (!delta) return { label: '较昨日', deltaText: null, tone: 'neutral' };
    return { label: '较昨日', deltaText: delta.text, tone: delta.tone };
  };

  return [
    {
      key: 'requests',
      title: '今日请求',
      value: formatCount(todayRequests),
      comparison: comparison(buildDelta(todayRequests, yesterdayRequests, percentDeltaFormatter, { neutral: true })),
      tone: 'tone-indigo'
    },
    {
      key: 'success-rate',
      title: '成功率',
      value: formatSuccessRate(today.success_rate, todayRequests),
      comparison: comparison(buildDelta(Number(today.success_rate) || 0, Number(yesterday.success_rate) || 0, pointDeltaFormatter)),
      tone: 'tone-emerald'
    },
    {
      key: 'latency',
      title: '平均延迟',
      value: formatDurationMs(today.avg_duration_ms),
      comparison: comparison(buildDelta(Number(today.avg_duration_ms) || 0, Number(yesterday.avg_duration_ms) || 0, msDeltaFormatter, { lowerIsBetter: true })),
      tone: 'tone-cyan'
    },
    {
      key: 'failed',
      title: '失败请求',
      value: formatCount(Number(today.failed_requests) || 0),
      comparison: comparison(buildDelta(Number(today.failed_requests) || 0, Number(yesterday.failed_requests) || 0, countDeltaFormatter, { lowerIsBetter: true })),
      tone: (Number(today.failed_requests) || 0) > 0 ? 'tone-rose' : 'tone-slate',
      attention: (Number(today.failed_requests) || 0) > 0 ? '需要关注' : (todayRequests > 0 ? '暂无失败' : '暂无请求')
    }
  ];
};

// ============================================
// 模型调用表
// ============================================

const FAMILY_LABELS = {
  claude: 'Claude',
  codex: 'Codex',
  image: 'Image',
  other: '其他'
};

const UPSTREAM_TYPE_LABELS = {
  endpoint: '端点',
  account: '账号池',
  subscription: '订阅直通'
};

export const familyLabel = (family) => FAMILY_LABELS[family] || '其他';

export const upstreamTypeLabel = (upstreamType) => UPSTREAM_TYPE_LABELS[upstreamType] || '未知来源';

/** 模型尚未解析时不应伪装成一个可筛选的真实模型。 */
export const isUnresolvedModelName = (modelName) => {
  const name = String(modelName || '').trim().toLowerCase();
  return name === '' || name === 'unknown' || name === 'default';
};

export const modelDisplayName = (modelName) => String(modelName || '').trim();

/**
 * 成功率着色：只有「接近满分」才给绿色，低于 90% 直接标红，
 * 避免 0.00% / 62% 也挂着一片绿，把失败读成正常。
 */
export const successRateTone = (rate, requestCount) => {
  if (!Number.isFinite(Number(requestCount)) || Number(requestCount) <= 0) return 'neutral';
  const value = Number(rate);
  if (!Number.isFinite(value)) return 'neutral';
  if (value >= 99) return 'success';
  if (value < 90) return 'danger';
  return 'neutral';
};

/**
 * 把模型聚合结果转成表格行视图模型。
 * 平均耗时为 0（无有效样本）时显示占位符，而不是 0ms。
 */
export const buildModelRows = (modelUsage) => {
  const rows = Array.isArray(modelUsage?.rows) ? modelUsage.rows : [];
  return rows.filter((row) => !isUnresolvedModelName(row?.model_name)).map((row) => ({
    key: `${row.model_name}|${row.request_family}|${row.upstream_type}`,
    modelName: modelDisplayName(row.model_name),
    rawModelName: String(row.model_name || ''),
    requestFamily: row.request_family,
    upstreamType: row.upstream_type,
    sourceLabel: `${familyLabel(row.request_family)} · ${upstreamTypeLabel(row.upstream_type)}`,
    requestCount: Number(row.request_count) || 0,
    requestCountText: formatCount(row.request_count),
    successRate: Number(row.success_rate) || 0,
    successRateText: formatSuccessRate(row.success_rate, row.request_count),
    successRateTone: successRateTone(row.success_rate, row.request_count),
    durationText: formatDurationMs(row.avg_duration_ms),
    tokensText: formatTokenCount(row.total_tokens),
    costText: formatCostUSD(row.total_cost_usd)
  }));
};

/** Token 分布视图模型：范围与页面时间范围一致。 */
export const buildTokenBreakdown = (modelUsage) => {
  const input = Number(modelUsage?.input_tokens) || 0;
  const output = Number(modelUsage?.output_tokens) || 0;
  const cacheCreation = Number(modelUsage?.cache_creation_tokens) || 0;
  const cacheRead = Number(modelUsage?.cache_read_tokens) || 0;
  const total = input + output + cacheCreation + cacheRead;

  const items = [
    { key: 'input', name: '输入 Token', value: input },
    { key: 'output', name: '输出 Token', value: output },
    { key: 'cacheCreation', name: '缓存创建', value: cacheCreation },
    { key: 'cacheRead', name: '缓存读取', value: cacheRead }
  ].map((item) => ({
    ...item,
    percent: total > 0 ? (item.value / total) * 100 : 0,
    percentText: total > 0 && item.value > 0 ? `${((item.value / total) * 100).toFixed(0)}%` : EMPTY_VALUE,
    valueText: formatTokenCount(item.value)
  }));

  return {
    total,
    totalText: formatTokenCount(total),
    hasData: total > 0,
    items
  };
};

/**
 * 上游成本排行视图模型：按成本降序取前 limit 条，宽度按最大值归一。
 * 成本全为 0 时返回空列表，避免画一排零宽度空条。
 */
export const buildCostRanking = (endpointCosts, limit = 6) => {
  const list = (Array.isArray(endpointCosts) ? endpointCosts : [])
    .map((item) => ({
      name: String(item?.name || '').trim() || '未知上游',
      cost: Number(item?.cost) || 0,
      tokens: Number(item?.tokens) || 0
    }))
    .filter((item) => item.cost > 0)
    .sort((a, b) => b.cost - a.cost)
    .slice(0, limit);

  const maxCost = list.reduce((max, item) => Math.max(max, item.cost), 0);

  return list.map((item) => ({
    ...item,
    costText: formatCostUSD(item.cost),
    tokensText: formatTokenCount(item.tokens),
    percent: maxCost > 0 ? Math.max((item.cost / maxCost) * 100, 2) : 0
  }));
};

/**
 * 请求趋势摘要：数据点已按时间桶聚合，因此窗口内总量就是各桶之和。
 */
export const buildTrendSummary = (chartData) => {
  const points = Array.isArray(chartData) ? chartData : [];
  if (points.length === 0) {
    return { hasData: false, requestsText: '0', failedText: '0', successText: '0' };
  }

  const sum = (key) => points.reduce((total, point) => total + (Number(point?.[key]) || 0), 0);

  return {
    hasData: true,
    requestsText: formatCount(sum('total')),
    successText: formatCount(sum('success')),
    failedText: formatCount(sum('fail'))
  };
};

/** 趋势桶粒度 → x 轴标签格式：天桶显示月-日，其余显示时分。 */
export const formatTrendBucketLabel = (timestamp, granularity, { formatTimeOnly, formatMonthDayTime }) => {
  if (!timestamp) return '';
  if (granularity === 'day') {
    const label = formatMonthDayTime(timestamp);
    // formatMonthDayTime 形如 "09-09 10:00"，天桶只需要日期部分
    return String(label).split(' ')[0] || String(label);
  }
  return formatTimeOnly(timestamp);
};
