import test from 'node:test';
import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { createRequire } from 'node:module';
import { build } from 'esbuild';
import { createElement } from 'react';
import { renderToStaticMarkup } from 'react-dom/server';
import { buildColumnMenuItems } from './utils/columnMenu.js';
import { TABLE_COLUMNS } from './utils/constants.js';

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const sourcePath = path.resolve(__dirname, './index.jsx');
const autoRefreshPath = path.resolve(__dirname, './hooks/useAutoRefresh.js');
const autoRefreshControlPath = path.resolve(__dirname, './components/AutoRefreshControl.jsx');
const toolbarPath = path.resolve(__dirname, './components/Toolbar.jsx');

const renderComponent = async (name, props) => {
  const compiled = await build({ entryPoints:[path.resolve(__dirname, `./components/${name}.jsx`)], bundle:true, write:false, platform:'node', format:'cjs', jsx:'automatic', external:['react','react/jsx-runtime','react-dom','lucide-react'],
    plugins:[{name:'request-component-context',setup(builder){
      const mocks = {
        '@contexts/TimezoneContext.jsx':'export const useTimezone=()=>({timezone:"Asia/Shanghai",formatListTimestamp:value=>value});',
        '@components/desktop/useListKeyboard.js':'export default ()=>()=>{};',
        '@components/desktop/useContextMenu.jsx':'export default ()=>({contextMenu:null,openMenuAt:()=>{}});',
        '@hooks/useAnchoredPanel.js':'export default ()=>null;',
        '@components/ui':'export const Button=()=>null;',
        '../hooks/useRequestLifecycleDetail.js':'export default ()=>({lifecycle:null,lifecycleLoading:false});'
      };
      builder.onResolve({filter:/.*/},({path})=>path in mocks?{path,namespace:'request-context'}:undefined);
      builder.onLoad({filter:/.*/,namespace:'request-context'},({path})=>({contents:mocks[path]}));
    }}] });
  const mod={exports:{}};
  Function('require','module','exports',compiled.outputFiles[0].text)(createRequire(import.meta.url),mod,mod.exports);
  return renderToStaticMarkup(createElement(mod.exports.default,props));
};

test('两种视图的筛选提示与清除筛选使用实际组件渲染', async () => {
  const props={filters:{},activeRange:'today'};
  const requests=await renderComponent('FilterBar',props);
  const sessions=await renderComponent('FilterBar',{...props,viewMode:'sessions'});
  assert.match(requests,/单击选中 · 右键更多操作 · ↑↓ 切换/);
  assert.match(sessions,/单击选中 · ↑↓ 切换/); assert.doesNotMatch(sessions,/右键更多操作/);
  for (const viewMode of ['requests','sessions']) {
    const active=await renderComponent('FilterBar',{...props,viewMode,filters:{model:'test-model'}});
    assert.match(active,/清除筛选/); assert.doesNotMatch(active,/单击选中/);
  }
  const source=await readFile(sourcePath,'utf8');
  assert.doesNotMatch(source,/会话先汇总后分页/);
});

test('会话表复用四类徽章样式，独立展示缓存样本覆盖与成本缺失', async () => {
  const rows=['codex','claude','image','other'].map(request_family=>({request_family,session_id:`${request_family}-session`,request_count:5,total_cost_usd:0,missing_costs:0,cache_ratio:0.75,missing_cache_usage:2,last_activity:'2026-11-03'}));
  const html=await renderComponent('SessionTable',{sessions:{rows,page:1,size:30,sortBy:'recent',sortOrder:'desc'}});
  for (const tone of ['family-tone-codex','family-tone-claude','family-tone-image','tone-slate']) assert.match(html,new RegExp(`${tone}"><span>`));
  assert.match(html,/aria-sort="descending"[^]*最近活动 ↓/);
  assert.match(html,/缓存用量已知 3 \/ 5 条/); assert.match(html,/75\.0%/);
  assert.match(html,/>费用缺失</); assert.match(html,/>用量缺失</);
  assert.match(html,/title="未记录成本的请求数">0<\/td>/);
  assert.match(html,/title="未记录完整输入或缓存读取的请求数">2<\/td>/);
  assert.match(html,/text-warn font-medium[^]*?\$0\.0000<\/td>/);
  assert.match(html,/text-success font-medium[^]*?75\.0%<\/td>/);
  assert.doesNotMatch(html,/完整成本未知|缺 2 条|NaN/);
});

test('会话表部分费用显示已知合计，两种缺失数独立成列，无缺失为0', async () => {
  const rows=[
    {session_id:'partial',request_count:5,total_cost_usd:8.125,missing_costs:2,cache_ratio:0.93,missing_cache_usage:1},
    {session_id:'unknown',request_count:2,total_cost_usd:null,missing_costs:2,cache_ratio:null,missing_cache_usage:2},
    {session_id:'zero',request_count:1,total_cost_usd:0,missing_costs:0,cache_ratio:0,missing_cache_usage:0}
  ].map(row=>({...row,request_family:'codex',last_activity:'2026-11-03'}));
  const html=await renderComponent('SessionTable',{sessions:{rows,page:1,size:30,sortBy:'cost',sortOrder:'desc'}});
  const renderedRows=[...html.matchAll(/<tr[^>]*data-row-id[^]*?<\/tr>/g)].map(match=>[...match[0].matchAll(/<td[^>]*>([^]*?)<\/td>/g)].map(cell=>cell[1].replace(/<[^>]+>/g,'')));
  assert.equal(renderedRows[0].length,9);
  assert.deepEqual(renderedRows.map(cells=>cells.slice(4,8)),[['$8.1250','2','93.0%','1'],['—','2','—','2'],['$0.0000','0','0.0%','0']]);
  assert.match(html,/已记录成本合计，已知 3 \/ 5 条/);
  assert.doesNotMatch(html,/缺 \d|完整成本未知/);
});

test('会话明细标出字段级未知用量，真实零不标为缺失', async () => {
  const points=[{request_id:'known-zero',ordinal:1,full_input:0,cache_read:0,cost:0},{request_id:'unknown-read',ordinal:2,full_input:100,cache_read:null,cost:0},{request_id:'unknown-input',ordinal:3,full_input:null,cache_read:100,cost:0}];
  const html=await renderComponent('SessionDetailPane',{sessions:{selected:{request_family:'codex',session_id:'coverage'},point:points[1],trend:{points,total:3,page:1,page_size:100}}});
  assert.equal((html.match(/>用量未记录</g)||[]).length,2);
  assert.match(html,/缓存读取未记录；未计入缓存读取占比/);
  assert.match(html,/完整输入未记录；未计入缓存读取占比/);
});

test('自动滚动只跟随明确选中的请求ID与视图，不跟随请求数组或计时刷新', async () => {
  const source=await readFile(sourcePath,'utf8');
  const effect=source.match(/scrollIntoView\([\s\S]*?\n  \}, \[([^\]]*)\]\);/);
  assert.ok(effect,'应保留跨页定位后的可见性处理');
  assert.match(effect[1],/selectedRequestId/);
  assert.doesNotMatch(effect[1],/\brequests\b|\bselection\b|\bselectedRequest\b/);
});

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
