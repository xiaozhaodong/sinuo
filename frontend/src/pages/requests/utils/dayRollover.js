import { getRecentDaysRange, getTodayTimeRange } from '../../../utils/timezone.js';

export const ROLLING_TIME_RANGES = ['today', '7days', '30days'];

export const computePresetRange = (range, timezone, now = new Date()) => {
  let timeRange;
  switch (range) {
    case '7days':
      timeRange = getRecentDaysRange(7, timezone, now);
      break;
    case '30days':
      timeRange = getRecentDaysRange(30, timezone, now);
      break;
    default:
      timeRange = getTodayTimeRange(timezone, now);
  }
  return { startDate: timeRange.start_date, endDate: timeRange.end_date };
};

export const resolveDayRollover = ({ activeRange, filters, timezone, now = new Date() }) => {
  if (!ROLLING_TIME_RANGES.includes(activeRange) || !timezone) return null;

  const nextRange = computePresetRange(activeRange, timezone, now);
  // 直接比较业务时区区间，跨天、时区变化或外部修改后都按当前预设收敛。
  return filters.startDate === nextRange.startDate && filters.endDate === nextRange.endDate
    ? null
    : nextRange;
};
