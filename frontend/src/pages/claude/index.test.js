import test from 'node:test';
import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

const here = dirname(fileURLToPath(import.meta.url));
const pageSourcePath = join(here, 'index.jsx');
const endpointsSourcePath = resolve(here, '../endpoints/index.jsx');
const appSourcePath = resolve(here, '../../App.jsx');
const navigationSourcePath = resolve(here, '../../components/layout/navigation.js');
const settingsSourcePath = resolve(here, '../settings/index.jsx');

test('Claude 管理页承载端点与客户端接入两个页签', async () => {
  const source = await readFile(pageSourcePath, 'utf8');

  assert.match(source, /label: '端点'/);
  assert.match(source, /label: '模型目录'/);
  assert.match(source, /label: '客户端接入'/);
  assert.match(source, /<PageToolbar>[\s\S]*<PageTabs tabs=\{TABS\}[\s\S]*<\/PageToolbar>/);
  assert.match(source, /<EndpointsPage embedded active=\{activeTab === 'endpoints'\} headerTarget=\{endpointActionsTarget\}[^>]*\/>/);
  assert.match(source, /<ClaudeCodeEntryCard \/>/);
  assert.match(source, /hidden=\{activeTab !== 'endpoints'\}/);
  assert.match(source, /hidden=\{activeTab !== 'models'\}/);
  assert.match(source, /hidden=\{activeTab !== 'client-access'\}/);
  assert.match(source, /<ClaudeModelsPage active=\{activeTab === 'models'\} headerTarget=\{modelsActionsTarget\} \/>/);
});

test('顶层导航保留 endpoints 路由键，设置页可直达客户端接入', async () => {
  const appSource = await readFile(appSourcePath, 'utf8');
  const navigationSource = await readFile(navigationSourcePath, 'utf8');
  const settingsSource = await readFile(settingsSourcePath, 'utf8');

  assert.match(navigationSource, /\{ name: 'endpoints', label: 'Claude', shortcut: '4' \}/);
  assert.match(appSource, /const \[activeClaudeTab, setActiveClaudeTab\] = useState\('endpoints'\)/);
  assert.match(appSource, /setActiveClaudeTab\('client-access'\)/);
  assert.match(appSource, /onNavigateToClaudeAccess=\{navigateToClaudeAccess\}/);
  assert.doesNotMatch(settingsSource, /<ClaudeCodeEntryCard\s*\/>/);
  assert.match(settingsSource, /onNavigateToClaudeAccess/);
});

test('嵌入端点页时切走页签会收起抽屉并暂停快照轮询', async () => {
  const source = await readFile(endpointsSourcePath, 'utf8');

  assert.match(source, /headerTarget \? \(active && createPortal\(actions, headerTarget\)\)/);
  assert.match(source, /if \(snapshotUnsupported \|\| !active\) return undefined;/);
  assert.match(source, /open=\{active && drawerOpen\}/);
  assert.doesNotMatch(source, /ClaudeModelsPanel/);
  const modelsSource = await readFile(resolve(here, './models/ClaudeModelsPage.jsx'), 'utf8');
  assert.match(modelsSource, /headerTarget && active && createPortal\(/);
  assert.match(source, /open=\{active && routingOpen\}/);
  assert.match(source, /\{active && formOpen && <EndpointForm/);
});
