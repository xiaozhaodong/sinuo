import test from 'node:test';
import assert from 'node:assert/strict';
import {
  buildHeatmapCumulative, buildHeatmapDays, buildHeatmapWeeks,
  formatHeatmapValue, heatmapCellBackground, heatmapDayTooltip,
  heatmapKeyboardTarget, heatmapLayout, heatmapLevel, heatmapMonthLabels, heatmapRequestsFilters,
  heatmapSummary, heatmapTooltipPosition, heatmapWeekday
} from './usageHeatmap.js';

const fixture = (first, end, dataStart = first, values = {}) => {
  const days = [];
  const [year, month, day] = first.split('-').map(Number);
  for (let time = Date.UTC(year, month - 1, day); ; time += 86400000) {
    const date = new Date(time).toISOString().slice(0, 10);
    if (date > end) break;
    days.push({
      date, start_time: new Date(time).toISOString(), end_time: new Date(time + 86400000).toISOString(),
      request_count: 0, total_tokens: 0, total_cost_usd: 0, ...(values[date] || {})
    });
  }
  return { start_date: first, end_date: end, data_start_date: dataStart, days };
};

test('周一第一行，周日第七行，只画到今天，三种日期状态区分', () => {
  const data = fixture('2026-10-19', '2026-10-26', '2026-10-21', { '2026-10-23': { total_tokens: 100 } });
  data.end_date = '2026-10-25';
  const days = buildHeatmapDays(data, 'tokens');
  assert.equal(days.length, 7);
  assert.deepEqual(days.map((day) => day.row), [0, 1, 2, 3, 4, 5, 6]);
  assert.deepEqual(days.map((day) => day.state), ['no-record', 'no-record', 'zero', 'zero', 'usage', 'zero', 'zero']);
  assert.equal(heatmapWeekday('2026-10-23'), '周五');
  assert.equal(heatmapWeekday('2026-11-01'), '周日');
  assert.match(days[4].ariaLabel, /2026-10-23 周五 Token 100/);
});

test('经验分位：并列上界排名、全相等、单日、零值及无样本', () => {
  assert.deepEqual([0, 1, 2, 4].map((value) => heatmapLevel(value, [1, 2, 2, 4])), [0, 1, 3, 4]);
  assert.equal(heatmapLevel(8, [8, 8, 8]), 4);
  assert.equal(heatmapLevel(8, [8]), 4);
  assert.equal(heatmapLevel(0, []), 0);
  const data = fixture('2026-10-19', '2026-10-25', '2026-10-19', {
    '2026-10-19': { total_tokens: 1 }, '2026-10-20': { total_tokens: 2 },
    '2026-10-21': { total_tokens: 3 }, '2026-10-22': { total_tokens: 4 }
  });
  assert.deepEqual(buildHeatmapDays(data, 'tokens').map((day) => day.level), [1, 2, 3, 4, 0, 0, 0]);
});

test('按当前指标独立分档；窄窗口截断不改变分位或全区间合计', () => {
  const data = fixture('2026-10-19', '2026-10-26', '2026-10-19', {
    '2026-10-19': { total_tokens: 1, total_cost_usd: 20 },
    '2026-10-26': { total_tokens: 4, total_cost_usd: 1 }
  });
  const tokens = buildHeatmapDays(data, 'tokens');
  const cost = buildHeatmapDays(data, 'cost');
  assert.equal(tokens[0].level, 2);
  assert.equal(cost[0].level, 4);
  const weeks = buildHeatmapWeeks(tokens, data.end_date, 'tokens');
  assert.equal(weeks.slice(-1)[0].days[0].level, 4);
  assert.equal(heatmapSummary(tokens, 'tokens').total, 5);
  assert.equal(heatmapSummary(tokens, 'tokens').activeDays, 2);
});

