// ============================================
// 桌面化组件 - 面板（浅灰底上的白色内容框）
// 2026-10-07
// ============================================
// 概览等仪表盘页用：细边框 + 8px 圆角，不加阴影；34px 标题栏放标题、副标题与右侧操作。

export const Panel = ({ title, subtitle, actions = null, className = '', bodyClassName = '', children }) => (
  <section className={`flex min-w-0 flex-col overflow-hidden rounded-lg border border-line bg-surface ${className}`}>
    {(title || actions) && (
      <header className="flex h-[34px] shrink-0 items-center gap-2 border-b border-line-soft px-3">
        {title && <h3 className="shrink-0 text-[13px] font-semibold text-fg">{title}</h3>}
        {subtitle && <span className="min-w-0 truncate text-xs text-fg-muted">{subtitle}</span>}
        {actions && <div className="ml-auto flex shrink-0 items-center gap-2">{actions}</div>}
      </header>
    )}
    <div className={`min-h-0 flex-1 ${bodyClassName}`}>{children}</div>
  </section>
);

export default Panel;
