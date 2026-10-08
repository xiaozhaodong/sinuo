// ============================================
// 桌面化组件 - 检查器（列表页右侧的详情 / 编辑面板）
// 2026-10-07
// ============================================
// 选中列表行后在这里就地查看与编辑，替代详情弹窗与抽屉。
// 结构：<Inspector> 内放 <InspectorHeader>、可滚动的 <InspectorBody>（若干 <InspectorSection>）、固定在底部的 <InspectorFooter>。

export const Inspector = ({ label, width, children }) => (
  <aside
    aria-label={label}
    className="flex w-80 shrink-0 flex-col border-l border-line bg-inspector"
    style={width ? { width } : undefined}
  >
    {children}
  </aside>
);

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
// 有多行值（如两行摘要）时传 align="start"，标签与值的首行对齐
const PROPERTY_ALIGN = { center: 'items-center', start: 'items-start' };

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