test('按周求和、整周无记录为 null、本周进行中及点击边界', () => {
  const data = fixture('2026-10-05', '2026-10-21', '2026-10-16', {
    '2026-10-16': { request_count: 2 }, '2026-10-18': { request_count: 3 }, '2026-10-21': { request_count: 4 }
  });
  const weeks = buildHeatmapWeeks(buildHeatmapDays(data, 'requests'), data.end_date, 'requests');
  assert.deepEqual(weeks.map((week) => week.value), [null, 5, 4]);
  assert.deepEqual(weeks.map((week) => week.inProgress), [false, false, true]);
  assert.equal(weeks[0].filters, null);
  assert.equal(weeks[0].tooltip.status, '无记录');
  assert.equal(weeks[2].tooltip.note, '本周进行中，截至今天');
  assert.deepEqual(weeks[2].filters, {
    startDate: '2026-10-19T00:00:00.000Z', endDate: '2026-10-22T00:00:00.000Z', includeCompaction: false
  });
  const fullWeek = fixture('2026-10-19', '2026-10-25');
  assert.equal(buildHeatmapWeeks(buildHeatmapDays(fullWeek, 'tokens'), fullWeek.end_date, 'tokens')[0].inProgress, false);
});

test('累计从最早有记录日起，之前为 null；零值保留平坦段，不可点击', () => {
  const data = fixture('2026-10-19', '2026-10-23', '2026-10-21', {
    '2026-10-21': { total_cost_usd: 2 }, '2026-10-23': { total_cost_usd: 3 }
  });
  const points = buildHeatmapCumulative(buildHeatmapDays(data, 'cost'), 'cost');
  assert.deepEqual(points.map((point) => point.value), [null, null, 2, 2, 5]);
  assert.deepEqual(points[4].tooltip.rows.map((row) => row.value), ['$5.00', '$3.00']);
  assert.ok(points.every((point) => !('filters' in point)));
});

test('月份位于含 1 号的列，跨年统一使用 1月，少于三列省略', () => {
  const data = fixture('2025-12-29', '2026-02-02');
  const labels = heatmapMonthLabels(buildHeatmapWeeks(buildHeatmapDays(data, 'tokens'), data.end_date, 'tokens'));
  assert.deepEqual(labels.map(({ index, label }) => ({ index, label })), [{ index: 0, label: '1月' }, { index: 4, label: '2月' }]);
  assert.equal(labels[0].dayDate, '2026-01-01');
  const crowded = [
    { date: 'a', days: [{ date: '2026-01-01' }] },
    { date: 'b', days: [{ date: '2026-02-01' }] },
    { date: 'c', days: [] },
    { date: 'd', days: [{ date: '2026-03-01' }] }
  ];
  assert.deepEqual(heatmapMonthLabels(crowded).map((label) => label.label), ['1月', '3月']);
});

test('交接保留后端 UTC/DST/微秒边界，仅请求数带压缩过滤，零值无记录不可点', () => {
  const entry = { state: 'usage', value: 1, start_time: '2026-11-01T04:00:00.123456Z', end_time: '2026-11-02T05:00:00.654321Z' };
  const range = { startDate: entry.start_time, endDate: entry.end_time };
  assert.deepEqual(heatmapRequestsFilters(entry, 'tokens'), range);
  assert.deepEqual(heatmapRequestsFilters(entry, 'cost'), range);
  assert.deepEqual(heatmapRequestsFilters(entry, 'requests'), { ...range, includeCompaction: false });
  assert.equal(heatmapRequestsFilters({ ...entry, value: 0 }, 'tokens'), null);
  assert.equal(heatmapRequestsFilters({ ...entry, state: 'no-record' }, 'tokens'), null);
  assert.equal(heatmapRequestsFilters(null, 'tokens'), null);
});

test('宽度计算：最大 16、最小 10、窄窗口只保留最后几周，无溢出', () => {
  assert.deepEqual(heatmapLayout(1004), { cellSize: 16, visibleWeeks: 53, gridWidth: 1004 });
  assert.deepEqual(heatmapLayout(800), { cellSize: 12, visibleWeeks: 53, gridWidth: 792 });
  assert.deepEqual(heatmapLayout(686), { cellSize: 10, visibleWeeks: 53, gridWidth: 686 });
  assert.deepEqual(heatmapLayout(685), { cellSize: 10, visibleWeeks: 52, gridWidth: 673 });
  assert.deepEqual(heatmapLayout(400), { cellSize: 10, visibleWeeks: 31, gridWidth: 400 });
  assert.deepEqual(heatmapLayout(9), { cellSize: 10, visibleWeeks: 0, gridWidth: 0 });
  assert.deepEqual(heatmapLayout(1004, 1), { cellSize: 16, visibleWeeks: 1, gridWidth: 16 });
  for (let width = 0; width <= 1100; width++) assert.ok(heatmapLayout(width).gridWidth <= width);
});

