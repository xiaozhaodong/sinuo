// ============================================
// 布局组件 - 统一工具栏与桌面外壳布局状态
// 2026-10-07
// 2026-11：承接侧边栏折叠、右侧检查器显隐注册与快捷键面板状态
// ============================================
// 52px 高：macOS 统一工具栏的红绿灯实测落在 y≈26pt，侧边栏顶部与这里同高、同一中线。
// 标题由 App 按导航配置给出；副标题与操作按钮由页面用 <PageToolbar> 投送（portal），
// 随页面挂载 / 卸载，切页时不需要手动清理。
// 整条工具栏是窗口拖拽区（index.css 的 .app-drag），按钮、输入框等控件自动排除。

import { useCallback, useContext, useMemo, useState } from 'react';
import { createPortal } from 'react-dom';
import { PanelLeftOpen, PanelRight } from 'lucide-react';
import { readStorage, writeStorage } from '@components/desktop/storage.js';
import { formatAltShortcut, isMacPlatform } from '@utils/platform.js';
import { ToolbarButton, ToolbarSeparator } from './ToolbarButton.jsx';
import { ToolbarSlotsContext, useShellLayout } from './shellLayoutContext.js';

const SIDEBAR_COLLAPSED_KEY = 'shell.sidebarCollapsed';
const INSPECTOR_OPEN_KEY = 'shell.inspectorOpen';

export const ToolbarProvider = ({ children }) => {
  const [subtitleSlot, setSubtitleSlot] = useState(null);
  const [toolsSlot, setToolsSlot] = useState(null);
  const [sidebarCollapsed, setSidebarCollapsedState] = useState(() => readStorage(SIDEBAR_COLLAPSED_KEY) === 'true');
  const [inspectorOpen, setInspectorOpenState] = useState(() => readStorage(INSPECTOR_OPEN_KEY) !== 'false');
  const [inspectorCount, setInspectorCount] = useState(0);
  const [shortcutsOpen, setShortcutsOpen] = useState(false);

  const setSidebarCollapsed = useCallback((next) => {
    setSidebarCollapsedState((current) => {
      const resolved = typeof next === 'function' ? next(current) : Boolean(next);
      writeStorage(SIDEBAR_COLLAPSED_KEY, resolved);
      return resolved;
    });
  }, []);

  const toggleSidebar = useCallback(() => {
    setSidebarCollapsed((current) => !current);
  }, [setSidebarCollapsed]);

  const setInspectorOpen = useCallback((next) => {
    setInspectorOpenState((current) => {
      const resolved = typeof next === 'function' ? next(current) : Boolean(next);
      writeStorage(INSPECTOR_OPEN_KEY, resolved);
      return resolved;
    });
  }, []);

  const toggleInspector = useCallback(() => {
    setInspectorOpen((current) => !current);
  }, [setInspectorOpen]);

  const openInspector = useCallback(() => {
    setInspectorOpen(true);
  }, [setInspectorOpen]);

  const registerInspector = useCallback(() => {
    setInspectorCount((count) => count + 1);
    return () => {
      setInspectorCount((count) => Math.max(0, count - 1));
    };
  }, []);

  const value = useMemo(
    () => ({
      subtitleSlot,
      toolsSlot,
      setSubtitleSlot,
      setToolsSlot,
      sidebarCollapsed,
      toggleSidebar,
      inspectorOpen,
      toggleInspector,
      openInspector,
      hasInspector: inspectorCount > 0,
      registerInspector,
      shortcutsOpen,
      setShortcutsOpen
    }),
    [
      subtitleSlot,
      toolsSlot,
      sidebarCollapsed,
      toggleSidebar,
      inspectorOpen,
      toggleInspector,
      openInspector,
      inspectorCount,
      registerInspector,
      shortcutsOpen
    ]
  );

  return <ToolbarSlotsContext.Provider value={value}>{children}</ToolbarSlotsContext.Provider>;
};

// eslint-disable-next-line react-refresh/only-export-components
export { useShellLayout };

// sidebarNotice：侧边栏收起后，原本在侧边栏底部的提示（如有新版本）改挂在「显示侧边栏」按钮上
export const AppToolbar = ({ title, sidebarNotice = '' }) => {
  const {
    setSubtitleSlot,
    setToolsSlot,
    sidebarCollapsed,
    toggleSidebar,
    hasInspector,
    inspectorOpen,
    toggleInspector
  } = useContext(ToolbarSlotsContext);
  const mac = isMacPlatform();
  // macOS 下侧边栏收起后，工具栏左缘要让出系统红绿灯（x≈19–79pt）
  const leftPadding = sidebarCollapsed ? (mac ? 'pl-[84px]' : 'pl-3') : 'pl-5';

  return (
    <div data-app-toolbar className={`app-drag relative z-30 flex h-[52px] shrink-0 items-center gap-3 border-b border-line bg-surface pr-3 ${leftPadding}`}>
      {sidebarCollapsed && (
        <span className="relative inline-flex shrink-0">
          <ToolbarButton
            icon={PanelLeftOpen}
            title={`显示侧边栏  ${formatAltShortcut('S')}${sidebarNotice ? `（${sidebarNotice}）` : ''}`}
            onClick={toggleSidebar}
          />
          {sidebarNotice && <span aria-hidden="true" className="pointer-events-none absolute right-0.5 top-0.5 h-1.5 w-1.5 rounded-full bg-accent-ring" />}
        </span>
      )}
      <div className="flex min-w-0 shrink-0 flex-col justify-center">
        <h1 className="toolbar-title truncate text-[15px] font-semibold leading-5 tracking-tight text-fg">{title}</h1>
        <div ref={setSubtitleSlot} className="truncate text-[11px] leading-4 text-fg-muted empty:hidden" />
      </div>
      {/* @container：页面工具栏按自身可用宽度收放（侧边栏占去 220px，视口断点不再准确） */}
      <div ref={setToolsSlot} className="@container flex min-w-0 flex-1 items-center gap-2" />
      {hasInspector && (
        <>
          <ToolbarSeparator />
          <ToolbarButton
            icon={PanelRight}
            title={`${inspectorOpen ? '隐藏' : '显示'}右侧详情面板  ${formatAltShortcut('I')}`}
            pressed={inspectorOpen}
            onClick={toggleInspector}
          />
        </>
      )}
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
