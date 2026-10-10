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
const navigationSourcePath = resolve(here, '../../components/layout/navigation.js');
const routingWorkspaceSourcePath = resolve(here, '../../components/model-routing/ModelRoutingWorkspace.jsx');
const routingInteractionSourcePath = resolve(here, '../../components/model-routing/useRoutingWorkspace.js');

test('Codex 管理页按顺序承载账号池、模型路由、模型目录与客户端接入', async () => {
  const source = await readFile(pageSourcePath, 'utf8');

  assert.match(source, /label: '账号池'/);
  assert.match(source, /id: 'accounts'[\s\S]*id: 'routing'[\s\S]*id: 'models'[\s\S]*id: 'client-access'/);
  assert.match(source, /label: '客户端接入'/);
  assert.match(source, /<AccountPoolPage embedded active=\{activeTab === 'accounts'\} headerTarget=\{accountActionsTarget\}[^>]*\/>/);
  assert.match(source, /<CodexSubscriptionProxyCard \/>/);
  assert.match(source, /<PageToolbar>[\s\S]*<PageTabs tabs=\{TABS\}[\s\S]*<\/PageToolbar>/);
  assert.match(source, /role="tabpanel"/);
});

test('顶层导航保留原路由键，并支持设置页直达客户端接入', async () => {
  const appSource = await readFile(appSourcePath, 'utf8');
  const navigationSource = await readFile(navigationSourcePath, 'utf8');

  assert.match(navigationSource, /\{ name: 'account-pool', label: 'Codex', shortcut: '5' \}/);
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

  assert.match(accountPoolSource, /useNotice\(\{\s*enabled:\s*active\s*\}\)/);
  assert.match(accountPoolSource, /open=\{active && Boolean\(sharedDrawerRow\)\}/);
  assert.doesNotMatch(accountPoolSource, /schedulerDrawerOpen|SchedulerDrawer/);
  assert.doesNotMatch(accountPoolSource, /CodexModelRoutingDrawer|codexModelRoutingOpen|modelRoutingOpenRequest/);
  assert.match(accountPoolSource, /open=\{active && accountModalOpen\}/);
  assert.match(accountPoolSource, /deleteTarget=\{active \? deleteTarget : null\}/);
});

test('模型路由首次访问挂载，独立加载账号并保留编辑器重挂载与路由试算', async () => {
  const [page, routing, workspace, interaction, table, app, header] = await Promise.all([
    readFile(pageSourcePath, 'utf8'),
    readFile(join(here, 'routing/CodexRoutingPage.jsx'), 'utf8'),
    readFile(routingWorkspaceSourcePath, 'utf8'),
    readFile(routingInteractionSourcePath, 'utf8'),
    readFile(join(here, 'routing/CodexRoutingTable.jsx'), 'utf8'),
    readFile(appSourcePath, 'utf8'),
    readFile(accountPoolHeaderSourcePath, 'utf8')
  ]);
  assert.match(page, /hidden=\{activeTab !== 'routing'\}/);
  assert.match(page, /routingVisited && <CodexRoutingPage active=\{activeTab === 'routing'\} headerTarget=\{routingActionsTarget\} \/>/);
  assert.match(page, /if \(activeTab === 'routing' && !routingVisited\) setRoutingVisited\(true\)/);
  assert.match(routing, /fetchUpstreamAccounts\(\)/);
  assert.match(routing, /ModelRoutingWorkspace/);
  assert.match(workspace, /headerTarget && active && createPortal\(toolbar, headerTarget\)/);
  assert.match(workspace, /active && actions.editor && <RoutingEditor/);
  assert.match(workspace, /<Editor key=\{actions.editor.assignment_id \?\? 'new'\}/);
  assert.match(routing, /Editor: CodexBindingEditor/);
  assert.match(routing, /TestBox: CodexRouteTestBox/);
  assert.match(workspace, /<TestBox presentation="inspector"/);
  assert.match(workspace, /useListKeyboard/);
  assert.match(interaction, /useContextMenu/);
  for (const label of ['编辑…', '恢复自动', '删除…']) assert.ok(interaction.includes(label));
  assert.match(table, /<Switch/);
  assert.doesNotMatch(table, /<select|type="checkbox"/);
  assert.match(app, /navigateToCodexModelRouting[\s\S]*setActiveCodexTab\('routing'\)/);
  assert.doesNotMatch(page, /modelRoutingOpenRequest/);
  assert.doesNotMatch(header, /onOpenCodexModelRouting|label="模型路由"/);
});