test('悬停包含非缓存输入而非原始 input，负数钳到零，当前指标加粗', () => {
  const day = { date: '2026-10-23', total_tokens: 150, input_tokens: 999, output_tokens: 20, cache_creation_tokens: 10, cache_read_tokens: 100, total_cost_usd: 3.21, request_count: 128 };
  const tooltip = heatmapDayTooltip(day, 'usage', 'cost');
  assert.equal(tooltip.heading, '2026-10-23 周五');
  assert.equal(tooltip.rows.find((row) => row.label === '非缓存输入').value, '20');
  assert.deepEqual(tooltip.rows.filter((row) => row.strong).map((row) => row.label), ['成本']);
  assert.equal(heatmapDayTooltip({ ...day, total_tokens: 0 }, 'usage', 'tokens').rows[1].value, '0');
  assert.equal(heatmapDayTooltip(day, 'no-record', 'tokens').heading, '2026-10-23 周五 · 无记录');
  assert.equal(heatmapDayTooltip(day, 'zero', 'tokens').heading, '2026-10-23 周五 · 无用量');
});

test('悬停浮层跟随格子：右侧放不下翻到左侧，竖直与格子居中，始终钳在面板内', () => {
  const bounds = { width: 400, height: 176 };
  const small = { width: 250, height: 40 };
  const full = { width: 250, height: 158 };
  // 最后一行的小浮层不再贴着面板顶部
  assert.deepEqual(heatmapTooltipPosition({ left: 100, right: 116, centerY: 122 }, bounds, small), { left: 124, top: 102 });
  // 右侧放不下 → 放到格子左侧
  assert.deepEqual(heatmapTooltipPosition({ left: 300, right: 316, centerY: 8 }, bounds, small), { left: 42, top: 0 });
  // 高浮层竖直钳在面板内；右侧刚好放得下时留在右侧，左右都放不下时贴面板左边缘
  assert.deepEqual(heatmapTooltipPosition({ left: 120, right: 136, centerY: 160 }, bounds, full), { left: 144, top: 18 });
  assert.deepEqual(heatmapTooltipPosition({ left: 200, right: 216, centerY: 160 }, bounds, full), { left: 0, top: 18 });
});

test('日历方向键：上下差一天且不跨列，左右差一周，越界不动', () => {
  const data = fixture('2026-10-05', '2026-10-21');
  const days = buildHeatmapDays(data, 'tokens');
  assert.equal(heatmapKeyboardTarget(days, '2026-10-13', 'ArrowDown'), '2026-10-14');
  assert.equal(heatmapKeyboardTarget(days, '2026-10-13', 'ArrowUp'), '2026-10-12');
  assert.equal(heatmapKeyboardTarget(days, '2026-10-13', 'ArrowRight'), '2026-10-20');
  assert.equal(heatmapKeyboardTarget(days, '2026-10-13', 'ArrowLeft'), '2026-10-06');
  // 周一按上、周日按下不跨到相邻列
  assert.equal(heatmapKeyboardTarget(days, '2026-10-12', 'ArrowUp'), null);
  assert.equal(heatmapKeyboardTarget(days, '2026-10-11', 'ArrowDown'), null);
  // 最左列按左、本周之后没有格子
  assert.equal(heatmapKeyboardTarget(days, '2026-10-07', 'ArrowLeft'), null);
  assert.equal(heatmapKeyboardTarget(days, '2026-10-15', 'ArrowRight'), null);
  assert.equal(heatmapKeyboardTarget(days, '2026-10-21', 'ArrowDown'), null);
  assert.equal(heatmapKeyboardTarget(days, '2026-10-13', 'Enter'), null);
  assert.equal(heatmapKeyboardTarget(days, undefined, 'ArrowDown'), null);
});

test('颜色沿用强调色 30/55/80/100，空数据正常', () => {
  assert.equal(heatmapCellBackground(0), 'var(--app-surface-mut)');
  for (const [level, percentage] of [[1, 30], [2, 55], [3, 80], [4, 100]]) {
    assert.equal(heatmapCellBackground(level), `color-mix(in srgb, var(--app-accent-ring) ${percentage}%, var(--app-surface-mut))`);
  }
  assert.deepEqual(buildHeatmapDays(null, 'tokens'), []);
  assert.equal(formatHeatmapValue(1200000000, 'tokens'), '1.2B');
  assert.equal(formatHeatmapValue(2, 'cost'), '$2.00');
  assert.equal(formatHeatmapValue(1234, 'requests'), '1,234');
});
