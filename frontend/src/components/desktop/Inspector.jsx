// ============================================
// 桌面化组件 - 检查器（列表页右侧的详情 / 编辑面板）
// 2026-10-07
// 2026-11：支持左缘拖拽调宽（双击复位）与全局显隐折叠（工具栏按钮 / ⌥⌘I）
// ============================================
// 选中列表行后在这里就地查看与编辑，替代详情弹窗与抽屉。
// 结构：<Inspector> 内放 <InspectorHeader>、可滚动的 <InspectorBody>（若干 <InspectorSection>）、固定在底部的 <InspectorFooter>。

import { useEffect, useRef } from 'react';
import { useShellLayout } from '../layout/shellLayoutContext.js';
import PaneSplitter from './PaneSplitter.jsx';
import useResizablePane from './useResizablePane.js';

const DEFAULT_INSPECTOR_WIDTH = 320;
const MIN_INSPECTOR_WIDTH = 260;
const MAX_INSPECTOR_WIDTH = 520;
const MIN_TABLE_REMAINING = 360;

export const Inspector = ({
  label,
  width,
  storageKey,
  resizable = true,
  collapsible = true,
  forceOpen = false,
  children
}) => {
  const shell = useShellLayout();
  // 只取用到的字段做依赖：shell 对象随任何外壳状态变化而换新，整体依赖会反复注销再注册
  const registerInspector = shell?.registerInspector;
  const openInspector = shell?.openInspector;
  const inspectorOpen = shell ? shell.inspectorOpen : true;
  const markerRef = useRef(null);
  const resolvedDefault = width || DEFAULT_INSPECTOR_WIDTH;
  const pane = useResizablePane({
    orientation: 'vertical',
    storageKey: `inspector.${storageKey || label || 'default'}`,
    defaultSize: resolvedDefault,
    minSize: MIN_INSPECTOR_WIDTH,
    maxSize: MAX_INSPECTOR_WIDTH,
    minRemaining: MIN_TABLE_REMAINING
  });

  // 仅当当前 Inspector 不处于隐藏的 tabpanel 内时，才向工具栏注册「显示 / 隐藏详情面板」按钮。
  useEffect(() => {
    if (!collapsible || !registerInspector) return undefined;
    const marker = markerRef.current;
    if (!marker) return undefined;

    let unregister = null;
    const syncRegistration = () => {
      const isHidden = Boolean(marker.closest('[hidden]'));
      if (!isHidden && !unregister) {
        unregister = registerInspector();
      } else if (isHidden && unregister) {
        unregister();
        unregister = null;
      }
    };

    syncRegistration();
    const tabPanel = marker.closest('[role="tabpanel"]');
    let observer = null;
    if (tabPanel && typeof MutationObserver !== 'undefined') {
      observer = new MutationObserver(syncRegistration);
      observer.observe(tabPanel, { attributes: true, attributeFilter: ['hidden'] });
    }

    return () => {
      observer?.disconnect();
      if (unregister) {
        unregister();
        unregister = null;
      }
    };
  }, [collapsible, registerInspector]);

  // 新建表单在检查器内承载时，触发新建自动展开已折叠的检查器
  useEffect(() => {
    if (forceOpen && !inspectorOpen) openInspector?.();
  }, [forceOpen, inspectorOpen, openInspector]);

  const isCollapsed = collapsible && !inspectorOpen;

  // 折叠时只隐藏、不卸载：检查器里的表单草稿留在组件内部，卸载会静默丢掉未保存的修改
  return (
    <>
      <span ref={markerRef} aria-hidden="true" className="hidden" />
      {resizable && !isCollapsed && (
        <PaneSplitter
          {...pane.splitterProps}
          label={`调整${label || '详情面板'}宽度`}
        />
      )}
      <aside
        aria-label={label}
        hidden={isCollapsed}
        className={`flex max-w-[55%] shrink-0 flex-col bg-inspector ${resizable ? '' : 'border-l border-line'}`}
        style={{ width: resizable ? pane.width : resolvedDefault }}
      >
        {children}
      </aside>
    </>
  );
};

export const InspectorHeader = ({ title, subtitle, badges = null }) => (
  <div className="shrink-0 border-b border-line px-4 pb-3 pt-3.5">
    <div className="flex min-w-0 items-center gap-2">
      <h2 className="truncate text-[15px] font-semibold text-fg" title={typeof title === 'string' ? title : undefined}>{title}</h2>
      {badges}
    </div>
    {subtitle && (
      <div className="mt-0.5 truncate font-mono text-[11.5px] text-fg-muted" title={typeof subtitle === 'string' ? subtitle : undefined}>
        {subtitle}
      </div>
    )}
  </div>
);

export const InspectorBody = ({ children }) => (
  <div className="min-h-0 flex-1 overflow-y-auto">{children}</div>
);

export const InspectorSection = ({ title, action = null, children }) => (
  <section className="border-b border-line px-4 pb-3.5 pt-3 last:border-b-0">
    {(title || action) && (
      <div className="mb-2 flex items-center justify-between gap-2">
        <h3 className="text-[11.5px] font-semibold text-fg-muted">{title}</h3>
        {action}
      </div>
    )}
    {children}
  </section>
);

// 属性列表：左侧右对齐的标签 + 右侧值；值可以是文本，也可以是输入控件。
// 多行内容可按顶边对齐；首行混排文字和徽标时按文字基线对齐。
const PROPERTY_ALIGN = { center: 'items-center', start: 'items-start', baseline: 'items-baseline' };

export const PropertyList = ({ children, labelWidth = 84, align = 'center' }) => (
  <dl
    className={`grid gap-x-2.5 gap-y-2 text-[12.5px] ${PROPERTY_ALIGN[align] || PROPERTY_ALIGN.center}`}
    style={{ gridTemplateColumns: `${labelWidth}px minmax(0, 1fr)` }}
  >
    {children}
  </dl>
);

// 值是输入控件时传 truncate={false}：truncate 带 overflow:hidden，会裁掉输入框的聚焦光环
export const Property = ({ label, title, truncate = true, children }) => (
  <>
    <dt className="text-right text-fg-muted">{label}</dt>
    <dd className={`min-w-0 text-fg ${truncate ? 'truncate' : ''}`} title={title}>{children}</dd>
  </>
);

export const InspectorFooter = ({ children }) => (
  <div className="flex shrink-0 items-center gap-2 border-t border-line px-4 py-2.5">{children}</div>
);

export const InspectorEmpty = ({ icon: Icon, children }) => (
  <div className="flex flex-1 flex-col items-center justify-center gap-2.5 p-6 text-center text-[13px] text-fg-subtle">
    {Icon && <Icon size={32} strokeWidth={1.25} />}
    <div>{children}</div>
  </div>
);
