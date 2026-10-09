// ============================================
// 右键菜单 hook：返回待渲染的菜单节点与打开函数（openContextMenu 跟随光标，openMenuAt 锚定按钮）
// 2026-10-07
// 2026-10-09 锚定菜单记住触发元素：菜单开着时再点同一个按钮即关闭
// ============================================

import { useCallback, useState } from 'react';
import ContextMenu from './ContextMenu.jsx';

// 同一个触发元素再次点击时关闭，否则换成新菜单
const toggleAnchoredMenu = (anchorElement, items) => (current) => (
  current?.anchorElement === anchorElement
    ? null
    : { anchorElement, anchorRect: anchorElement.getBoundingClientRect(), items: items.filter(Boolean) }
);

const useContextMenu = () => {
  const [menu, setMenu] = useState(null);
  const closeContextMenu = useCallback(() => setMenu(null), []);

  // 右键跟随光标；由点击（行尾「更多」按钮、键盘）触发时没有有意义的光标位置，锚定到触发元素
  const openContextMenu = useCallback((event, items) => {
    event.preventDefault();
    event.stopPropagation();
    if (event.type !== 'contextmenu' && event.currentTarget instanceof Element) {
      setMenu(toggleAnchoredMenu(event.currentTarget, items));
      return;
    }
    setMenu({ x: event.clientX, y: event.clientY, items: items.filter(Boolean) });
  }, []);

  // 弹出按钮：菜单锚定在按钮下方（放不下时在上方）
  const openMenuAt = useCallback((anchorElement, items) => {
    setMenu(toggleAnchoredMenu(anchorElement, items));
  }, []);

  const contextMenu = menu
    ? (
      <ContextMenu
        x={menu.x}
        y={menu.y}
        anchorRect={menu.anchorRect}
        anchorElement={menu.anchorElement}
        items={menu.items}
        onClose={closeContextMenu}
      />
    )
    : null;

  return { contextMenu, openContextMenu, openMenuAt, closeContextMenu };
};

export default useContextMenu;
