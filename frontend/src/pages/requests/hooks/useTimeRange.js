// ============================================
// useTimeRange Hook - 时间范围快捷选择
// 2025-12-01 09:30:08
// ============================================

import { useState, useCallback } from 'react';
import { useTimezone } from '@/contexts/TimezoneContext.jsx';
import { computePresetRange } from '../utils/dayRollover.js';

/**
 * useTimeRange Hook - 管理时间范围快捷选择
 * @param {Function} onRangeChange - 时间范围变更回调
 * @param {string} initialRange - 初始高亮的范围（概览带过来的自定义区间传 'custom'，不点亮任何预设）
 * @returns {Object}
 */
export const useTimeRange = (onRangeChange, initialRange = 'today') => {
  const { timezone } = useTimezone();
  const [activeRange, setActiveRange] = useState(initialRange);

  // 选择时间范围
  const selectRange = useCallback((range) => {
    setActiveRange(range);

    const timeRange = computePresetRange(range, timezone);

    // 通知父组件时间范围变更
    onRangeChange?.(timeRange);
  }, [timezone, onRangeChange]);

  // 筛选条里应用了自定义区间：三个预设都不再高亮
  const markCustomRange = useCallback(() => setActiveRange('custom'), []);

  return {
    activeRange,
    selectRange,
    markCustomRange
  };
};

export default useTimeRange;
