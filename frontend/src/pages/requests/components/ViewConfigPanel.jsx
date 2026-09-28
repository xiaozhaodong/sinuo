// ============================================
// ViewConfigPanel - 列显示配置面板
// 2025-12-01 09:30:08
// ============================================

import { X, Columns, Eye, EyeOff } from 'lucide-react';

/**
 * ViewConfigPanel - 列显示配置弹出面板
 * @param {Object} props
 * @param {boolean} props.isOpen - 是否打开
 * @param {Function} props.onClose - 关闭回调
 * @param {Array} props.columns - 所有列配置
 * @param {Array} props.visibleColumns - 当前可见的列ID数组
 * @param {Function} props.onToggleColumn - 切换列显示回调
 * @param {Function} props.onReset - 重置回调
 */
const ViewConfigPanel = ({
  isOpen,
  onClose,
  columns = [],
  visibleColumns = [],
  onToggleColumn,
  onReset
}) => {
  if (!isOpen) return null;

  return (
    <div className="absolute top-12 right-0 z-30 w-64 bg-surface rounded-xl shadow-xl border border-line ring-1 ring-hairline motion-popover origin-top-right">
      {/* 头部 */}
      <div className="px-4 py-3 border-b border-line-soft flex justify-between items-center bg-surface-sub rounded-t-xl">
        <div className="flex items-center gap-2 text-sm font-semibold text-fg">
          <Columns className="w-3.5 h-3.5 text-accent" />
          <span>显示列配置</span>
        </div>
        <button
          onClick={onClose}
          className="p-1 hover:bg-surface-mut rounded-md transition-colors"
        >
          <X className="w-3.5 h-3.5 text-fg-subtle" />
        </button>
      </div>

      {/* 列选项列表 */}
      <div className="p-2 overflow-y-auto max-h-[300px]">
        {columns.map(col => {
          const isVisible = visibleColumns.includes(col.id);
          return (
            <label
              key={col.id}
              className={`flex items-center justify-between px-3 py-2 rounded-lg cursor-pointer transition-all ${
                col.alwaysVisible
                  ? 'opacity-50 cursor-not-allowed bg-surface-sub'
                  : 'hover:bg-surface-sub active:scale-[0.98]'
              }`}
            >
              <div className="flex items-center gap-3">
                <input
                  type="checkbox"
                  checked={isVisible}
                  disabled={col.alwaysVisible}
                  onChange={() => !col.alwaysVisible && onToggleColumn?.(col.id)}
                  className="app-checkbox"
                />
                <span className={`text-sm ${isVisible ? 'text-fg-body font-medium' : 'text-fg-subtle'}`}>
                  {col.label}
                </span>
              </div>
              {isVisible ? (
                <Eye className="w-3.5 h-3.5 text-fg-subtle" />
              ) : (
                <EyeOff className="w-3.5 h-3.5 text-fg-subtle/60" />
              )}
            </label>
          );
        })}
      </div>

      {/* 底部重置按钮 */}
      <div className="p-3 border-t border-line-soft bg-surface-sub rounded-b-xl flex justify-center">
        <button
          onClick={onReset}
          className="text-xs text-accent font-medium hover:text-accent-fg hover:underline transition-colors"
        >
          恢复默认设置
        </button>
      </div>

      {/* 装饰箭头 */}
      <div className="absolute -top-1.5 right-11 w-3 h-3 bg-surface border-l border-t border-line transform rotate-45"></div>
    </div>
  );
};

export default ViewConfigPanel;
