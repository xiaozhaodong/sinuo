// LogFilters.jsx - 日志级别筛选（工具栏分段控件）
// 2026-10-07：搜索框与「跟随最新」移到工具栏，这里只剩级别分段；
//             计数在工具栏够宽时才显示（容器查询 @4xl），窄窗口只留级别名
import { LOG_LEVEL_LIST } from '../constants';

/**
 * @param {Object} stats 日志级别统计 { DEBUG: 10, INFO: 20, ... }
 * @param {string} levelFilter 当前级别过滤（'ALL' 表示全部）
 * @param {Function} onLevelFilterChange 级别过滤变化
 * @param {number} totalLogs 总日志数
 */
function LogFilters({ stats, levelFilter, onLevelFilterChange, totalLogs }) {
  const options = [
    { value: 'ALL', label: '全部', count: totalLogs },
    ...LOG_LEVEL_LIST.map((level) => ({ value: level, label: level, count: stats[level] ?? 0 }))
  ];

  return (
    <div role="radiogroup" aria-label="日志级别" className="inline-flex shrink-0 items-center rounded-[7px] bg-surface-mut p-0.5">
      {options.map((option) => {
        const selected = levelFilter === option.value;
        return (
          <button
            key={option.value}
            type="button"
            role="radio"
            aria-checked={selected}
            onClick={() => onLevelFilterChange(option.value)}
            className={`h-[22px] whitespace-nowrap rounded-[5px] px-2 text-xs transition-colors ${
              selected
                ? 'bg-surface font-medium text-fg shadow-xs ring-1 ring-hairline dark:bg-surface-emph'
                : 'text-fg-body hover:text-fg'
            }`}
          >
            {option.label}
            <span className="ml-1 hidden tabular-nums text-fg-subtle @4xl:inline">{option.count}</span>
          </button>
        );
      })}
    </div>
  );
}

export default LogFilters;
