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

test('嵌入端点页时切走页签会收起弹窗并暂停快照轮询', async () => {
  const source = await readFile(endpointsSourcePath, 'utf8');

  assert.match(source, /headerTarget \? \(active && createPortal\(actions, headerTarget\)\)/);
  assert.match(source, /if \(snapshotUnsupported \|\| !active\) return undefined;/);
  assert.doesNotMatch(source, /EndpointScheduleDrawer|setDrawerOpen|drawerOpen/);
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

test('Claude 调度快照页签排在最后，首次访问才挂载并保留隐藏面板', async () => {
  const source = await readFile(pageSourcePath, 'utf8');
  const tabs = source.match(/const TABS = \[([\s\S]*?)\];/)[1];
  assert.deepEqual([...tabs.matchAll(/id: '([\w-]+)'/g)].map((match) => match[1]), ['endpoints', 'routing', 'models', 'client-access', 'schedule']);
  assert.match(tabs, /\{ id: 'schedule', label: '调度快照' \}/);
  assert.match(source, /\[scheduleVisited, setScheduleVisited\] = useState\(activeTab === 'schedule'\)/);
  assert.match(source, /if \(nextTab === 'schedule'\) setScheduleVisited\(true\)/);
  assert.match(source, /if \(activeTab === 'schedule' && !scheduleVisited\) setScheduleVisited\(true\)/);
  assert.match(source, /<section id="claude-panel-schedule" role="tabpanel" aria-labelledby="claude-tab-schedule" hidden=\{activeTab !== 'schedule'\} className="flex min-h-0 flex-1">/);
  assert.match(source, /scheduleVisited && <ClaudeSchedulePage active=\{activeTab === 'schedule'\} headerTarget=\{scheduleActionsTarget\} \/>/);
  assert.doesNotMatch(source, /activeTab === 'schedule' && <ClaudeSchedulePage|setScheduleVisited\(false\)/);
  assert.match(source, /ref=\{setScheduleActionsTarget\} className="flex min-w-0 flex-1 items-center justify-end" hidden=\{activeTab !== 'schedule'\}/);
});

test('端点页异常提示跳转调度快照页签，不再保留旧工具栏入口', async () => {
  const [page, endpoints, exports] = await Promise.all([
    readFile(pageSourcePath, 'utf8'),
    readFile(endpointsSourcePath, 'utf8'),
    readFile(resolve(here, '../endpoints/components/index.js'), 'utf8')
  ]);
  assert.match(page, /onOpenScheduleSnapshot=\{\(\) => handleTabChange\('schedule'\)\}/);
  assert.match(endpoints, /onClick=\{onOpenScheduleSnapshot\}/);
  assert.match(endpoints, /在「调度快照」查看 →/);
  assert.match(endpoints, /import \{ resolveSnapshotOutcome \} from '@pages\/claude\/schedule\/scheduleSnapshotView.js'/);
  assert.doesNotMatch(endpoints, /EndpointScheduleDrawer|setDrawerOpen|title="调度快照"/);
  assert.doesNotMatch(exports, /EndpointScheduleDrawer|resolveSnapshotOutcome/);
});

test('调度快照独立加载，只有活动且支持时轮询，读取失败保留上一份数据', async () => {
  const source = await readFile(join(here, 'schedule/ClaudeSchedulePage.jsx'), 'utf8');
  assert.match(source, /mounted.current = true;[\s\S]*loadSnapshot\(\);[\s\S]*mounted.current = false/);
  assert.match(source, /if \(!active \|\| unsupported\) return undefined/);
  assert.match(source, /window.setInterval\(\(\) => loadSnapshot\(\{ silent: true \}\), 5000\)/);
  assert.match(source, /async \(\{ silent = false \} = \{\}\)/);
  assert.match(source, /if \(!silent\) setLoading\(true\)/);
  assert.match(source, /if \(!silent && mounted.current\) setLoading\(false\)/);
  assert.match(source, /window.clearInterval\(timer\)/);
  assert.match(source, /headerTarget && active && createPortal\(actions, headerTarget\)/);
  assert.match(source, /ToolbarButton icon=\{RefreshCw\} title="刷新" onClick=\{\(\) => loadSnapshot\(\)\} loading=\{loading\}/);
  assert.match(source, /await fetchLatestEndpointScheduleSnapshot\(\)/);
  assert.match(source, /if \(pending.current\) return/);
  const errorBranch = source.match(/catch \(loadError\) \{([\s\S]*?)\} finally/)[1];
  assert.match(errorBranch, /setError\(resolveErrorMessage\(loadError, '端点调度快照加载失败'\)\)/);
  assert.doesNotMatch(errorBranch, /setSnapshot/);
  assert.match(source, /role="alert"/);
  assert.match(source, /!unsupported && <StatusItem label="5s 自动刷新"/);
  assert.match(source, /snapshot.updatedAt \|\| snapshot.capturedAt/);
  assert.match(source, /formatListTimestamp\(updatedAt\)/);
  assert.match(source, /snapshot.message \|\| '当前运行版本暂不支持端点调度快照。'/);
});

test('调度快照决策表无行选中态，检查器保留请求、结果和最终错误', async () => {
  const [table, inspector] = await Promise.all([
    readFile(join(here, 'schedule/EndpointDecisionsTable.jsx'), 'utf8'),
    readFile(join(here, 'schedule/ClaudeScheduleInspector.jsx'), 'utf8')
  ]);
  assert.match(table, /className="desk-table desk-dense"/);
  for (const label of ['端点', '决策', '运行结果', '原因', '预计恢复']) assert.ok(table.includes(`<th>${label}</th>`));
  assert.doesNotMatch(table, /aria-selected|onSelect|selectedId/);
  assert.match(table, /resolveReasonLabel\(decision.reason\)/);
  assert.match(table, /formatListTimestamp\(decision.availableAt\)/);
  assert.doesNotMatch(table, /formatTimestamp/);
  assert.match(table, /selectable text-\[11.5px\] text-danger/);
  assert.match(inspector, /unsupported \|\| !snapshot.hasSnapshot/);
  assert.match(inspector, /title="最近一次调度"/);
  for (const label of ['请求', '结果', '最终信息']) assert.ok(inspector.includes(`title="${label}"`));
  assert.match(inspector, /label="请求内故障转移"/);
  assert.equal((inspector.match(/<PropertyList labelWidth=\{96\}>/g) || []).length, 2);
  assert.match(inspector, /subtitle=\{snapshot.requestId \? <span className="selectable">\{snapshot.requestId\}<\/span> : undefined\}/);
  assert.doesNotMatch(inspector, /label="请求 ID"/);
  assert.match(inspector, /<p className=\{`mb-2 text-\[12.5px\] leading-\[18px\] \$\{isAbnormal \? 'text-warn' : 'text-fg-body'\}`\}>[\s\S]*snapshot.summary \|\| '端点调度已完成'[\s\S]*<\/p>\s*<PropertyList/);
  assert.match(inspector, /const \{ isAbnormal \} = resolveSnapshotOutcome\(snapshot\)/);
  assert.match(inspector, /formatTimestamp\(snapshot.capturedAt\)/);
  assert.doesNotMatch(inspector, /formatListTimestamp/);
  assert.match(inspector, /snapshot.finalError &&/);
});

test('旧端点调度抽屉文件已删除', async () => {
  await assert.rejects(readFile(resolve(here, '../endpoints/components/EndpointScheduleDrawer.jsx')), { code: 'ENOENT' });
});
