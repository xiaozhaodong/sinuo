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
const routingWorkspaceSourcePath = resolve(here, '../../components/model-routing/ModelRoutingWorkspace.jsx');
const routingInteractionSourcePath = resolve(here, '../../components/model-routing/useRoutingWorkspace.js');

test('Claude 管理页按顺序承载端点、模型路由、模型目录与客户端接入', async () => {
  const source = await readFile(pageSourcePath, 'utf8');

  assert.match(source, /label: '端点'/);
  assert.match(source, /id: 'endpoints'[\s\S]*id: 'routing'[\s\S]*id: 'models'[\s\S]*id: 'client-access'/);
  assert.match(source, /label: '模型目录'/);
  assert.match(source, /label: '客户端接入'/);
  assert.match(source, /<PageToolbar>[\s\S]*<PageTabs tabs=\{TABS\}[\s\S]*<\/PageToolbar>/);
  assert.match(source, /<EndpointsPage embedded active=\{activeTab === 'endpoints'\} headerTarget=\{endpointActionsTarget\}[^\n]*\/>/);
  assert.match(source, /<ClaudeCodeEntryCard \/>/);
  assert.match(source, /hidden=\{activeTab !== 'endpoints'\}/);
  assert.match(source, /hidden=\{activeTab !== 'models'\}/);
  assert.match(source, /hidden=\{activeTab !== 'routing'\}/);
  assert.match(source, /hidden=\{activeTab !== 'client-access'\}/);
  assert.match(source, /<ClaudeModelsPage active=\{activeTab === 'models'\} headerTarget=\{modelsActionsTarget\} \/>/);
  assert.match(source, /routingVisited && <ClaudeRoutingPage active=\{activeTab === 'routing'\} headerTarget=\{routingActionsTarget\} \/>/);
  assert.match(source, /if \(activeTab === 'routing' && !routingVisited\) setRoutingVisited\(true\)/);
  assert.match(source, /onOpenModelRouting=\{\(\) => handleTabChange\('routing'\)\}/);
  assert.doesNotMatch(source, /modelRoutingOpenRequest/);
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
  assert.doesNotMatch(source, /ModelRoutingDrawer|routingOpen|modelRoutingOpenRequest/);
  assert.match(source, /onClick=\{onOpenModelRouting\}/);
  assert.match(source, /subscribeToEvent\('model-routing:update',[\s\S]*setModelRoutingSettings\(state\)/);
  assert.match(source, /\{active && formOpen && <EndpointForm/);
});

test('Claude 模型路由自加载成员、投送操作、保留弹窗并支持请求页直接导航', async () => {
  const [source, workspace, interaction, table, inspector, app] = await Promise.all([
    readFile(join(here, 'routing/ClaudeRoutingPage.jsx'), 'utf8'),
    readFile(routingWorkspaceSourcePath, 'utf8'),
    readFile(routingInteractionSourcePath, 'utf8'),
    readFile(join(here, 'routing/ClaudeRoutingTable.jsx'), 'utf8'),
    readFile(join(here, 'routing/ClaudeRoutingInspector.jsx'), 'utf8'),
    readFile(appSourcePath, 'utf8')
  ]);
  assert.match(source, /fetchEndpoints\(\)/);
  assert.match(workspace, /headerTarget && active && createPortal\(toolbar, headerTarget\)/);
  assert.match(workspace, /active && actions.editor && <RoutingEditor/);
  assert.match(workspace, /<Editor key=\{actions.editor.assignment_id \?\? 'new'\}/);
  assert.match(source, /Editor: ModelBindingDialog/);
  assert.match(workspace, /useListKeyboard/);
  assert.match(interaction, /useContextMenu/);
  for (const label of ['编辑…', '恢复自动', '删除…']) assert.ok(interaction.includes(label));
  assert.match(table, /<Globe/);
  assert.match(table, /<Switch/);
  assert.doesNotMatch(table, /<select|type="checkbox"/);
  assert.match(inspector, /共用旧端点组，编辑后自动拆开/);
  assert.match(inspector, /端点已不存在/);
  assert.match(source, /ModelRoutingWorkspace/);
  assert.match(source, /TestBox: RouteTestBox/);
  assert.match(workspace, /<TestBox presentation="inspector"/);
  assert.match(app, /navigateToClaudeModelRouting[\s\S]*setActiveClaudeTab\('routing'\)/);
  assert.doesNotMatch(app, /ModelRoutingOpenRequest|route.modelRouting/);
});
