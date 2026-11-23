// ============================================
// 桌面化组件 - 面板分隔条（支持上下 / 左右拖拽）
// 2026-10-07
// ============================================
// 7px 命中区、1px 可见线（两侧各借 3px，不额外占布局空间）；
// 悬停或拖动时分隔线轻度提亮并浮现居中微抓手，提示可拖拽调整尺寸。
// 拖动 / 双击行为由 useResizablePane 的 splitterProps 提供。

const HORIZONTAL_CLASSES = [
  'group relative z-[3] -my-[3px] h-[7px] shrink-0 cursor-row-resize touch-none',
  'before:absolute before:inset-x-0 before:top-[3px] before:h-px before:bg-line before:transition-[background-color,height,top]',
  'hover:before:top-[2.5px] hover:before:h-[2px] hover:before:bg-accent-ring/55',
  'data-[dragging=true]:before:top-[2.5px] data-[dragging=true]:before:h-[2px] data-[dragging=true]:before:bg-accent-ring/70',
  'after:pointer-events-none after:absolute after:left-1/2 after:top-1/2 after:h-[3px] after:w-9 after:-translate-x-1/2 after:-translate-y-1/2 after:scale-x-60 after:rounded-full after:bg-accent-ring after:opacity-0 after:motion-safe:transition-[opacity,transform]',
  'hover:after:scale-x-100 hover:after:opacity-90 data-[dragging=true]:after:scale-x-100 data-[dragging=true]:after:opacity-100'
].join(' ');

const VERTICAL_CLASSES = [
  'group relative z-[3] -mx-[3px] w-[7px] shrink-0 cursor-col-resize touch-none',
  'before:absolute before:inset-y-0 before:left-[3px] before:w-px before:bg-line before:transition-[background-color,width,left]',
  'hover:before:left-[2.5px] hover:before:w-[2px] hover:before:bg-accent-ring/55',
  'data-[dragging=true]:before:left-[2.5px] data-[dragging=true]:before:w-[2px] data-[dragging=true]:before:bg-accent-ring/70',
  'after:pointer-events-none after:absolute after:left-1/2 after:top-1/2 after:h-9 after:w-[3px] after:-translate-x-1/2 after:-translate-y-1/2 after:scale-y-60 after:rounded-full after:bg-accent-ring after:opacity-0 after:motion-safe:transition-[opacity,transform]',
  'hover:after:scale-y-100 hover:after:opacity-90 data-[dragging=true]:after:scale-y-100 data-[dragging=true]:after:opacity-100'
].join(' ');

const PaneSplitter = ({ orientation = 'horizontal', label, ...handlers }) => {
  const isVertical = orientation === 'vertical';
  const defaultLabel = isVertical ? '调整面板宽度' : '调整面板高度';
  const defaultTitle = isVertical ? '拖动调整宽度，双击恢复默认' : '拖动调整高度，双击恢复默认';

  return (
    <div
      role="separator"
      aria-orientation={orientation}
      aria-label={label ?? defaultLabel}
      title={defaultTitle}
      className={isVertical ? VERTICAL_CLASSES : HORIZONTAL_CLASSES}
      {...handlers}
    />
  );
};

export default PaneSplitter;
