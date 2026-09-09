// ============================================
// useTimeRange Hook - 时间范围快捷选择
// 2025-12-01 09:30:08
// ============================================

import { useState, useCallback } from 'react';
import { useTimezone } from '@/contexts/TimezoneContext.jsx';

/**
 * 获取指定天数前的时间范围
 * @param {number} days - 天数
 * @returns {{ startDate: string, endDate: string }}
 */
/**
 * useTimeRange Hook - 管理时间范围快捷选择
 * @param {Function} onRangeChange - 时间范围变更回调
 * @param {string} initialRange - 初始高亮的范围（概览带过来的自定义区间传 'custom'，不点亮任何预设）
 * @returns {Object}
 */
export const useTimeRange = (onRangeChange, initialRange = 'today') => {
  const { getTodayTimeRange, getRecentDaysRange } = useTimezone();
  const [activeRange, setActiveRange] = useState(initialRange);

  // 选择时间范围
  const selectRange = useCallback((range) => {
    setActiveRange(range);

    let timeRange;
    switch (range) {
      case 'today':
        timeRange = getTodayTimeRange();
        break;
      case '7days':
        timeRange = getRecentDaysRange(7);
        break;
      case '30days':
        timeRange = getRecentDaysRange(30);
        break;
      default:
        timeRange = getTodayTimeRange();
    }

    timeRange = { startDate: timeRange.start_date, endDate: timeRange.end_date };

    // 通知父组件时间范围变更
    onRangeChange?.(timeRange);
  }, [getRecentDaysRange, getTodayTimeRange, onRangeChange]);

  return {
    activeRange,
    selectRange
  };
};

export default useTimeRange;
