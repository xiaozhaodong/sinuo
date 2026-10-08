// ============================================
// 请求页筛选条的纯逻辑
// 2026-10-07 桌面化：筛选从「弹出面板 + 应用」改为工具栏下方的筛选条，选了立即生效
// ============================================
// 条件按钮只管类型 / 来源 / 状态 / 模型 / 上游，外加概览跳转带来的「仅生成」；
// 时间由工具栏的今天 / 7 天 / 30 天和筛选条上的自定义区间按钮负责，不计入生效条件数。

import { DEFAULT_FILTERS, PAGINATION_CONFIG } from './constants.js';

export const FILTER_CHIP_KEYS = ['requestFamily', 'sourceView', 'status', 'model', 'upstreamName', 'includeCompaction'];

export const isChipActive = (filters = {}, key) => {
  const value = filters[key];
  if (key === 'includeCompaction') return value === false;
  return value !== undefined && value !== null && value !== '' && value !== 'all' && value !== DEFAULT_FILTERS[key];
};

export const countActiveChips = (filters = {}) => FILTER_CHIP_KEYS.filter((key) => isChipActive(filters, key)).length;

// 选中某个条件得到的筛选补丁；空值回到默认。
// 类型 / 来源变了，上游候选随之变化，原来选的上游清掉（与旧筛选面板一致）
export const buildChipPatch = (key, value) => {
  const next = value === undefined || value === null || value === '' ? DEFAULT_FILTERS[key] : value;
  if (key === 'requestFamily' || key === 'sourceView') {
    return { [key]: next, upstreamName: DEFAULT_FILTERS.upstreamName };
  }
  return { [key]: next };
};

export const buildClearChipsPatch = () => Object.fromEntries(FILTER_CHIP_KEYS.map((key) => [key, DEFAULT_FILTERS[key]]));

const PRESET_RANGE_LABELS = {
  today: '今天',
  '7days': '近 7 天',
  '30days': '近 30 天'
};

// 墙上时间（YYYY-MM-DDTHH:mm[:ss]）缩成「月/日 时:分」
const formatWallShort = (wall) => {
  const match = String(wall || '').match(/^\d{4}-(\d{2})-(\d{2})T(\d{2}):(\d{2})/);
  if (!match) return '';
  return `${Number(match[1])}/${Number(match[2])} ${match[3]}:${match[4]}`;
};

// 时间按钮上的文字：预设显示名称；自定义区间显示「月/日 时:分 – 月/日 时:分」，缺一端写「起」/「今」
export const describeTimeChip = (activeRange, startWall, endWall) => {
  if (PRESET_RANGE_LABELS[activeRange]) return PRESET_RANGE_LABELS[activeRange];
  const start = formatWallShort(startWall);
  const end = formatWallShort(endWall);
  if (!start && !end) return '不限';
  return `${start || '起'} – ${end || '今'}`;
};

// 自定义区间能否应用：两端都填时开始必须早于结束（同为墙上时间字符串，可直接比较）
export const isValidCustomRange = (startWall, endWall) => {
  if (!startWall || !endWall) return Boolean(startWall || endWall);
  return startWall < endWall;
};

export const resolveStoredPageSize = (raw, options = PAGINATION_CONFIG.PAGE_SIZE_OPTIONS, fallback = PAGINATION_CONFIG.DEFAULT_PAGE_SIZE) => {
  const value = Number(raw);
  return options.includes(value) ? value : fallback;
};
