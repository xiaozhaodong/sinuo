// ============================================
// 内容区定位：抽屉 / 侧滑面板贴在内容区内（工具栏下方、侧边栏右侧）
// 2026-10-07
// ============================================
// 只读抽屉是非模态的，遮罩与面板都不该盖住侧边栏和工具栏（与原来「从页头下方滑出」的惯例一致）。
// 工具栏根节点带 data-app-toolbar，见 AppToolbar.jsx。

export const getContentAreaInsets = () => {
  if (typeof document === 'undefined') return { top: 0, left: 0 };
  const toolbar = document.querySelector('[data-app-toolbar]');
  if (!toolbar) return { top: 0, left: 0 };
  const rect = toolbar.getBoundingClientRect();
  return { top: rect.bottom, left: rect.left };
};
