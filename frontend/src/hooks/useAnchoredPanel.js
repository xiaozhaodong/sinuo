// 锚定在触发按钮下方的浮层（请求页 Codex / Claude 接入切换栏、页头主题菜单共用）：
// 定位到触发按钮下方，宽度不超过视口、贴边收敛，窗口尺寸或滚动变化时重算；
// 点外部或按 Esc 关闭。dismissible=false（如确认弹窗打开中）时暂停这两种关闭方式。

import { useEffect, useState } from 'react';

const VIEWPORT_MARGIN = 12;
const TRIGGER_GAP = 8;
const MIN_PANEL_HEIGHT = 120;
// 触发按钮贴近视口底部时，浮层至少留出这么高再往上收
const MIN_VISIBLE_HEIGHT = 180;

// placement='top' 时向上展开（侧边栏底部的主题菜单），用 bottom 定位，不必预先知道浮层高度
const useAnchoredPanel = ({ open, setOpen, triggerRef, panelRef, maxWidth, dismissible = true, placement = 'bottom' }) => {
  const [position, setPosition] = useState(null);

  useEffect(() => {
    if (!open) return undefined;
    const positionPanel = () => {
      const rect = triggerRef.current?.getBoundingClientRect();
      if (!rect) return;
      const width = Math.min(maxWidth, window.innerWidth - VIEWPORT_MARGIN * 2);
      const left = Math.max(VIEWPORT_MARGIN, Math.min(rect.left, window.innerWidth - width - VIEWPORT_MARGIN));
      if (placement === 'top') {
        setPosition({
          width,
          left,
          bottom: window.innerHeight - rect.top + TRIGGER_GAP,
          maxHeight: Math.max(MIN_PANEL_HEIGHT, rect.top - TRIGGER_GAP - VIEWPORT_MARGIN)
        });
        return;
      }
      const top = Math.min(rect.bottom + TRIGGER_GAP, Math.max(VIEWPORT_MARGIN, window.innerHeight - MIN_VISIBLE_HEIGHT));
      setPosition({
        width,
        top,
        left,
        maxHeight: Math.max(MIN_PANEL_HEIGHT, window.innerHeight - top - VIEWPORT_MARGIN)
      });
    };
    positionPanel();
    window.addEventListener('resize', positionPanel);
    window.addEventListener('scroll', positionPanel, true);
    return () => {
      window.removeEventListener('resize', positionPanel);
      window.removeEventListener('scroll', positionPanel, true);
    };
  }, [open, triggerRef, maxWidth, placement]);

  useEffect(() => {
    if (!open || !dismissible) return undefined;
    const outside = (event) => {
      if (!triggerRef.current?.contains(event.target) && !panelRef.current?.contains(event.target)) setOpen(false);
    };
    const escape = (event) => {
      if (event.key === 'Escape') {
        setOpen(false);
        triggerRef.current?.focus();
      }
    };
    document.addEventListener('mousedown', outside);
    document.addEventListener('keydown', escape);
    return () => {
      document.removeEventListener('mousedown', outside);
      document.removeEventListener('keydown', escape);
    };
  }, [open, dismissible, setOpen, triggerRef, panelRef]);

  return position;
};

export default useAnchoredPanel;
