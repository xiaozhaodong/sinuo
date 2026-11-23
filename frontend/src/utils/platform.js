// ============================================
// 前端平台判断（WKWebView UA 含 Macintosh，WebView2 含 Windows NT）
// 2026-10-07
// ============================================

export const isMacPlatform = () => (
  typeof navigator !== 'undefined' && /Mac/i.test(navigator.userAgent || navigator.platform || '')
);

// 快捷键提示文案：macOS 用 ⌘，其他平台用 Ctrl+
export const formatShortcut = (key) => (isMacPlatform() ? `⌘${key}` : `Ctrl+${key}`);

// 外壳快捷键（侧边栏 / 检查器显隐）：macOS 用 ⌥⌘，其他平台用 Ctrl+Alt+
export const formatAltShortcut = (key) => (isMacPlatform() ? `⌥⌘${key}` : `Ctrl+Alt+${key}`);
