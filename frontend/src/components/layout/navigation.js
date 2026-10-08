// ============================================
// 主窗口导航配置：侧边栏分组、工具栏标题与页面快捷键共用
// 2026-10-07
// ============================================
// name 是 App 的页面键：托盘跳转（utils/trayNavigation.js）、概览跳请求页等跨页导航都按它路由，
// 只改文案时不要动 name。shortcut 与主修饰键（macOS ⌘ / 其他平台 Ctrl）组合使用。

export const NAV_SECTIONS = Object.freeze([
  {
    label: '监控',
    items: [
      { name: 'overview', label: '概览', shortcut: '1' },
      { name: 'requests', label: '请求追踪', shortcut: '2' },
      { name: 'logs', label: '系统日志', shortcut: '3' }
    ]
  },
  {
    label: '路由',
    items: [
      { name: 'endpoints', label: 'Claude', shortcut: '4' },
      { name: 'account-pool', label: 'Codex', shortcut: '5' }
    ]
  },
  {
    label: '策略',
    items: [
      { name: 'privacy-protection', label: '隐私保护', shortcut: '6' },
      { name: 'pricing', label: '基础定价', shortcut: '7' }
    ]
  }
]);

// 设置固定在侧边栏底部，快捷键沿用 macOS 惯例 ⌘,
export const SETTINGS_NAV_ITEM = Object.freeze({ name: 'settings', label: '设置', shortcut: ',' });

// 不在侧边栏露出、但仍可到达的页面
const HIDDEN_PAGE_TITLES = Object.freeze({ config: '配置' });

const ALL_NAV_ITEMS = [...NAV_SECTIONS.flatMap((section) => section.items), SETTINGS_NAV_ITEM];

export const pageTitleOf = (name) => ALL_NAV_ITEMS.find((item) => item.name === name)?.label ?? HIDDEN_PAGE_TITLES[name] ?? '';

export const pageForShortcut = (key) => ALL_NAV_ITEMS.find((item) => item.shortcut === key)?.name ?? null;
