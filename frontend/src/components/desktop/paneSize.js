// ============================================
// 可调高度面板的纯逻辑：高度钳制与持久化值解析
// 2026-10-07
// ============================================

// 钳到 [min, max]；容器太矮导致 max < min 时以 min 为准（面板至少保留可读高度）
export const clampPaneHeight = (height, { min, max }) => {
  const upper = Math.max(min, max);
  return Math.min(Math.max(Math.round(height), min), upper);
};

// localStorage 读出的是字符串或 null；非正数、非数字一律回落默认值
export const parseStoredPaneHeight = (raw, fallback) => {
  if (raw === null || raw === undefined || raw === '') return fallback;
  const value = Number(raw);
  return Number.isFinite(value) && value > 0 ? value : fallback;
};

// 显隐只认精确的 'false'；其它值（含未写过）按默认显示
export const parseStoredPaneVisible = (raw, fallback = true) => {
  if (raw === 'false') return false;
  if (raw === 'true') return true;
  return fallback;
};
