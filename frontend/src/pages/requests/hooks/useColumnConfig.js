// ============================================
// useColumnConfig Hook - 列配置状态管理
// 2025-12-01 09:30:08
// ============================================

import { useState, useCallback } from 'react';
import { TABLE_COLUMNS, DEFAULT_VISIBLE_COLUMNS } from '../utils/constants.js';

/**
 * useColumnConfig Hook - 管理表格列的显示/隐藏状态
 * @returns {Object}
 */
export const useColumnConfig = () => {
  // 可见列状态（默认隐藏会话列）
  const [visibleColumns, setVisibleColumns] = useState(DEFAULT_VISIBLE_COLUMNS);

  // 切换单个列的显示/隐藏
  const toggleColumn = useCallback((columnId) => {
    // 检查是否为必须显示的列
    const column = TABLE_COLUMNS.find(col => col.id === columnId);
    if (column?.alwaysVisible) {
      return; // 必须显示的列不能隐藏
    }

    setVisibleColumns(prev => {
      if (prev.includes(columnId)) {
        // 隐藏列：从数组中移除
        return prev.filter(id => id !== columnId);
      } else {
        // 显示列：按照TABLE_COLUMNS的原始顺序插入到正确位置
        const newVisible = [...prev, columnId];
        // 根据TABLE_COLUMNS的顺序重新排序
        return TABLE_COLUMNS
          .map(col => col.id)
          .filter(id => newVisible.includes(id));
      }
    });
  }, []);

  // 重置为默认配置（会话列隐藏）
  const resetColumns = useCallback(() => {
    setVisibleColumns(DEFAULT_VISIBLE_COLUMNS);
  }, []);

  // 检查某列是否可见
  const isColumnVisible = useCallback((columnId) => {
    return visibleColumns.includes(columnId);
  }, [visibleColumns]);

  // 获取当前可见的列配置
  const getVisibleColumnConfigs = useCallback(() => {
    return TABLE_COLUMNS.filter(col => visibleColumns.includes(col.id));
  }, [visibleColumns]);

  return {
    visibleColumns,
    toggleColumn,
    resetColumns,
    isColumnVisible,
    getVisibleColumnConfigs,
    allColumns: TABLE_COLUMNS
  };
};

export default useColumnConfig;
