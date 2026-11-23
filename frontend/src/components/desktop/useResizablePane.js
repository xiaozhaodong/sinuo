// ============================================
// 可调尺寸面板：支持底部调高（horizontal）与右侧调宽（vertical），尺寸与显隐记在 localStorage
// 2026-10-07
// ============================================
// 用法（底部面板）：
//   const pane = useResizablePane({ storageKey: 'requests.detailPane', defaultHeight: 280, minHeight: 160, minRemaining: 120 });
// 用法（右侧检查器）：
//   const pane = useResizablePane({ orientation: 'vertical', storageKey: 'inspector.xxx', defaultSize: 320, minSize: 260, maxSize: 520, minRemaining: 380 });

import { useCallback, useRef, useState } from 'react';
import { clampPaneHeight, parseStoredPaneHeight, parseStoredPaneVisible } from './paneSize.js';
import { readStorage, writeStorage } from './storage.js';

const useResizablePane = ({
  orientation = 'horizontal',
  storageKey,
  defaultSize,
  minSize,
  maxSize = Number.POSITIVE_INFINITY,
  defaultHeight,
  minHeight,
  minRemaining = 120
}) => {
  const isVertical = orientation === 'vertical';
  const resolvedDefault = defaultSize ?? defaultHeight ?? (isVertical ? 320 : 280);
  const resolvedMin = minSize ?? minHeight ?? (isVertical ? 260 : 160);
  const containerRef = useRef(null);
  const sizeKey = `${storageKey}.${isVertical ? 'width' : 'height'}`;
  const visibleKey = `${storageKey}.visible`;
  const [size, setSize] = useState(() => parseStoredPaneHeight(readStorage(sizeKey), resolvedDefault));
  const [visible, setVisibleState] = useState(() => parseStoredPaneVisible(readStorage(visibleKey)));
  const [dragging, setDragging] = useState(false);

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
    const startPos = isVertical ? event.clientX : event.clientY;
    const containerSize = isVertical
      ? (containerRef.current?.clientWidth ?? handle.parentElement?.clientWidth ?? Number.POSITIVE_INFINITY)
      : (containerRef.current?.clientHeight ?? handle.parentElement?.clientHeight ?? Number.POSITIVE_INFINITY);
    const limits = {
      min: resolvedMin,
      max: Math.min(maxSize, containerSize - minRemaining)
    };
    // 窗口缩小后渲染尺寸可能已被约束，从实际可用值起算，避免拖动开头「空走」一段
    const startSize = clampPaneHeight(size, limits);
    let latest = startSize;

    const handleMove = (moveEvent) => {
      const currentPos = isVertical ? moveEvent.clientX : moveEvent.clientY;
      latest = clampPaneHeight(startSize + startPos - currentPos, limits);
      setSize(latest);
    };
    const handleEnd = () => {
      setDragging(false);
      handle.removeEventListener('pointermove', handleMove);
      handle.removeEventListener('pointerup', handleEnd);
      handle.removeEventListener('pointercancel', handleEnd);
      writeStorage(sizeKey, latest);
    };

    setDragging(true);
    handle.setPointerCapture(event.pointerId);
    handle.addEventListener('pointermove', handleMove);
    handle.addEventListener('pointerup', handleEnd);
    handle.addEventListener('pointercancel', handleEnd);
  }, [isVertical, maxSize, minRemaining, resolvedMin, size, sizeKey]);

  const resetSize = useCallback(() => {
    setSize(resolvedDefault);
    writeStorage(sizeKey, resolvedDefault);
  }, [resolvedDefault, sizeKey]);

  return {
    containerRef,
    size,
    height: size,
    width: size,
    visible,
    show,
    hide,
    toggle,
    splitterProps: {
      orientation,
      onPointerDown: handlePointerDown,
      onDoubleClick: resetSize,
      'data-dragging': dragging ? 'true' : undefined
    }
  };
};

export default useResizablePane;
