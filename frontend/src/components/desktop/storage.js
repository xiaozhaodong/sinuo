// ============================================
// 界面偏好的 localStorage 读写（面板显隐、高度、每页条数等）
// 2026-10-07
// ============================================
// 读不到（存储被禁用）返回 null；写不进（配额满、隐私模式）时只在本次运行内生效，不打断操作。

export const readStorage = (key) => {
  try {
    return window.localStorage.getItem(key);
  } catch {
    return null;
  }
};

export const writeStorage = (key, value) => {
  try {
    window.localStorage.setItem(key, String(value));
  } catch {
    // 忽略
  }
};
