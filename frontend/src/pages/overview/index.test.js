import test from 'node:test';
import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const readSource = (relativePath) => readFile(path.resolve(__dirname, relativePath), 'utf8');

const overviewPagePath = './index.jsx';
const overviewHookPath = '../../hooks/useOverviewData.js';
const overviewComponents = [
  './components/KPICardsGrid.jsx',
  './components/ModelUsageTable.jsx',
  './components/RequestTrendChart.jsx',
  './components/UpstreamCostRanking.jsx',
  './components/TokenDistributionChart.jsx',
  './components/UsageHeatmap.jsx',
  './components/ResponseTimeChart.jsx',
  './components/ConnectionActivityChart.jsx',
  './components/PerformanceDetailsPanel.jsx'
];

test('概览页是快照页：不订阅 SSE、不派发 chartUpdate、不保留组件级定时器', async () => {
  const [pageSource, hookSource] = await Promise.all([
    readSource(overviewPagePath),
    readSource(overviewHookPath)
  ]);

  // 概览自己的实时通道已移除（注释里提到 chartUpdate 不算）
  assert.doesNotMatch(hookSource, /useSSE/);
  assert.doesNotMatch(hookSource, /addEventListener\(\s*'chartUpdate'/);
  assert.doesNotMatch(hookSource, /CustomEvent\(\s*'chartUpdate'/);
  assert.doesNotMatch(hookSource, /setInterval/);
  assert.doesNotMatch(pageSource, /useSSE/);

  for (const componentPath of overviewComponents) {
    const source = await readSource(componentPath);
    assert.doesNotMatch(source, /setInterval/, `${componentPath} 不应再有定时刷新`);
    assert.doesNotMatch(source, /addEventListener\(\s*'chartUpdate'/, `${componentPath} 不应再监听 chartUpdate`);
  }
});

test('概览跳转按入口区分口径：次数入口仅生成，用量与模型明细保留独立压缩', async () => {
  const [pageSource, tableSource] = await Promise.all([
    readSource(overviewPagePath),
    readSource('./components/ModelUsageTable.jsx')
  ]);

  // 页面本身不下发一刀切口径，口径随入口 filter 传递
  assert.doesNotMatch(pageSource, /includeCompaction: false/);
  // 家族计数是生成口径，点到列表必须收窄，否则行数与数字对不上
  assert.match(tableSource, /includeCompaction: false/);
});

test('概览页不再渲染端点健康环形图与底部连接统计大卡', async () => {
  const pageSource = await readSource(overviewPagePath);

  assert.doesNotMatch(pageSource, /EndpointHealthChart/);
  assert.doesNotMatch(pageSource, /ConnectionStats/);
});

test('概览页首屏按 4 个今日 KPI + 趋势 + 模型表组织，工具栏显示快照时间与刷新', async () => {
  const pageSource = await readSource(overviewPagePath);

  assert.match(pageSource, /KPICardsGrid/);
  assert.match(pageSource, /RequestTrendChart/);
  assert.match(pageSource, /ModelUsageTable/);
  assert.match(pageSource, /UpstreamCostRanking/);
  assert.match(pageSource, /TokenDistributionChart/);
  assert.match(pageSource, /UsageHeatmap/);
  assert.match(pageSource, /PerformanceDetailsPanel/);
  assert.match(pageSource, /<PageToolbar subtitle=\{`快照 · \$\{lastUpdate \|\| '—'\} 更新`\}>/);
  assert.match(pageSource, /title="刷新数据"/);
});

test('用量活动独立加载：不跟随 period 或 refreshKey，顶部刷新同时重取，过期及卸载响应被丢弃', async () => {
  const [page, hook, overviewHook] = await Promise.all([
    readSource(overviewPagePath), readSource('./hooks/useUsageHeatmap.js'), readSource(overviewHookPath)
  ]);
  assert.match(page, /useUsageHeatmap\(\)/);
  assert.match(page, /onClick=\{refreshAll\} loading=\{refreshing\}/);
  assert.match(page, /refresh\(\);\s*reloadHeatmap\(\);/);
  assert.doesNotMatch(page, /<UsageHeatmap[^>]*(?:refreshKey|period)/);
  assert.doesNotMatch(overviewHook, /fetchUsageHeatmap/);
  assert.doesNotMatch(hook, /setInterval|useSSE|chartUpdate|refreshKey|\bperiod\b/);
  assert.match(hook, /requestIdRef\.current === requestId/);
  assert.match(hook, /mountedRef\.current = false/);
  assert.match(hook, /requestIdRef\.current \+= 1/);
});

test('全局 status/notification SSE 与请求页实时流保持不变', async () => {
  const [appSource, requestsSource] = await Promise.all([
    readSource('../../App.jsx'),
    readSource('../requests/index.jsx')
  ]);

  assert.match(appSource, /useSSE\(handleRealtimeEvent, \{ events: 'status,notification' \}\)/);
  assert.match(requestsSource, /useAutoRefresh\(refreshLiveRequestData[,)]/);
});

test('详细性能区默认折叠，展开后才加载两张图', async () => {
  const panelSource = await readSource('./components/PerformanceDetailsPanel.jsx');

  assert.match(panelSource, /aria-expanded=\{isOpen\}/);
  assert.match(panelSource, /<ResponseTimeChart active=\{isOpen\}/);
  assert.match(panelSource, /<ConnectionActivityChart active=\{isOpen\}/);
});

test('时段统计分区：时间切换不在工具栏，放在分区标题并吸顶；四块在分区内，性能详情在分区外', async () => {
  const pageSource = await readSource(overviewPagePath);
  const toolbar = pageSource.slice(pageSource.indexOf('<PageToolbar'), pageSource.indexOf('</PageToolbar>'));
  const sectionStart = pageSource.indexOf('<section aria-labelledby="overview-period-title">');
  const sectionEnd = pageSource.indexOf('</section>', sectionStart);
  const section = pageSource.slice(sectionStart, sectionEnd);

  assert.doesNotMatch(toolbar, /SegmentedControl/);
  assert.ok(sectionStart > 0 && sectionEnd > sectionStart, '应有时段统计分区');
  assert.match(section, /className="sticky top-0 z-20[^"]*bg-pane/);
  assert.match(section, /<h2 id="overview-period-title"[^>]*>时段统计<\/h2>/);
  assert.match(section, /<SegmentedControl options=\{OVERVIEW_PERIODS\} value=\{period\} onChange=\{setPeriod\}/);
  for (const panel of ['RequestTrendChart', 'ModelUsageTable', 'UpstreamCostRanking', 'TokenDistributionChart']) {
    assert.match(section, new RegExp(`<${panel}`), `${panel} 应在时段统计分区内`);
  }
  assert.ok(pageSource.indexOf('<PerformanceDetailsPanel') > sectionEnd, '性能详情不受时段统计控制，应在分区外');
  assert.ok(pageSource.indexOf('<UsageHeatmap') < sectionStart, '用量活动固定近一年，应在分区外');
});

test('上游成本排行跟随时间范围：与模型调用同一窗口，失败可重试', async () => {
  const [pageSource, hookSource, costSource] = await Promise.all([
    readSource(overviewPagePath),
    readSource(overviewHookPath),
    readSource('./components/UpstreamCostRanking.jsx')
  ]);

  // 三个请求共用同一个查询窗口（含「今日」），窗口由 buildOverviewQuery 统一计算
  assert.match(hookSource, /const query = \{ \.\.\.buildOverviewQuery\(targetPeriod, now, getTodayTimeRange\(now\)\), source_view: 'all' \};/);
  assert.match(hookSource, /fetchModelUsageStats\(\{ \.\.\.query, limit: MODEL_USAGE_LIMIT \}\)/);
  assert.match(hookSource, /fetchEndpointCostsData\(query\)/);
  assert.match(hookSource, /fetchRequestTrendData\(query\)/);
  assert.match(pageSource, /<UpstreamCostRanking[\s\S]*?periodCaption=\{caption\}[\s\S]*?loading=\{periodPanelLoading\(errors\.endpointCosts\)\}[\s\S]*?onRetry=\{retryPeriodStats\}/);
  assert.doesNotMatch(costSource, /subtitle="今日"/);
});

test('加载拆分：切换时间范围只重取时段组；顶部刷新整页并递增性能详情的刷新信号', async () => {
  const [pageSource, hookSource] = await Promise.all([
    readSource(overviewPagePath),
    readSource(overviewHookPath)
  ]);

  // 切换时间范围的 effect 只调用时段组加载，挂载时由整页加载发起同一窗口
  assert.match(hookSource, /if \(requestedPeriodRef\.current === period\) return;\s*loadPeriodStats\(period, new Date\(\)\);\s*\}, \[loadPeriodStats, period\]\);/);
  // 整页加载两组共用同一时刻
  assert.match(hookSource, /Promise\.all\(\[loadKpis\(now\), loadPeriodStats\(targetPeriod, now\)\]\)/);
  // 性能详情的刷新信号由页面持有，只在顶部刷新时递增
  assert.doesNotMatch(hookSource, /refreshKey/);
  assert.match(pageSource, /refresh\(\);\s*reloadHeatmap\(\);\s*setPerformanceRefreshKey\(\(current\) => current \+ 1\);/);
  assert.match(pageSource, /<PerformanceDetailsPanel refreshKey=\{performanceRefreshKey\} \/>/);
  // 局部重试只重取所属组
  assert.match(pageSource, /<KPICardsGrid[\s\S]*?onRetry=\{retryKpis\}/);
  assert.equal((pageSource.match(/onRetry=\{retryPeriodStats\}/g) || []).length, 4);
});

test('请求趋势与模型调用整行定高，模型表在面板内滚动，趋势绘图区保留 180px 下限', async () => {
  const [pageSource, tableSource, trendSource] = await Promise.all([
    readSource(overviewPagePath),
    readSource('./components/ModelUsageTable.jsx'),
    readSource('./components/RequestTrendChart.jsx')
  ]);

  assert.match(pageSource, /<div className="grid h-\[312px\] grid-cols-2 gap-3">\s*<RequestTrendChart/);
  assert.doesNotMatch(tableSource, /max-h-\[290px\]|min-h-\[220px\]/);
  assert.match(tableSource, /@container min-h-0 flex-1 overflow-auto/);
  assert.match(trendSource, /relative min-h-\[180px\] w-full flex-1/);
});
