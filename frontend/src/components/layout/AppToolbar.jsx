// ============================================
// 布局组件 - 统一工具栏
// 2026-10-07
// ============================================
// 52px 高：macOS 统一工具栏的红绿灯实测落在 y≈26pt，侧边栏顶部与这里同高、同一中线。
// 标题由 App 按导航配置给出；副标题与操作按钮由页面用 <PageToolbar> 投送（portal），
// 随页面挂载 / 卸载，切页时不需要手动清理。
// 整条工具栏是窗口拖拽区（index.css 的 .app-drag），按钮、输入框等控件自动排除。

import { createContext, useContext, useMemo, useState } from 'react';
import { createPortal } from 'react-dom';

const ToolbarSlotsContext = createContext(null);

export const ToolbarProvider = ({ children }) => {
  const [subtitleSlot, setSubtitleSlot] = useState(null);
  const [toolsSlot, setToolsSlot] = useState(null);
  const value = useMemo(
    () => ({ subtitleSlot, toolsSlot, setSubtitleSlot, setToolsSlot }),
    [subtitleSlot, toolsSlot]
  );

  return <ToolbarSlotsContext.Provider value={value}>{children}</ToolbarSlotsContext.Provider>;
};

export const AppToolbar = ({ title }) => {
  const { setSubtitleSlot, setToolsSlot } = useContext(ToolbarSlotsContext);

  return (
    <div data-app-toolbar className="app-drag relative z-30 flex h-[52px] shrink-0 items-center gap-3 border-b border-line bg-surface pl-5 pr-3">
      <div className="flex min-w-0 shrink-0 flex-col justify-center">
        <h1 className="toolbar-title truncate text-[15px] font-semibold leading-5 tracking-tight text-fg">{title}</h1>
        <div ref={setSubtitleSlot} className="truncate text-[11px] leading-4 text-fg-muted empty:hidden" />
      </div>
      {/* @container：页面工具栏按自身可用宽度收放（侧边栏占去 220px，视口断点不再准确） */}
      <div ref={setToolsSlot} className="@container flex min-w-0 flex-1 items-center gap-2" />
    </div>
  );
};

// 页面投送工具栏内容：subtitle 放在标题下方，children 放在标题右侧（用 <ToolbarSpacer /> 把操作推到最右）
export const PageToolbar = ({ subtitle = null, children = null }) => {
  const slots = useContext(ToolbarSlotsContext);
  if (!slots) return null;

  return (
    <>
      {subtitle !== null && slots.subtitleSlot && createPortal(subtitle, slots.subtitleSlot)}
      {children !== null && slots.toolsSlot && createPortal(children, slots.toolsSlot)}
    </>
  );
};
