// ============================================
// 管理页子页签（Codex 管理 / Claude 管理共用）
// 2026-09-24
// 2026-10-07：放进统一工具栏，改为分段控件样式
// ============================================
// 按钮与面板通过 `${idPrefix}-tab-${id}` / `${idPrefix}-panel-${id}` 关联，
// 面板由页面自己渲染并设置 role="tabpanel"。

const PageTabs = ({ tabs, activeTab, onChange, idPrefix, ariaLabel }) => (
  <div
    role="tablist"
    aria-label={ariaLabel}
    className="inline-flex shrink-0 items-center rounded-[7px] bg-surface-mut p-0.5"
  >
    {tabs.map((tab) => {
      const selected = activeTab === tab.id;
      return (
        <button
          key={tab.id}
          id={`${idPrefix}-tab-${tab.id}`}
          type="button"
          role="tab"
          aria-selected={selected}
          aria-controls={`${idPrefix}-panel-${tab.id}`}
          onClick={() => onChange?.(tab.id)}
          className={`h-[22px] whitespace-nowrap rounded-[5px] px-2.5 text-xs transition-colors ${
            selected
              ? 'bg-surface font-medium text-fg shadow-xs ring-1 ring-hairline dark:bg-surface-emph'
              : 'text-fg-body hover:text-fg'
          }`}
        >
          {tab.label}
        </button>
      );
    })}
  </div>
);

export default PageTabs;
