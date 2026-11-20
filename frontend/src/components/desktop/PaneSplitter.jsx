// ============================================
// 桌面化组件 - 上下分隔条
// 2026-10-07
// ============================================
// 7px 命中区、1px 可见线（上下各借 3px，不额外占布局高度）；
// 悬停或拖动时分隔线轻度提亮并浮现居中微抓手，提示可拖拽调高。
// 拖动 / 双击行为由 useResizablePane 的 splitterProps 提供。

const PaneSplitter = ({ label = '调整面板高度', ...handlers }) => (
  <div
    role="separator"
    aria-orientation="horizontal"
    aria-label={label}
    title="拖动调整高度，双击恢复默认"
    className="group relative z-[3] -my-[3px] h-[7px] shrink-0 cursor-row-resize touch-none before:absolute before:inset-x-0 before:top-[3px] before:h-px before:bg-line before:transition-[background-color,height,top] hover:before:top-[2.5px] hover:before:h-[2px] hover:before:bg-accent-ring/55 data-[dragging=true]:before:top-[2.5px] data-[dragging=true]:before:h-[2px] data-[dragging=true]:before:bg-accent-ring/70 after:pointer-events-none after:absolute after:left-1/2 after:top-1/2 after:h-[3px] after:w-9 after:-translate-x-1/2 after:-translate-y-1/2 after:scale-x-60 after:rounded-full after:bg-accent-ring after:opacity-0 after:motion-safe:transition-[opacity,transform] hover:after:scale-x-100 hover:after:opacity-90 data-[dragging=true]:after:scale-x-100 data-[dragging=true]:after:opacity-100"
    {...handlers}
  />
);

export default PaneSplitter;
