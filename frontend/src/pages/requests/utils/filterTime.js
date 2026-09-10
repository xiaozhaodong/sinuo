import { getTodayTimeRange as getConfiguredTodayRange, getZonedDateParts, parseTimestamp } from '../../../utils/timezone.js';
import { DEFAULT_FILTERS } from './constants.js';

export const getTodayTimeRange = (timezone, now = new Date()) => {
  const range = getConfiguredTodayRange(timezone, now);
  return { startDate: range.start_date, endDate: range.end_date };
};

export const createInitialFilters = (timezone, now = new Date()) => {
  const todayRange = getTodayTimeRange(timezone, now);
  return {
    ...DEFAULT_FILTERS,
    startDate: todayRange.startDate,
    endDate: todayRange.endDate
  };
};

const normalizeFilterDateTime = (value) => {
  const text = String(value || '').trim();
  if (/^\d{4}-\d{2}-\d{2}$/.test(text)) return `${text}T00:00:00`;
  if (/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}$/.test(text)) return `${text}:00`;
  return text;
};

// 绝对时刻仅在日期输入框中转为业务时区。筛选状态保留原始 offset/精度，
// 直到用户主动编辑该字段，防止 DST 重复小时和分钟取整改变查询边界。
export const formatFilterDateTimeInput = (value, timezone) => {
  const text = String(value || '');
  if (!/(?:Z|[+-]\d{2}:\d{2})$/i.test(text)) return text;
  const parts = getZonedDateParts(parseTimestamp(text), timezone);
  const pad = (number) => String(number).padStart(2, '0');
  const fraction = text.match(/\.(\d+)(?:Z|[+-]\d{2}:\d{2})$/i)?.[1].slice(0, 3);
  return `${parts.year}-${pad(parts.month)}-${pad(parts.day)}T${pad(parts.hour)}:${pad(parts.minute)}:${pad(parts.second)}${fraction ? `.${fraction}` : ''}`;
};

export const buildQueryParamsFromFilters = (filters = {}) => {
  const queryParams = {};

  if (filters.startDate) {
    queryParams.start_date = normalizeFilterDateTime(filters.startDate);
  }
  if (filters.endDate) {
    queryParams.end_date = normalizeFilterDateTime(filters.endDate);
  }

  if (filters.status && filters.status !== 'all') queryParams.status = filters.status;
  if (filters.model) queryParams.model = filters.model;
  if (filters.requestFamily && filters.requestFamily !== 'all') queryParams.request_family = filters.requestFamily;
  if (filters.sourceView === 'endpoint' || filters.sourceView === 'account' || filters.sourceView === 'subscription') queryParams.source_view = filters.sourceView;
  if (filters.upstreamName && filters.upstreamName !== 'all') queryParams.upstream_name = filters.upstreamName;
  // 概览跳转携带「仅生成」时收窄列表；缺省不下传，由后端承载全部落库记录。
  if (filters.includeCompaction === false) queryParams.include_compaction = false;

  return queryParams;
};
