// ============================================
// 桌面化组件 - 底部状态栏
// 2026-10-07
// ============================================
// 列表页的计数、汇总统计放在这里，替代页面顶部的统计卡片。
// 用 <StatusSpacer /> 把右侧信息（最近刷新、当前命中等）推到最右。

export const StatusBar = ({ children }) => (
  <div className="flex h-[26px] shrink-0 items-center gap-3.5 overflow-hidden whitespace-nowrap border-t border-line bg-surface px-4 text-[11.5px] text-fg-muted">
    {children}
  </div>
);

export const StatusItem = ({ label, value, valueClassName = 'text-fg-body', title }) => (
  <span title={title}>
    {label}{label && value !== undefined ? ' ' : ''}
    {value !== undefined && <b className={`font-medium tabular-nums ${valueClassName}`}>{value}</b>}
  </span>
);

export const StatusSpacer = () => <span className="flex-1" aria-hidden="true" />;
