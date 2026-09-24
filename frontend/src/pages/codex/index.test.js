import test from 'node:test';
import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

const here = dirname(fileURLToPath(import.meta.url));
const pageSourcePath = join(here, 'index.jsx');
const cardSourcePath = resolve(here, '../../components/codex-access/CodexSubscriptionProxyCard.jsx');
const accountPoolSourcePath = resolve(here, '../account-pool/index.jsx');
const accountPoolHeaderSourcePath = resolve(here, '../account-pool/components/PageHeader.jsx');
const appSourcePath = resolve(here, '../../App.jsx');
const headerSourcePath = resolve(here, '../../components/layout/Header.jsx');

test('Codex 管理页独立承载账号池与客户端接入', async () => {
  const source = await readFile(pageSourcePath, 'utf8');

  assert.match(source, /label: '账号池'/);
  assert.match(source, /label: '客户端接入'/);
  assert.match(source, /<AccountPoolPage embedded active=\{activeTab === 'accounts'\} headerTarget=\{accountActionsTarget\} \/>/);
  assert.match(source, /<CodexSubscriptionProxyCard \/>/);
  assert.match(source, /<PageTabs tabs=\{TABS\}/);
  assert.match(source, /role="tabpanel"/);
});

test('顶层导航保留原路由键，并支持设置页直达客户端接入', async () => {
  const appSource = await readFile(appSourcePath, 'utf8');
  const headerSource = await readFile(headerSourcePath, 'utf8');

  assert.match(headerSource, /\{ name: 'account-pool', label: 'Codex 管理' \}/);
  assert.match(appSource, /const \[activeCodexTab, setActiveCodexTab\] = useState\('accounts'\)/);
  assert.match(appSource, /setActiveCodexTab\('client-access'\)/);
  assert.match(appSource, /setActiveTab\('account-pool'\)/);
  assert.match(appSource, /<SettingsPage onNavigateToCodexAccess=\{navigateToCodexAccess\}/);
});

test('页签隐藏已挂载面板，并将账号池 portal 可见性绑定到当前页签', async () => {
  const source = await readFile(pageSourcePath, 'utf8');
  const accountPoolSource = await readFile(accountPoolSourcePath, 'utf8');

  assert.match(source, /hidden=\{activeTab !== 'accounts'\}/);
  assert.match(source, /hidden=\{activeTab !== 'client-access'\}/);
  assert.match(source, /clientAccessVisited/);
  assert.match(source, /setClientAccessVisited\(true\)/);
  assert.doesNotMatch(source, /activeTab === 'accounts'\s*&&\s*<AccountPoolPage/);

  assert.match(accountPoolSource, /notice=\{active \? notice : null\}/);
  assert.match(accountPoolSource, /open=\{active && Boolean\(sharedDrawerRow\)\}/);
  assert.match(accountPoolSource, /open=\{active && schedulerDrawerOpen\}/);
  assert.match(accountPoolSource, /open=\{active && codexModelsOpen\}/);
  assert.match(accountPoolSource, /open=\{active && codexModelRoutingOpen\}/);
  assert.match(accountPoolSource, /open=\{active && accountModalOpen\}/);
  assert.match(accountPoolSource, /deleteTarget=\{active \? deleteTarget : null\}/);
});

test('嵌入账号池时只隐藏重复标题，账号操作栏仍保留', async () => {
  const source = await readFile(accountPoolHeaderSourcePath, 'utf8');

  assert.match(source, /!embedded &&/);
  assert.match(source, /<Button icon=\{Plus\}/);
  assert.match(source, /<Button icon=\{RefreshCw\}/);
  assert.match(source, /onOpenScheduler/);
});

test('一次性接入地址继续由客户端接入卡片内存承载', async () => {
  const source = await readFile(cardSourcePath, 'utf8');

  assert.match(source, /buildSubscriptionProxyAccessURL/);
  assert.match(source, /secret:/);
  assert.match(source, /listenAddressKey\(config\)/);
});

test('隐私读取失败只降级隐私区块，不阻断代理控制', async () => {
  const source = await readFile(cardSourcePath, 'utf8');

  assert.doesNotMatch(source, /Promise\.all\(\[\s*getCodexSubscriptionProxyConfig\(\)[\s\S]{0,200}getPrivacySettings\(\)/);
  assert.match(source, /const loadPrivacy = useCallback/);
  assert.match(source, /setPrivacyError/);
});
