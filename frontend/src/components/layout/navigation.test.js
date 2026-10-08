import test from 'node:test';
import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import { NAV_SECTIONS, SETTINGS_NAV_ITEM, pageForShortcut, pageTitleOf } from './navigation.js';
import { resolveTrayNavigation } from '../../utils/trayNavigation.js';

const here = dirname(fileURLToPath(import.meta.url));
const appSourcePath = resolve(here, '../../App.jsx');
const sidebarSourcePath = join(here, 'Sidebar.jsx');
const indexCssPath = resolve(here, '../../index.css');

const allItems = [...NAV_SECTIONS.flatMap((section) => section.items), SETTINGS_NAV_ITEM];
const TRAY_TARGETS = ['requests', 'logs', 'requests-failed', 'endpoints', 'account-pool', 'codex-access', 'claude-model-routing', 'codex-model-routing'];

test('侧边栏保留原页面键，托盘跳转的目标页都能在导航里找到', () => {
  const names = allItems.map((item) => item.name);
  assert.equal(new Set(names).size, names.length, '页面键不能重复');
  for (const target of TRAY_TARGETS) {
    const route = resolveTrayNavigation(target);
    assert.ok(names.includes(route.tab), `托盘目标 ${target} → ${route.tab} 不在侧边栏里`);
  }
});

test('快捷键唯一：⌘1–⌘7 依次对应侧边栏条目，⌘, 打开设置', () => {
  const shortcuts = allItems.map((item) => item.shortcut);
  assert.equal(new Set(shortcuts).size, shortcuts.length);
  const ordered = NAV_SECTIONS.flatMap((section) => section.items);
  ordered.forEach((item, index) => assert.equal(item.shortcut, String(index + 1)));
  assert.equal(pageForShortcut('1'), 'overview');
  assert.equal(pageForShortcut(','), 'settings');
  assert.equal(pageForShortcut('9'), null);
});

test('工具栏标题来自导航配置，隐藏页给固定标题，未知页为空串', () => {
  assert.equal(pageTitleOf('endpoints'), 'Claude');
  assert.equal(pageTitleOf('account-pool'), 'Codex');
  assert.equal(pageTitleOf('settings'), '设置');
  assert.equal(pageTitleOf('config'), '配置');
  assert.equal(pageTitleOf('unknown'), '');
});

test('外壳：侧边栏 + 统一工具栏，内容区不再限宽、不再铺点阵纹理', async () => {
  const appSource = await readFile(appSourcePath, 'utf8');
  assert.match(appSource, /<Sidebar\b/);
  assert.match(appSource, /<AppToolbar title=\{pageTitleOf\(activeTab\)\} \/>/);
  assert.match(appSource, /useNavigationShortcuts\(switchTab\)/);
  assert.match(appSource, /<main id="app-scroll-container"/);
  assert.doesNotMatch(appSource, /max-w-7xl/);
  assert.doesNotMatch(appSource, /radial-gradient/);
  assert.doesNotMatch(appSource, /layout\/Header\.jsx/);
});

test('窗口拖拽区用 Wails v3 的 --wails-draggable，控件排除在外', async () => {
  const [css, sidebar] = await Promise.all([readFile(indexCssPath, 'utf8'), readFile(sidebarSourcePath, 'utf8')]);
  assert.match(css, /\.app-drag \{\s*--wails-draggable: drag;/);
  assert.match(css, /\.app-drag :is\(button, input[^)]*\) \{\s*--wails-draggable: no-drag;/);
  assert.match(sidebar, /className="app-drag /);
});
