// ============================================
// 前端平台判断（WKWebView UA 含 Macintosh，WebView2 含 Windows NT）
// 2026-10-07
// ============================================

export const isMacPlatform = () => (
  typeof navigator !== 'undefined' && /Mac/i.test(navigator.userAgent || navigator.platform || '')
);

// 快捷键提示文案：macOS 用 ⌘，其他平台用 Ctrl+
export const formatShortcut = (key) => (isMacPlatform() ? `⌘${key}` : `Ctrl+${key}`);
