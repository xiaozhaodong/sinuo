// ============================================
// 视图切换(表格 / 网格)分段控件
// 端点页与账号池共用,选择持久化在各页面自己的 localStorage key 里
// 2026-10-07 桌面化：compact 为 24px 高的分段控件，与范围条里的其它控件同高（外观同 SegmentedControl）
// ============================================

import { LayoutGrid, LayoutList } from 'lucide-react';

const ViewModeSwitcher = ({ value = 'table', onChange, compact = false }) => {
  const options = [
    { key: 'table', label: '表格', icon: LayoutList },
    { key: 'grid', label: '网格', icon: LayoutGrid }
  ];
  return (
    <div
      role="group"
      aria-label="视图切换"
      className={`inline-flex items-center gap-0.5 p-0.5 ${compact ? 'rounded-[7px] bg-surface-mut' : 'rounded-lg border border-line bg-surface-mut/80'}`}
    >
      {options.map(({ key, label, icon: Icon }) => (
        <button
          key={key}
          type="button"
          onClick={() => onChange?.(key)}
          aria-pressed={value === key}
          title={key === 'table' ? '表格视图' : '网格卡片视图'}
          className={`inline-flex items-center text-xs font-medium transition ${
            compact ? 'h-5 w-6 justify-center rounded-[5px]' : 'gap-1 rounded-md px-2.5 py-1'
          } ${
            value === key
              ? (compact ? 'bg-surface text-fg shadow-xs ring-1 ring-hairline dark:bg-surface-emph' : 'bg-surface text-accent shadow-sm')
              : 'text-fg-muted hover:text-fg-body'
          }`}
        >
          <Icon size={13} />
          <span className={compact ? 'sr-only' : ''}>{label}</span>
        </button>
      ))}
    </div>
  );
};

export default ViewModeSwitcher;
