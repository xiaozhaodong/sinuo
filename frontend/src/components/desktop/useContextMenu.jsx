// ============================================
// 右键菜单 hook：返回待渲染的菜单节点与打开函数（openContextMenu 跟随光标，openMenuAt 锚定按钮）
// 2026-10-07
// ============================================

import { useCallback, useState } from 'react';
import ContextMenu from './ContextMenu.jsx';

const useContextMenu = () => {
  const [menu, setMenu] = useState(null);
  const closeContextMenu = useCallback(() => setMenu(null), []);

  // 右键跟随光标；由点击（行尾「更多」按钮、键盘）触发时没有有意义的光标位置，锚定到触发元素
  const openContextMenu = useCallback((event, items) => {
    event.preventDefault();
    event.stopPropagation();
    const visibleItems = items.filter(Boolean);
    if (event.type !== 'contextmenu' && event.currentTarget instanceof Element) {
      setMenu({ anchorRect: event.currentTarget.getBoundingClientRect(), items: visibleItems });
      return;
    }
    setMenu({ x: event.clientX, y: event.clientY, items: visibleItems });
  }, []);

  // 弹出按钮：菜单锚定在按钮下方（放不下时在上方）
  const openMenuAt = useCallback((anchorElement, items) => {
    setMenu({ anchorRect: anchorElement.getBoundingClientRect(), items: items.filter(Boolean) });
  }, []);

  const contextMenu = menu
    ? <ContextMenu x={menu.x} y={menu.y} anchorRect={menu.anchorRect} items={menu.items} onClose={closeContextMenu} />
    : null;

  return { contextMenu, openContextMenu, openMenuAt, closeContextMenu };
};

export default useContextMenu;
