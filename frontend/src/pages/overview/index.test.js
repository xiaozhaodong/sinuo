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
  assert.match(pageSource, /PerformanceDetailsPanel/);
  assert.match(pageSource, /<PageToolbar subtitle=\{`快照 · \$\{lastUpdate \|\| '—'\} 更新`\}>/);
  assert.match(pageSource, /title="刷新数据"/);
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
