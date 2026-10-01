// ============================================
// 管理页顶部页签条（Codex 管理 / Claude 管理共用）
// 2026-09-24
// ============================================
// 按钮与面板通过 `${idPrefix}-tab-${id}` / `${idPrefix}-panel-${id}` 关联，
// 面板由页面自己渲染并设置 role="tabpanel"。

const PageTabs = ({ tabs, activeTab, onChange, idPrefix, ariaLabel, size = 'md' }) => (
  <div
    role="tablist"
    aria-label={ariaLabel}
    className="inline-flex shrink-0 items-center gap-1 rounded-lg border border-line bg-surface-sub p-1"
  >
    {tabs.map((tab) => {
      const Icon = tab.icon;
      const selected = activeTab === tab.id;
      const sizeClass = size === 'sm' ? 'rounded-md px-3 py-1 text-xs' : 'rounded-md px-3 py-1.5 text-sm';
      return (
        <button
          key={tab.id}
          id={`${idPrefix}-tab-${tab.id}`}
          type="button"
          role="tab"
          aria-selected={selected}
          aria-controls={`${idPrefix}-panel-${tab.id}`}
          onClick={() => onChange?.(tab.id)}
          className={`inline-flex items-center gap-2 ${sizeClass} font-medium transition-colors ${
            selected
              ? 'bg-surface text-accent shadow-sm'
              : 'text-fg-muted hover:bg-surface/60 hover:text-fg-body'
          }`}
        >
          {Icon && <Icon size={size === 'sm' ? 13 : 15} className="shrink-0" />}
          {tab.label}
        </button>
      );
    })}
  </div>
);

export default PageTabs;
