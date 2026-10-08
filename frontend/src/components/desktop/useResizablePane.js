// ============================================
// 底部可调高度面板：拖动分隔条改高度、显示 / 隐藏，两者都记在 localStorage
// 2026-10-07
// ============================================
// 用法：
//   const pane = useResizablePane({ storageKey: 'requests.detailPane', defaultHeight: 280, minHeight: 160, minRemaining: 120 });
//   <div ref={pane.containerRef} className="flex flex-col">  …列表…
//     {pane.visible && <><PaneSplitter {...pane.splitterProps} /><div style={{ height: pane.height }}>…</div></>}
// minRemaining 是拖动时给上方列表保留的最小高度（按容器当前高度算）。

import { useCallback, useRef, useState } from 'react';
import { clampPaneHeight, parseStoredPaneHeight, parseStoredPaneVisible } from './paneSize.js';
import { readStorage, writeStorage } from './storage.js';

const useResizablePane = ({ storageKey, defaultHeight, minHeight, minRemaining }) => {
  const containerRef = useRef(null);
  const heightKey = `${storageKey}.height`;
  const visibleKey = `${storageKey}.visible`;
  const [height, setHeight] = useState(() => parseStoredPaneHeight(readStorage(heightKey), defaultHeight));
  const [visible, setVisibleState] = useState(() => parseStoredPaneVisible(readStorage(visibleKey)));

  const setVisible = useCallback((next) => {
    setVisibleState(next);
    writeStorage(visibleKey, next);
  }, [visibleKey]);
  const show = useCallback(() => setVisible(true), [setVisible]);
  const hide = useCallback(() => setVisible(false), [setVisible]);
  const toggle = useCallback(() => setVisible(!visible), [setVisible, visible]);

  const handlePointerDown = useCallback((event) => {
    if (event.button !== 0) return;
    event.preventDefault();
    const handle = event.currentTarget;
    const startY = event.clientY;
    const limits = { min: minHeight, max: (containerRef.current?.clientHeight ?? Number.POSITIVE_INFINITY) - minRemaining };
    // 窗口缩小后渲染高度可能已被 CSS max-height 压住，从实际可用值起算，避免拖动开头「空走」一段
    const startHeight = clampPaneHeight(height, limits);
    let latest = startHeight;

    const handleMove = (moveEvent) => {
      latest = clampPaneHeight(startHeight + startY - moveEvent.clientY, limits);
      setHeight(latest);
    };
    const handleEnd = () => {
      handle.removeEventListener('pointermove', handleMove);
      handle.removeEventListener('pointerup', handleEnd);
      handle.removeEventListener('pointercancel', handleEnd);
      writeStorage(heightKey, latest);
    };

    handle.setPointerCapture(event.pointerId);
    handle.addEventListener('pointermove', handleMove);
    handle.addEventListener('pointerup', handleEnd);
    handle.addEventListener('pointercancel', handleEnd);
  }, [height, heightKey, minHeight, minRemaining]);

  const resetHeight = useCallback(() => {
    setHeight(defaultHeight);
    writeStorage(heightKey, defaultHeight);
  }, [defaultHeight, heightKey]);

  return {
    containerRef,
    height,
    visible,
    show,
    hide,
    toggle,
    splitterProps: { onPointerDown: handlePointerDown, onDoubleClick: resetHeight }
  };
};

export default useResizablePane;
