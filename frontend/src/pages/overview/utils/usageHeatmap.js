import { formatCostUSD, formatCount, formatTokenCount } from './overviewViewModel.js';

export const HEATMAP_METRICS = [
  { value: 'tokens', label: 'Token' },
  { value: 'cost', label: '成本' },
  { value: 'requests', label: '请求数' }
];
export const HEATMAP_VIEWS = [
  { value: 'daily', label: '每天' },
  { value: 'weekly', label: '每周' },
  { value: 'cumulative', label: '累计总量' }
];
export const HEATMAP_LEVEL_PERCENTAGES = [0, 30, 55, 80, 100];
const WEEKDAYS = ['周日', '周一', '周二', '周三', '周四', '周五', '周六'];
const DAY_MS = 86400000;
const dateTime = (date) => {
  const [year, month, day] = date.split('-').map(Number);
  return Date.UTC(year, month - 1, day);
};
const dateString = (time) => new Date(time).toISOString().slice(0, 10);
const mondayOffset = (date) => (new Date(dateTime(date)).getUTCDay() + 6) % 7;
const number = (value) => Math.max(Number(value) || 0, 0);

export const heatmapWeekday = (date) => WEEKDAYS[new Date(dateTime(date)).getUTCDay()];
export const heatmapMetricValue = (day, metric) => number(day?.[{
  tokens: 'total_tokens', cost: 'total_cost_usd', requests: 'request_count'
}[metric]]);
export const formatHeatmapValue = (value, metric) => ({
  tokens: formatTokenCount, cost: formatCostUSD, requests: formatCount
}[metric])(value);

// 在完整区间非零样本中按经验分位分档；相等值使用相同的上界排名。
export const heatmapLevel = (value, sortedNonzeroValues) => {
  if (value <= 0 || !sortedNonzeroValues.length) return 0;
  let low = 0;
  let high = sortedNonzeroValues.length;
  while (low < high) {
    const middle = Math.floor((low + high) / 2);
    if (sortedNonzeroValues[middle] <= value) low = middle + 1;
    else high = middle;
  }
  return Math.max(1, Math.min(4, Math.ceil(4 * low / sortedNonzeroValues.length)));
};

export const heatmapCellBackground = (level) => level === 0
  ? 'var(--app-surface-mut)'
  : `color-mix(in srgb, var(--app-accent-ring) ${HEATMAP_LEVEL_PERCENTAGES[level]}%, var(--app-surface-mut))`;

export const heatmapDayState = (day, dataStartDate, metric) => {
  if (!dataStartDate || day.date < dataStartDate) return 'no-record';
  return heatmapMetricValue(day, metric) > 0 ? 'usage' : 'zero';
};

// 直接交接后端 UTC 边界，不在浏览器重新计算业务日或 DST。
export const heatmapRequestsFilters = (entry, metric) => {
  if (!entry || entry.state === 'no-record' || !(entry.value > 0) || !entry.start_time || !entry.end_time) return null;
  return {
    startDate: entry.start_time,
    endDate: entry.end_time,
    ...(metric === 'requests' ? { includeCompaction: false } : {})
  };
};

export const heatmapDayTooltip = (day, state, metric) => {
  const heading = `${day.date} ${heatmapWeekday(day.date)}`;
  if (state !== 'usage') return { heading: `${heading} · ${state === 'no-record' ? '无记录' : '无用量'}`, rows: [] };
  const total = number(day.total_tokens);
  const output = number(day.output_tokens);
  const creation = number(day.cache_creation_tokens);
  const read = number(day.cache_read_tokens);
  return {
    heading,
    rows: [
      { label: 'Token', value: formatTokenCount(total), strong: metric === 'tokens' },
      { label: '非缓存输入', value: formatTokenCount(Math.max(total - output - creation - read, 0)), indent: true },
      { label: '输出', value: formatTokenCount(output), indent: true },
      { label: '缓存创建', value: formatTokenCount(creation), indent: true },
      { label: '缓存读取', value: formatTokenCount(read), indent: true },
      { label: '成本', value: formatCostUSD(day.total_cost_usd), strong: metric === 'cost' },
      { label: '请求数', value: formatCount(day.request_count), strong: metric === 'requests' }
    ]
  };
};

export const buildHeatmapDays = (data, metric) => {
  const days = (Array.isArray(data?.days) ? data.days : []).filter((day) => day.date <= data.end_date);
  const nonzero = days.map((day) => heatmapMetricValue(day, metric)).filter((value) => value > 0).sort((a, b) => a - b);
  return days.map((day) => {
    const value = heatmapMetricValue(day, metric);
    const state = heatmapDayState(day, data.data_start_date, metric);
    const entry = { ...day, value, state, row: mondayOffset(day.date), level: heatmapLevel(value, nonzero) };
    return {
      ...entry,
      filters: heatmapRequestsFilters(entry, metric),
      ariaLabel: `${day.date} ${heatmapWeekday(day.date)} ${HEATMAP_METRICS.find((item) => item.value === metric).label} ${formatHeatmapValue(value, metric)}`,
      tooltip: heatmapDayTooltip(day, state, metric)
    };
  });
};

