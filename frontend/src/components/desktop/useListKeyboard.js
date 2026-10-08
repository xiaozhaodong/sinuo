// ============================================
// 列表键盘导航：表格容器获得焦点后用 ↑↓ / Home / End 切换选中行
// 2026-10-07
// ============================================
// 用法：<div ref={containerRef} tabIndex={0} onKeyDown={handleKeyDown}> … <tr data-row-id={id}> …
// 选中后把对应行滚进可视区（block: nearest，不会把已可见的行强行居中）。

import { useCallback } from 'react';
import { isListNavigationKey, nextListId } from './listNavigation.js';

const useListKeyboard = ({ ids, selectedId, onSelect, containerRef }) => useCallback((event) => {
  if (!isListNavigationKey(event.key) || event.target.closest('input, textarea, select, [contenteditable="true"]')) return;
  const nextId = nextListId(ids, selectedId, event.key);
  if (nextId === null) return;
  event.preventDefault();
  onSelect(nextId);
  requestAnimationFrame(() => {
    const row = containerRef?.current?.querySelector(`[data-row-id="${CSS.escape(String(nextId))}"]`);
    row?.scrollIntoView({ block: 'nearest' });
  });
}, [ids, selectedId, onSelect, containerRef]);

export default useListKeyboard;
