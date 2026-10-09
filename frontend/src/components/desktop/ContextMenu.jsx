// ============================================
// 桌面化组件 - 右键菜单
// 2026-10-07
// ============================================
// 用法（经 useContextMenu.jsx）：
//   const { contextMenu, openContextMenu } = useContextMenu();
//   <tr onContextMenu={(event) => openContextMenu(event, [{ label: '复制', onSelect }, '-', …])}>
//   …{contextMenu}
// 菜单项：{ label, shortcut?, checked?, danger?, disabled?, onSelect }、{ header }（不可聚焦的标题）或 '-'（分隔线）。
// 任一项带 checked（单选列表，如每页条数）时整张菜单留出勾选列，未选中项对齐留空。
// 点外部、按 Esc、外部滚动、窗口失焦都会关闭；↑↓ / Home / End 导航，Enter 选择。
// 位置贴着光标，越界时向左 / 向上收。
// 弹出按钮（筛选、每页条数）用 openMenuAt(按钮元素, items)：菜单对齐按钮左下，下方放不下时翻到按钮上方。
// 锚定菜单开着时再点同一个触发按钮即关闭：该按钮上的 mousedown 不算「点外部」，交给按钮 click 切换。

import { useEffect, useLayoutEffect, useRef } from 'react';
import { Check } from 'lucide-react';
import { createPortal } from 'react-dom';
import { isListNavigationKey, nextListId } from './listNavigation.js';

const VIEWPORT_MARGIN = 6;
const ANCHOR_GAP = 4;

const clamp = (value, min, max) => Math.max(min, Math.min(value, max));

const ContextMenu = ({ x, y, anchorRect = null, anchorElement = null, items, onClose }) => {
  const menuRef = useRef(null);
  const hasCheckColumn = items.some((item) => item !== '-' && item.checked !== undefined);

  // 关闭后恢复触发按钮的键盘焦点；若用户已点到其他控件，则不抢回焦点。
  useLayoutEffect(() => {
    const previousFocus = document.activeElement;
    const menu = menuRef.current;
    return () => {
      if (menu?.contains(document.activeElement) || document.activeElement === document.body) {
        if (previousFocus?.isConnected) previousFocus.focus({ preventScroll: true });
      }
    };
  }, []);

  // 先按光标 / 按钮位置渲染，量出尺寸后在绘制前直接改 DOM 位置收回视口内（不经过 state，避免二次渲染）
  useLayoutEffect(() => {
    const menu = menuRef.current;
    if (!menu) return;
    const rect = menu.getBoundingClientRect();
    let left = x;
    let top = y;
    if (anchorRect) {
      left = anchorRect.left;
      const below = anchorRect.bottom + ANCHOR_GAP;
      top = below + rect.height + VIEWPORT_MARGIN <= window.innerHeight ? below : anchorRect.top - rect.height - ANCHOR_GAP;
    }
    menu.style.left = `${clamp(left, VIEWPORT_MARGIN, window.innerWidth - rect.width - VIEWPORT_MARGIN)}px`;
    menu.style.top = `${clamp(top, VIEWPORT_MARGIN, window.innerHeight - rect.height - VIEWPORT_MARGIN)}px`;
    menu.focus();
  }, [x, y, anchorRect]);

  useEffect(() => {
    const handlePointer = (event) => {
      if (menuRef.current?.contains(event.target) || anchorElement?.contains(event.target)) return;
      onClose();
    };
    const handleKey = (event) => {
      if (event.key === 'Escape') onClose();
    };
    const handleScroll = (event) => {
      if (!menuRef.current?.contains(event.target)) onClose();
    };
    document.addEventListener('mousedown', handlePointer);
    document.addEventListener('keydown', handleKey);
    window.addEventListener('blur', onClose);
    window.addEventListener('scroll', handleScroll, true);
    window.addEventListener('resize', onClose);
    return () => {
      document.removeEventListener('mousedown', handlePointer);
      document.removeEventListener('keydown', handleKey);
      window.removeEventListener('blur', onClose);
      window.removeEventListener('scroll', handleScroll, true);
      window.removeEventListener('resize', onClose);
    };
  }, [anchorElement, onClose]);

  const handleNavigation = (event) => {
    if (event.key === 'Tab') {
      onClose();
      return;
    }
    if (!isListNavigationKey(event.key)) return;
    event.preventDefault();
    event.stopPropagation();
    const buttons = Array.from(menuRef.current.querySelectorAll('button:not(:disabled)'));
    const next = nextListId(buttons, document.activeElement, event.key);
    next?.focus({ preventScroll: true });
    next?.scrollIntoView({ block: 'nearest' });
  };

  return createPortal(
    <div
      ref={menuRef}
      role="menu"
      tabIndex={-1}
      style={anchorRect ? { left: anchorRect.left, top: anchorRect.bottom + ANCHOR_GAP } : { left: x, top: y }}
      onContextMenu={(event) => event.preventDefault()}
      onKeyDown={handleNavigation}
      className="fixed z-[130] max-h-[min(420px,70vh)] min-w-[196px] overflow-y-auto rounded-lg border border-line bg-surface/95 p-1 text-[13px] shadow-xl ring-1 ring-hairline backdrop-blur-xl focus:outline-none"
    >
      {items.map((item, index) => (item === '-'
        ? <div key={`separator-${index}`} role="separator" className="mx-1.5 my-1 h-px bg-line" />
        : item.header ? (
          <div key={`header-${index}`} role="presentation" className="px-2 py-1 text-[11.5px] text-fg-muted">
            {item.header}
          </div>
        ) : (
          <button
            key={`${index}-${item.label}`}
            type="button"
            role={item.checked !== undefined ? 'menuitemradio' : 'menuitem'}
            aria-checked={item.checked}
            disabled={item.disabled}
            onClick={() => {
              onClose();
              item.onSelect?.();
            }}
            className={`group flex h-6 w-full items-center gap-2 whitespace-nowrap rounded px-2 text-left hover:bg-indigo-600 hover:text-white focus-visible:bg-indigo-600 focus-visible:text-white focus-visible:outline-none disabled:pointer-events-none disabled:opacity-40 ${
              item.danger ? 'text-danger' : 'text-fg'
            }`}
          >
            {hasCheckColumn && (
              <span className="flex w-3 shrink-0 justify-center">{item.checked && <Check size={12} strokeWidth={2.25} />}</span>
            )}
            <span className="flex-1">{item.label}</span>
            {item.shortcut && <span className="pl-4 text-xs text-fg-subtle group-hover:text-white/75 group-focus-visible:text-white/75">{item.shortcut}</span>}
          </button>
        )))}
    </div>,
    document.body
  );
};

export default ContextMenu;
