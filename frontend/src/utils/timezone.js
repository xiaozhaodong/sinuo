const DEFAULT_LOCALE = 'zh-CN';

// new Intl.DateTimeFormat 是 ECMA-402 里最贵的构造之一（解析 locale + 加载时区数据）。
// 请求列表一次刷新要渲染上百个时间单元格，不缓存会直接吃掉整帧预算，
// 表现为新请求到达时列表卡顿。下面两层缓存都按时区字符串命中，进程内长期有效。
const validatedTimezones = new Set();
const invalidTimezones = new Set();

export const validateTimezone = (timezone) => {
  const value = String(timezone || '').trim();
  if (!value) throw new Error('后端未返回活动时区');
  if (validatedTimezones.has(value)) return value;
  if (invalidTimezones.has(value)) throw new Error(`后端返回了无效时区：${value}`);
  try {
    new Intl.DateTimeFormat(DEFAULT_LOCALE, { timeZone: value }).format(new Date(0));
  } catch {
    invalidTimezones.add(value);
    throw new Error(`后端返回了无效时区：${value}`);
  }
  validatedTimezones.add(value);
  return value;
};

// 严格绝对时刻解析：仅接受有效 Date 或以 Z / ±hh:mm 结尾的时间点，
// 空串、无时区后缀、无效日期均拒绝，避免浏览器按本地时区解释无时区字符串。
export const parseTimestamp = (value) => {
  if (value instanceof Date) {
    if (!Number.isNaN(value.getTime())) return value;
    throw new Error('无效时间');
  }
  const raw = String(value || '').trim();
  if (!raw) throw new Error('时间为空');
  // 新 API 只接受带 offset 的时间点，避免浏览器把无时区字符串解释为系统本地时间。
  if (!/(?:Z|[+-]\d{2}:\d{2})$/i.test(raw)) {
    throw new Error(`后端时间缺少时区：${raw}`);
  }
  const parsed = new Date(raw);
  if (Number.isNaN(parsed.getTime())) throw new Error(`无效时间：${raw}`);
  return parsed;
};

// 分段解析统一使用 en-CA + h23，保证 year/month/day/hour/minute/second 六个 part 恒定可取。
const BASE_PARTS_OPTIONS = {
  hourCycle: 'h23',
  year: 'numeric', month: '2-digit', day: '2-digit',
  hour: '2-digit', minute: '2-digit', second: '2-digit'
};

const formatterCache = new Map();

// 键排序后参与 cache key，保证同一组选项无论书写顺序都只构造一次 formatter。
const formatterCacheKey = (timezone, options) => {
  const keys = Object.keys(options);
  if (keys.length === 0) return timezone;
  return `${timezone}|${keys.sort().map((key) => `${key}:${options[key]}`).join(',')}`;
};

const getPartsFormatter = (timezone, options) => {
  const key = formatterCacheKey(timezone, options);
  const cached = formatterCache.get(key);
  if (cached) return cached;
  // timeZone 置于 options 之前，沿用调用方可用 options.timeZone 覆盖的既有语义。
  const formatter = new Intl.DateTimeFormat('en-CA', {
    timeZone: timezone,
    ...BASE_PARTS_OPTIONS,
    ...options
  });
  formatterCache.set(key, formatter);
  return formatter;
};

const partsMap = (date, timezone, options = {}) => Object.fromEntries(
  getPartsFormatter(validateTimezone(timezone), options)
    .formatToParts(date)
    .filter((part) => part.type !== 'literal')
    .map((part) => [part.type, part.value])
);

export const formatTimestamp = (value, timezone) => {
  if (!value) return '-';
  try {
    const parts = partsMap(parseTimestamp(value), timezone);
    return `${parts.year}/${Number(parts.month)}/${Number(parts.day)} ${parts.hour}:${parts.minute}:${parts.second}`;
  } catch {
    return '-';
  }
};

export const formatTimeOnly = (value, timezone) => {
  if (!value) return '-';
  try {
    const parts = partsMap(parseTimestamp(value), timezone);
    return `${parts.hour}:${parts.minute}:${parts.second}`;
  } catch {
    return '-';
  }
};

// 列表时间列：当天显示时分秒，其它日期显示「月/日 时:分」，按配置时区判断「当天」。
// 完整时间由详情处展示，列表列宽因此可以收窄。
export const formatListTimestamp = (value, timezone, now = new Date()) => {
  if (!value) return '-';
  try {
    const parts = partsMap(parseTimestamp(value), timezone);
    const today = partsMap(now, timezone);
    const sameDay = parts.year === today.year && parts.month === today.month && parts.day === today.day;
    return sameDay
      ? `${parts.hour}:${parts.minute}:${parts.second}`
      : `${Number(parts.month)}/${Number(parts.day)} ${parts.hour}:${parts.minute}`;
  } catch {
    return '-';
  }
};

export const formatMonthDayTime = (value, timezone) => {
  if (!value) return '-';
  try {
    const parts = partsMap(parseTimestamp(value), timezone);
    return `${parts.month}-${parts.day} ${parts.hour}:${parts.minute}`;
  } catch {
    return '-';
  }
};

export const getZonedDateParts = (now, timezone) => {
  const parts = partsMap(parseTimestamp(now instanceof Date ? now : new Date(now)), timezone);
  return {
    year: Number(parts.year), month: Number(parts.month), day: Number(parts.day),
    hour: Number(parts.hour), minute: Number(parts.minute), second: Number(parts.second)
  };
};

const shiftCalendarDate = ({ year, month, day }, days) => {
  const cursor = new Date(Date.UTC(year, month - 1, day));
  cursor.setUTCDate(cursor.getUTCDate() + days);
  return { year: cursor.getUTCFullYear(), month: cursor.getUTCMonth() + 1, day: cursor.getUTCDate() };
};

const wallDate = ({ year, month, day }) => (
  `${String(year).padStart(4, '0')}-${String(month).padStart(2, '0')}-${String(day).padStart(2, '0')}`
);

export const getTodayTimeRange = (timezone, now = new Date()) => {
  const today = getZonedDateParts(now, timezone);
  const tomorrow = shiftCalendarDate(today, 1);
  return { start_date: `${wallDate(today)}T00:00`, end_date: `${wallDate(tomorrow)}T00:00` };
};

export const getRecentDaysRange = (days, timezone, now = new Date()) => {
  const count = Math.max(1, Number.parseInt(days, 10) || 1);
  const today = getZonedDateParts(now, timezone);
  const start = shiftCalendarDate(today, -(count - 1));
  const end = shiftCalendarDate(today, 1);
  return { start_date: `${wallDate(start)}T00:00`, end_date: `${wallDate(end)}T00:00` };
};
