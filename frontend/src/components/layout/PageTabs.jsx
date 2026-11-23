// ============================================
// 管理页子页签（Codex 管理 / Claude 管理共用）
// 2026-09-24
// 2026-10-07：放进统一工具栏，改为分段控件样式
// ============================================
// 按钮与面板通过 `${idPrefix}-tab-${id}` / `${idPrefix}-panel-${id}` 关联，
// 面板由页面自己渲染并设置 role="tabpanel"。支持 ← / → / Home / End 方向键切换。

import { useRef } from 'react';

const PageTabs = ({ tabs, activeTab, onChange, idPrefix, ariaLabel }) => {
  const listRef = useRef(null);

  const handleKeyDown = (event, index) => {
    if (!tabs.length) return;
    let nextIndex = null;
    if (event.key === 'ArrowRight') nextIndex = (index + 1) % tabs.length;
    else if (event.key === 'ArrowLeft') nextIndex = (index - 1 + tabs.length) % tabs.length;
    else if (event.key === 'Home') nextIndex = 0;
    else if (event.key === 'End') nextIndex = tabs.length - 1;
    if (nextIndex === null) return;

    event.preventDefault();
    const nextTab = tabs[nextIndex];
    if (!nextTab) return;
    onChange?.(nextTab.id);
    listRef.current?.querySelector(`#${CSS.escape(`${idPrefix}-tab-${nextTab.id}`)}`)?.focus();
  };

  return (
    <div
      ref={listRef}
      role="tablist"
      aria-label={ariaLabel}
      className="inline-flex shrink-0 items-center rounded-[7px] bg-surface-mut p-0.5"
    >
      {tabs.map((tab, index) => {
        const selected = activeTab === tab.id;
        return (
          <button
            key={tab.id}
            id={`${idPrefix}-tab-${tab.id}`}
            type="button"
            role="tab"
            tabIndex={selected ? 0 : -1}
            aria-selected={selected}
            aria-controls={`${idPrefix}-panel-${tab.id}`}
            onClick={() => onChange?.(tab.id)}
            onKeyDown={(event) => handleKeyDown(event, index)}
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
};

export default PageTabs;
