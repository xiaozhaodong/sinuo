import test from 'node:test';
import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import { dirname, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

const here = dirname(fileURLToPath(import.meta.url));
const readSrc = (rel) => readFile(resolve(here, '../..', rel), 'utf8');

test('确认/删除弹窗统一收敛到 AlertDialog 与 z-[70]，不再散落多种层级', async () => {
  const [confirm, delEndpoint, delAccount, delSecret, codexMode, restart] = await Promise.all([
    readSrc('components/ui/ConfirmDialog.jsx'),
    readSrc('pages/endpoints/components/DeleteConfirmDialog.jsx'),
    readSrc('pages/account-pool/components/DeleteAccountDialog.jsx'),
    readSrc('pages/privacy-protection/components/PrivacyExactSecretActionDialogs.jsx'),
    readSrc('pages/requests/components/CodexModeConfirmDialog.jsx'),
    readSrc('components/app-update/AppUpdateRestartDialog.jsx')
  ]);

  assert.match(confirm, /export const AlertDialog =/);
  assert.match(confirm, /z-\[70\]/);
  for (const [name, source] of [
    ['DeleteConfirmDialog', delEndpoint],
    ['DeleteAccountDialog', delAccount],
    ['PrivacyExactSecretActionDialogs', delSecret],
    ['CodexModeConfirmDialog', codexMode],
    ['AppUpdateRestartDialog', restart]
  ]) {
    assert.match(source, /<AlertDialog\b/, `${name} 应复用 AlertDialog`);
    assert.doesNotMatch(source, /z-\[100\]|z-50/, `${name} 不应硬编码冲突层级`);
  }
});

test('CustomSelect 使用 Portal、role=option 与完整键盘导航，老版废弃 UI 组件已清理', async () => {
  const [ui, select] = await Promise.all([
    readSrc('components/ui/index.jsx'),
    readSrc('components/ui/CustomSelect.jsx')
  ]);
  assert.match(select, /createPortal\(/);
  assert.match(select, /role="option"/);
  assert.match(select, /aria-selected=\{isSelected\}/);
  assert.match(select, /isListNavigationKey/);
  assert.match(select, /bg-accent-solid/);
  assert.match(ui, /bg-accent-solid/);
  for (const dead of ['KPICard', 'StatDetailItem', 'TraceSummaryCard', 'StatusBadge', 'LatencyIndicator', 'TokenSelector', 'EmptyState']) {
    assert.doesNotMatch(ui, new RegExp(`export const ${dead}\\b`), `应清理未引用的 ${dead}`);
  }
});

test('PageTabs 与 SegmentedControl 支持左右方向键与 roving tabIndex', async () => {
  const [tabs, seg] = await Promise.all([
    readSrc('components/layout/PageTabs.jsx'),
    readSrc('components/desktop/SegmentedControl.jsx')
  ]);
  for (const source of [tabs, seg]) {
    assert.match(source, /ArrowRight/);
    assert.match(source, /ArrowLeft/);
    assert.match(source, /tabIndex=\{/);
  }
});

test('侧边栏与检查器支持折叠与拖拽调宽，端口状态可点击直达', async () => {
  const [sidebar, toolbar, inspector, splitter, app] = await Promise.all([
    readSrc('components/layout/Sidebar.jsx'),
    readSrc('components/layout/AppToolbar.jsx'),
    readSrc('components/desktop/Inspector.jsx'),
    readSrc('components/desktop/PaneSplitter.jsx'),
    readSrc('App.jsx')
  ]);

  assert.match(sidebar, /PanelLeftClose/);
  assert.match(sidebar, /onOpenServerSettings/);
  assert.match(sidebar, /onOpenClaudeAccess/);
  assert.match(sidebar, /onOpenCodexAccess/);
  assert.match(toolbar, /PanelLeftOpen/);
  assert.match(toolbar, /PanelRight/);
  assert.match(inspector, /orientation: 'vertical'/);
  assert.match(inspector, /registerInspector/);
  assert.match(splitter, /orientation === 'vertical'/);
  assert.match(app, /NavigationGuardProvider/);
  assert.match(app, /useShellShortcuts/);
  assert.match(app, /<ShortcutsDialog/);
});

test('高频搜索统一支持 ⌘F，设置页与定价/模型页接入跨页未保存保护', async () => {
  const [toolbarSearch, accountFilters, privacyToolbar, settings, pricing, claudeModels, codexModels, codexImages] = await Promise.all([
    readSrc('components/layout/ToolbarSearch.jsx'),
    readSrc('pages/account-pool/components/AccountInventoryFilters.jsx'),
    readSrc('pages/privacy-protection/components/PrivacyRulesToolbar.jsx'),
    readSrc('pages/settings/index.jsx'),
    readSrc('pages/pricing/index.jsx'),
    readSrc('pages/claude/models/ClaudeModelsPage.jsx'),
    readSrc('pages/codex/models/CodexModelsPage.jsx'),
    readSrc('pages/codex/images/CodexImagesPage.jsx')
  ]);

  for (const source of [toolbarSearch, accountFilters, privacyToolbar]) {
    assert.match(source, /useSearchShortcut/);
  }
  for (const source of [settings, pricing, claudeModels, codexModels, codexImages]) {
    assert.match(source, /useNavigationGuard/);
  }
  assert.match(settings, /event\.key\.toLowerCase\(\) !== 's'/);
  assert.match(settings, /放弃更改/);
});
