import test from 'node:test';
import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { buildColumnMenuItems } from './utils/columnMenu.js';
import { TABLE_COLUMNS } from './utils/constants.js';

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const sourcePath = path.resolve(__dirname, './index.jsx');
const autoRefreshPath = path.resolve(__dirname, './hooks/useAutoRefresh.js');
const autoRefreshControlPath = path.resolve(__dirname, './components/AutoRefreshControl.jsx');
const toolbarPath = path.resolve(__dirname, './components/Toolbar.jsx');

test('requests page switches accounts by pinning the selected account instead of promoting its tier', async () => {
  const source = await readFile(sourcePath, 'utf8');

  assert.match(source, /pinUpstreamAccountSelection/);
  assert.match(source, /已固定使用|固定账号|固定到/);
  assert.doesNotMatch(source, /moveUpstreamAccountToTier\(accountId,\s*'primary'\)/);
  assert.doesNotMatch(source, /已经是主组账号/);
});

test('请求页顶部汇总与列表同口径', async () => {
  const source = await readFile(sourcePath, 'utf8');

  // 统计接口缺省是概览的按指标混合口径；请求页必须显式下传列表口径。
  assert.match(source, /include_compaction: requestsQueryParams\.include_compaction !== false/);
});

test('只看此会话与只看模型 / 上游一致，保留当前时间范围', async () => {
  const source = await readFile(sourcePath, 'utf8');
  const definition = source.match(/const applyContextFilter = useCallback\(\(patch\) => \{([\s\S]*?)\n  \}, \[/);

  assert.ok(definition, '应找到 applyContextFilter 的定义体');
  const body = definition[1];
  assert.doesNotMatch(body, /selectRange/);
  assert.doesNotMatch(body, /['"]30days['"]/);
  assert.match(body, /applyQuickFilter\(patch\)/);
});

test('requests page surfaces upstream account load failures to users', async () => {
  const source = await readFile(sourcePath, 'utf8');

  assert.match(source, /fetchUpstreamAccounts\(\)\.catch\(\(err\)\s*=>\s*\{/);
  assert.match(source, /fetchUpstreamAccounts\(\)\.catch\(\(err\)\s*=>\s*\{[^}]*showNotice\('error',/);
});

test('requests page uses Wails request events instead of permanent polling', async () => {
  const [source, hookSource, controlSource, toolbarSource] = await Promise.all([
    readFile(sourcePath, 'utf8'),
    readFile(autoRefreshPath, 'utf8'),
    readFile(autoRefreshControlPath, 'utf8'),
    readFile(toolbarPath, 'utf8')
  ]);

  // 实时事件只刷新请求明细与统计。refreshLiveRequestData 是 refreshRequestData 的
  // live 包装（live 仅用于决定新行是否播入场动画），不能换成整页 loadData —
  // 那会让每个 request:update 都连带重拉端点、账号池与调度快照。
  // 跨天顺延改变查询参数后由 loadData effect 整页重载，一天一次。
  assert.match(source, /const refreshLiveRequestData = useCallback\(\(\) => \{\s*if \(checkDayRollover\(\)\) return;\s*return refreshRequestData\(\{ live: true \}\);/);
  assert.match(source, /useAutoRefresh\(refreshLiveRequestData[,)]/);
  assert.doesNotMatch(source, /useAutoRefresh\(loadData\)/);
  assert.match(source, /const loadDataIdRef = useRef\(0\)/);
  assert.match(source, /loadDataIdRef\.current !== loadId/);
  assert.doesNotMatch(source, /refreshRequestData\(true\)/);
  assert.match(hookSource, /subscribeToEvent\('request:update'/);
  assert.match(hookSource, /EVENT_MAX_WAIT_MS = 1000/);
  assert.match(hookSource, /FAILURE_THRESHOLD = 3/);
  assert.match(hookSource, /createRealtimeRefreshScheduler/);
  assert.match(hookSource, /mode !== REALTIME_REFRESH_MODE\.FALLBACK/);
  assert.match(hookSource, /FALLBACK_INTERVAL_SECONDS = 30/);
  assert.doesNotMatch(hookSource, /refreshInterval/);
  assert.match(controlSource, /已降级/);
  assert.match(controlSource, /每 \{fallbackInterval\} 秒刷新/);
  assert.doesNotMatch(toolbarSource, /AutoRefreshControl/);
  assert.match(controlSource, /连续刷新失败/);
});

test('预设区间跨天顺延保留选中请求与详情面板', async () => {
  const source = await readFile(sourcePath, 'utf8');
  const definition = source.match(/const handleDayRollover = useCallback\(\(nextRange\) => \{([\s\S]*?)\n  \}, \[/);

  assert.match(source, /useDayRollover\(/);
  assert.ok(definition, '应找到 handleDayRollover 的定义体');
  assert.doesNotMatch(definition[1], /setSelection\(null\)/);
});

test('显示列菜单保留固定列并复用原切换与恢复回调', () => {
  const visibleColumns = Object.freeze(['requestId', 'timestamp', 'model']);
  const toggledColumns = [];
  let resetCount = 0;
  const resetColumns = () => { resetCount += 1; };
  const items = buildColumnMenuItems(TABLE_COLUMNS, visibleColumns, (id) => toggledColumns.push(id), resetColumns);

  assert.deepEqual(items[0], { header: '显示列' });
  assert.equal(items.some((item) => item.label === '请求ID' || item.label === '时间'), false);
  assert.equal(items.find((item) => item.label === '模型').checked, true);
  assert.equal(items.find((item) => item.label === '成本').checked, false);
  items.find((item) => item.label === '模型').onSelect();
  items.find((item) => item.label === '成本').onSelect();
  assert.deepEqual(toggledColumns, ['model', 'cost']);
  assert.equal(items.at(-2), '-');
  assert.equal(items.at(-1).label, '恢复默认列');
  assert.equal(items.at(-1).onSelect, resetColumns);
  items.at(-1).onSelect();
  assert.equal(resetCount, 1);
  assert.deepEqual(visibleColumns, ['requestId', 'timestamp', 'model']);
});