test('嵌入账号池时只隐藏重复标题，账号操作以工具栏按钮保留', async () => {
  const source = await readFile(accountPoolHeaderSourcePath, 'utf8');

  assert.match(source, /!embedded &&/);
  assert.match(source, /<ToolbarButton icon=\{Plus\} title="新增账号"/);
  assert.match(source, /<ToolbarButton icon=\{RefreshCw\} title="刷新数据"/);
  assert.doesNotMatch(source, /onOpenScheduler|label="调度编排"/);
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

test('模型目录首次访问挂载并保留页签状态，工具栏按当前页签投送', async () => {
  const [source, modelsSource, accountPoolSource, headerSource] = await Promise.all([
    readFile(pageSourcePath, 'utf8'),
    readFile(join(here, 'models/CodexModelsPage.jsx'), 'utf8'),
    readFile(accountPoolSourcePath, 'utf8'),
    readFile(accountPoolHeaderSourcePath, 'utf8')
  ]);
  assert.match(source, /label: '模型目录'/);
  assert.match(source, /hidden=\{activeTab !== 'models'\}/);
  assert.match(source, /useState\(activeTab === 'models'\)/);
  assert.match(source, /setModelsVisited\(true\)/);
  assert.match(source, /modelsVisited && <CodexModelsPage active=\{activeTab === 'models'\} headerTarget=\{modelsActionsTarget\} \/>/);
  assert.match(modelsSource, /headerTarget && active && createPortal\(/);
  assert.doesNotMatch(accountPoolSource, /CodexModelsPanel|codexModelsOpen|onOpenCodexModels/);
  assert.doesNotMatch(headerSource, /title="Codex 模型"|onOpenCodexModels/);
});

test('调度编排在账号池后首次访问挂载，隐藏时保留视图并投送工具栏', async () => {
  const source = await readFile(pageSourcePath, 'utf8');
  assert.match(source, /id: 'accounts'[\s\S]*\{ id: 'schedule', label: '调度编排' \}[\s\S]*id: 'routing'/);
  assert.match(source, /useState\(activeTab === 'schedule'\)/);
  assert.match(source, /if \(nextTab === 'schedule'\) setScheduleVisited\(true\)/);
  assert.match(source, /if \(activeTab === 'schedule' && !scheduleVisited\) setScheduleVisited\(true\)/);
  assert.match(source, /scheduleVisited && <CodexSchedulePage active=\{activeTab === 'schedule'\}/);
  assert.match(source, /id="codex-panel-schedule" role="tabpanel" aria-labelledby="codex-tab-schedule" hidden=\{activeTab !== 'schedule'\}/);
  assert.match(source, /ref=\{setScheduleActionsTarget\}[\s\S]*?hidden=\{activeTab !== 'schedule'\}/);
});

test('两个页签共用一个账号与快照数据 hook，并可跳回账号池查看本组', async () => {
  const [source, dataSource, accountSource] = await Promise.all([
    readFile(pageSourcePath, 'utf8'),
    readFile(join(here, 'useCodexPoolData.js'), 'utf8'),
    readFile(accountPoolSourcePath, 'utf8')
  ]);
  assert.match(source, /useCodexPoolData\(\{ enabled: activeTab === 'accounts' \|\| activeTab === 'schedule' \}\)/);
  assert.match(source, /<AccountPoolPage[^>]*poolData=\{poolData\}/);
  assert.match(source, /<CodexSchedulePage[^>]*poolData=\{poolData\}/);
  assert.match(dataSource, /useAccountPoolAccounts\(\)/);
  assert.match(dataSource, /useLatestScheduleSnapshot\(\{ showNotice, enabled \}\)/);
  assert.match(dataSource, /useNotice\(\{ enabled \}\)/);
  assert.doesNotMatch(accountSource, /useAccountPoolAccounts|useLatestScheduleSnapshot/);
  assert.match(source, /setInventoryViewRequest\(\{ type: 'focus-group', groupKey, requestId: Date\.now\(\) \}\);\s*handleTabChange\('accounts'\)/);
});

test('调度页面轮询不改变刷新加载态，检查器无副标题，列表时间和右键规则共用', async () => {
  const [page, inspector, table] = await Promise.all([
    readFile(join(here, 'schedule/CodexSchedulePage.jsx'), 'utf8'),
    readFile(join(here, 'schedule/SchedulerInspector.jsx'), 'utf8'),
    readFile(join(here, 'schedule/SchedulerGroupsTable.jsx'), 'utf8')
  ]);
  assert.match(page, /headerTarget && active && createPortal\(toolbar, headerTarget\)/);
  assert.match(page, /loading=\{loading \|\| refreshing\}/);
  assert.match(page, /Promise\.all\(\[loadData\(\{ silent: true \}\), loadLatestScheduleSnapshot\(\{ silent: true \}\)\]\)/);
  assert.doesNotMatch(page, /setInterval/);
  assert.doesNotMatch(inspector, /subtitle=/);
  assert.match(inspector, /PropertyList labelWidth=\{96\}/);
  assert.match(inspector, /formatTimestamp\(time\)/);
  assert.match(page, /formatListTimestamp\(updatedAt\)/);
  assert.match(table, /formatListTimestamp\(account\.lastSuccessAt\)/);
  assert.doesNotMatch(table, /aria-selected/);
  assert.match(page, /useContextMenu/);
  assert.match(page, /buildAccountActionState\(account, group, actions\.busyKey\)/);
  assert.match(table, /buildAccountActionState\(account, group, actions\.busyKey\)/);
});
