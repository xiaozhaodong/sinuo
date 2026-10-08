// ============================================
// 列表键盘导航的纯逻辑：↑↓ 逐行、Home / End 跳首尾
// 2026-10-07
// ============================================

const NAVIGATION_KEYS = new Set(['ArrowDown', 'ArrowUp', 'Home', 'End']);

export const isListNavigationKey = (key) => NAVIGATION_KEYS.has(key);

// 当前没有选中项时，↓ / Home 落到第一行，↑ / End 落到最后一行
export const nextListId = (ids, currentId, key) => {
  if (!ids.length || !isListNavigationKey(key)) return null;
  const last = ids.length - 1;
  const index = ids.indexOf(currentId);
  if (key === 'Home') return ids[0];
  if (key === 'End') return ids[last];
  if (index === -1) return key === 'ArrowDown' ? ids[0] : ids[last];
  const next = key === 'ArrowDown' ? Math.min(last, index + 1) : Math.max(0, index - 1);
  return ids[next];
};
