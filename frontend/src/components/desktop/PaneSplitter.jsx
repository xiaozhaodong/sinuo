// ============================================
// 桌面化组件 - 上下分隔条
// 2026-10-07
// ============================================
// 5px 命中区、1px 可见线（上下各借 2px，不额外占布局高度）。
// 拖动 / 双击行为由 useResizablePane 的 splitterProps 提供。

const PaneSplitter = ({ label = '调整面板高度', ...handlers }) => (
  <div
    role="separator"
    aria-orientation="horizontal"
    aria-label={label}
    title="拖动调整高度，双击恢复默认"
    className="relative z-[3] -my-0.5 h-[5px] shrink-0 cursor-row-resize touch-none after:absolute after:inset-x-0 after:top-0.5 after:h-px after:bg-line"
    {...handlers}
  />
);

export default PaneSplitter;
