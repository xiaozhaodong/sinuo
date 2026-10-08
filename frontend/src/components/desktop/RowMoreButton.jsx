// ============================================
// 桌面化组件 - 行尾「更多」按钮
// 2026-10-07
// ============================================
// 与右键菜单共用一套菜单项：onOpenMenu 直接传行的右键处理函数（useContextMenu 的
// openContextMenu 遇到点击事件会把菜单锚定在按钮下方）。

import { MoreHorizontal } from 'lucide-react';

const RowMoreButton = ({ onOpenMenu, label = '更多操作' }) => (
  <button
    type="button"
    aria-label={label}
    aria-haspopup="menu"
    title={label}
    onClick={onOpenMenu}
    className="inline-flex h-6 w-6 items-center justify-center rounded text-fg-muted hover:bg-surface-mut hover:text-fg"
  >
    <MoreHorizontal size={15} />
  </button>
);

export default RowMoreButton;