export const buildHeatmapWeeks = (days, endDate, metric) => {
  const weeks = [];
  for (const day of days) {
    const weekStart = dateString(dateTime(day.date) - day.row * DAY_MS);
    let week = weeks[weeks.length - 1];
    if (!week || week.date !== weekStart) {
      week = { date: weekStart, days: [], value: null, start_time: day.start_time, end_time: day.end_time };
      weeks.push(week);
    }
    week.days.push(day);
    week.end_time = day.end_time;
    if (day.state !== 'no-record') week.value = (week.value || 0) + day.value;
  }
  return weeks.map((week) => {
    const lastDate = week.days[week.days.length - 1].date;
    const inProgress = lastDate === endDate && mondayOffset(endDate) < 6;
    const state = week.value === null ? 'no-record' : week.value > 0 ? 'usage' : 'zero';
    const entry = { ...week, state, inProgress };
    return {
      ...entry,
      filters: heatmapRequestsFilters(entry, metric),
      tooltip: {
        heading: `${week.date} — ${lastDate}`,
        note: inProgress ? '本周进行中，截至今天' : '',
        rows: state === 'no-record' ? [] : [{ label: HEATMAP_METRICS.find((item) => item.value === metric).label, value: formatHeatmapValue(week.value, metric), strong: true }],
        status: state === 'no-record' ? '无记录' : state === 'zero' ? '无用量' : ''
      }
    };
  });
};

export const buildHeatmapCumulative = (days, metric) => {
  let cumulative = 0;
  return days.map((day) => {
    const value = day.state === 'no-record' ? null : (cumulative += day.value);
    return {
      date: day.date, value,
      tooltip: {
        heading: `${day.date} ${heatmapWeekday(day.date)}`,
        status: value === null ? '无记录' : '',
        rows: value === null ? [] : [
          { label: '截至当天累计', value: formatHeatmapValue(value, metric), strong: true },
          { label: '当天', value: formatHeatmapValue(day.value, metric) }
        ]
      }
    };
  });
};

// 标签只标包含当月 1 号的列；跨年仍统一使用「1月」，由测试固定。
export const heatmapMonthLabels = (weeks) => {
  const labels = [];
  for (let index = 0; index < weeks.length; index++) {
    const first = weeks[index].days.find((day) => day.date.slice(8) === '01');
    if (!first || (labels.length && index - labels[labels.length - 1].index < 3)) continue;
    labels.push({ index, date: weeks[index].date, dayDate: first.date, label: `${Number(first.date.slice(5, 7))}月` });
  }
  return labels;
};

export const heatmapSummary = (days, metric) => {
  const total = days.reduce((sum, day) => sum + day.value, 0);
  const activeDays = days.filter((day) => day.value > 0).length;
  return { total, activeDays, subtitle: `近一年 · 共 ${formatHeatmapValue(total, metric)} · 活跃 ${activeDays} 天` };
};

export const heatmapLayout = (width, weekCount = 53) => {
  const available = Math.max(Number(width) || 0, 0);
  const count = Math.max(weekCount, 0);
  if (!count || available < 10) return { cellSize: 10, visibleWeeks: 0, gridWidth: 0 };
  const cellSize = Math.min(16, Math.max(10, Math.floor((available - 3 * (count - 1)) / count)));
  const visibleWeeks = Math.min(count, Math.floor((available + 3) / (cellSize + 3)));
  return { cellSize, visibleWeeks, gridWidth: visibleWeeks * cellSize + Math.max(visibleWeeks - 1, 0) * 3 };
};

export const HEATMAP_TOOLTIP_WIDTH = 250;
const HEATMAP_TOOLTIP_GAP = 8;

// 浮层优先放在格子右侧、与格子竖直居中；右侧放不下翻到左侧，最后整体钳在面板内。
// anchor 是格子相对面板主体的 left / right / centerY，size 是浮层量出的实际尺寸。
export const heatmapTooltipPosition = (anchor, bounds, size) => {
  const clamp = (value, max) => Math.max(0, Math.min(value, max));
  const right = anchor.right + HEATMAP_TOOLTIP_GAP;
  const left = right + size.width <= bounds.width ? right : anchor.left - HEATMAP_TOOLTIP_GAP - size.width;
  return {
    left: clamp(left, bounds.width - size.width),
    top: clamp(anchor.centerY - size.height / 2, bounds.height - size.height)
  };
};

const HEATMAP_ARROW_STEPS = { ArrowUp: -1, ArrowDown: 1, ArrowLeft: -7, ArrowRight: 7 };

// 日历只占一个 Tab 位，方向键在可见格子间移动：上下差一天（不跨列），左右差一周；越界返回 null。
export const heatmapKeyboardTarget = (days, currentDate, key) => {
  const step = HEATMAP_ARROW_STEPS[key];
  const index = days.findIndex((day) => day.date === currentDate);
  if (!step || index < 0) return null;
  const { row } = days[index];
  if ((key === 'ArrowUp' && row === 0) || (key === 'ArrowDown' && row === 6)) return null;
  return days[index + step]?.date ?? null;
};
