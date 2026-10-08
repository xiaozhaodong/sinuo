// ============================================
// 桌面化组件 - 状态栏分页器
// 2026-10-07
// ============================================
// 「第 a–b 条 ‹ 页 / 总页 › 每页 N」，放在 <StatusBar> 右侧（前面加 <StatusSpacer />）。
// 每页条数用右键菜单同款弹出菜单：onOpenMenu 传 useContextMenu 的 openMenuAt（锚定在按钮上方）。

import { ChevronDown, ChevronLeft, ChevronRight } from 'lucide-react';

const PagerButton = ({ icon: Icon, title, disabled, onClick }) => (
  <button
    type="button"
    title={title}
    aria-label={title}
    disabled={disabled}
    onClick={onClick}
    className="inline-flex h-5 w-5 items-center justify-center rounded text-fg-muted hover:bg-surface-mut hover:text-fg disabled:opacity-35 disabled:hover:bg-transparent"
  >
    <Icon size={14} />
  </button>
);

/**
 * @param {Object} props
 * @param {number} props.currentPage - 当前页码（从 1 开始）
 * @param {number} props.pageSize - 每页条数
 * @param {number} props.total - 总条数
 * @param {number[]} props.pageSizeOptions - 可选的每页条数
 * @param {string} props.unit - 计数单位（条 / 个）
 * @param {Function} props.onPageChange - (page) => void
 * @param {Function} props.onPageSizeChange - (size) => void
 * @param {Function} props.onOpenMenu - (anchorElement, items) => void
 */
const StatusPager = ({
  currentPage = 1,
  pageSize = 10,
  total = 0,
  pageSizeOptions = [10, 20, 50, 100],
  unit = '条',
  onPageChange,
  onPageSizeChange,
  onOpenMenu
}) => {
  const totalPages = Math.max(1, Math.ceil(total / pageSize));
  const startItem = total === 0 ? 0 : (currentPage - 1) * pageSize + 1;
  const endItem = Math.min(currentPage * pageSize, total);

  const openPageSizeMenu = (event) => {
    onOpenMenu?.(event.currentTarget, pageSizeOptions.map((size) => ({
      label: `每页 ${size} ${unit}`,
      checked: size === pageSize,
      onSelect: () => onPageSizeChange?.(size)
    })));
  };

  return (
    <div className="flex items-center gap-1">
      <span className="mr-1 tabular-nums">
        {total === 0 ? '无记录' : <>第 <b className="font-medium text-fg-body">{startItem.toLocaleString()}–{endItem.toLocaleString()}</b> {unit}</>}
      </span>
      <PagerButton icon={ChevronLeft} title="上一页" disabled={currentPage <= 1} onClick={() => onPageChange?.(currentPage - 1)} />
      <span className="min-w-[48px] text-center tabular-nums text-fg-body">{currentPage} / {totalPages}</span>
      <PagerButton icon={ChevronRight} title="下一页" disabled={currentPage >= totalPages} onClick={() => onPageChange?.(currentPage + 1)} />
      <button
        type="button"
        onClick={openPageSizeMenu}
        className="ml-1 inline-flex h-5 items-center gap-0.5 rounded px-1.5 hover:bg-surface-mut hover:text-fg"
        title="每页条数"
      >
        每页 {pageSize}
        <ChevronDown size={12} />
      </button>
    </div>
  );
};

export default StatusPager;
