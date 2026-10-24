import test from 'node:test';
import assert from 'node:assert/strict';

import {
  DEFAULT_OVERVIEW_PERIOD,
  OVERVIEW_PERIODS,
  buildCostRanking,
  buildKpiCards,
  buildModelRows,
  buildOverviewQuery,
  buildOverviewRange,
  buildRequestsRangeFromSnapshot,
  buildKpiTrends,
  buildTokenBreakdown,
  buildTrendSummary,
  formatDurationMs,
  formatTokenCount,
  formatRequestShare,
  formatSuccessRate,
  formatTrendBucketLabel,
  isUnresolvedModelName,
  modelDisplayName,
  resolveOverviewPeriod,
  successRateTone,
  withTrendOther
} from './overviewViewModel.js';

test('Token 格式化支持一年合计的 B 档，并保留原有 K/M 档', () => {
  assert.equal(formatTokenCount(0), '0');
  assert.equal(formatTokenCount(1200), '1.2K');
  assert.equal(formatTokenCount(1250000), '1.3M');
  assert.equal(formatTokenCount(999000000), '999.0M');
  assert.equal(formatTokenCount(1000000000), '1.0B');
  assert.equal(formatTokenCount(12500000000), '12.5B');
});

test('时间范围解析：默认 24h，未知值回退到默认项', () => {
  assert.equal(DEFAULT_OVERVIEW_PERIOD, '1d');
  // 「今日」排在最前面，兜底仍按默认值查找，不能按下标落到别的选项
  assert.deepEqual(OVERVIEW_PERIODS.map((item) => item.label), ['今日', '1h', '24h', '7d', '30d']);
  assert.equal(resolveOverviewPeriod('不存在').value, '1d');
  assert.equal(resolveOverviewPeriod('today').caption, '今日');
  assert.equal(resolveOverviewPeriod('7d').caption, '近 7 天');
  assert.equal(resolveOverviewPeriod('30d').label, '30d');
});

test('KPI：今日有请求时给出数值与环比', () => {
  const cards = buildKpiCards(
    { total_requests: 48291, success_rate: 99.21, avg_duration_ms: 428, failed_requests: 382 },
    { total_requests: 42966, success_rate: 99.13, avg_duration_ms: 459, failed_requests: 358 }
  );

  assert.equal(cards.length, 4);
  assert.equal(cards[0].title, '今日请求');
  assert.equal(cards[0].value, '48,291');
  assert.equal(cards[0].comparison.label, '较昨日');
  assert.match(cards[0].comparison.deltaText, /↑ 12\.4%/);
  assert.equal(cards[0].comparison.tone, 'neutral');

  assert.equal(cards[1].value, '99.21%');
  assert.match(cards[1].comparison.deltaText, /↑ 0\.08pt/);

  assert.equal(cards[2].value, '428ms');
  // 延迟下降是好事：tone 走 success
  assert.match(cards[2].comparison.deltaText, /↓ 31ms/);
  assert.equal(cards[2].comparison.tone, 'success');
  assert.equal(cards[2].tone, 'tone-cyan');

  assert.equal(cards[3].value, '382');
  assert.match(cards[3].comparison.deltaText, /↑ 24/);
  assert.equal(cards[3].comparison.tone, 'danger');
});

test('KPI：昨日无数据时不编造百分比', () => {
  const cards = buildKpiCards(
    { total_requests: 12, success_rate: 100, avg_duration_ms: 0, failed_requests: 0 },
    { total_requests: 0 }
  );

  cards.forEach((card) => {
    assert.equal(card.comparison.label, '较昨日无数据');
    assert.equal(card.comparison.deltaText, null);
  });
  // 无有效耗时不显示 0ms
  assert.equal(cards[2].value, '—');
});

test('KPI：昨日查询失败时区分「没有」与「没查到」', () => {
  const cards = buildKpiCards(
    { total_requests: 12, success_rate: 100, avg_duration_ms: 0, failed_requests: 0 },
    {},
    { comparisonUnavailable: true }
  );

  cards.forEach((card) => assert.equal(card.comparison.label, '较昨日数据不可用'));
});

test('KPI：今日查询失败时四张卡都显示占位符，不画成 0 请求', () => {
  const cards = buildKpiCards({}, { total_requests: 30 }, { todayUnavailable: true });

  assert.equal(cards.length, 4);
  cards.forEach((card) => {
    assert.equal(card.value, '—');
    assert.equal(card.comparison.label, '今日数据不可用');
    assert.equal(card.comparison.deltaText, null);
    assert.equal(card.attention, null);
  });
  assert.equal(cards[3].tone, 'tone-slate');
});

test('KPI：今日无请求时成功率与延迟显示占位符', () => {
  const cards = buildKpiCards({ total_requests: 0 }, { total_requests: 0 });

  assert.equal(cards[0].value, '0');
  assert.equal(cards[1].value, '—');
  assert.equal(cards[2].value, '—');
  assert.equal(cards[3].value, '0');
});

test('模型表：未解析模型不作为正常模型行展示', () => {
  const rows = buildModelRows({
    rows: [
      {
        model_name: 'claude-sonnet-4',
        request_family: 'claude',
        request_count: 18420,
        success_rate: 99.6,
        avg_duration_ms: 428,
        total_tokens: 2800000,
        total_cost_usd: 4.82
      },
      {
        model_name: '',
        request_family: 'codex',
        request_count: 3,
        success_rate: 33.3,
        avg_duration_ms: 0,
        total_tokens: 100,
        total_cost_usd: 0
      }
    ]
  });

  assert.equal(rows.length, 1);
  assert.equal(rows[0].modelName, 'claude-sonnet-4');
  assert.equal(rows[0].familyText, 'Claude');
  assert.equal(rows[0].requestCountText, '18,420');
  assert.equal(rows[0].successRateText, '99.60%');
  assert.equal(rows[0].durationText, '428ms');
  assert.equal(rows[0].tokensText, '2.8M');
  assert.equal(rows[0].costText, '$4.82');

  assert.equal(rows[0].successRateTone, 'success');
});

test('成功率着色：只有接近满分才给绿色', () => {
  assert.equal(successRateTone(100, 10), 'success');
  assert.equal(successRateTone(99.6, 10), 'success');
  assert.equal(successRateTone(95, 10), 'neutral');
  assert.equal(successRateTone(89.9, 10), 'danger');
  assert.equal(successRateTone(0, 10), 'danger');
  // 没有请求时不表态
  assert.equal(successRateTone(0, 0), 'neutral');
});

test('模型表：接口无数据时返回空数组而不是假行', () => {
  assert.deepEqual(buildModelRows(undefined), []);
  assert.deepEqual(buildModelRows({ rows: [] }), []);
  assert.equal(isUnresolvedModelName('   '), true);
  assert.equal(isUnresolvedModelName('unknown'), true);
  assert.equal(isUnresolvedModelName('default'), true);
  assert.equal(modelDisplayName('gpt-6-astra'), 'gpt-6-astra');
});

test('模型表：上游标签、悬停耗时与请求占比保持生成请求口径', () => {
  const rows = buildModelRows({
    total_requests: 200,
    rows: [
      {
        model_name: 'claude-opus-4-7',
        request_family: 'claude',
        request_count: 80,
        avg_duration_ms: 5100
      },
      {
        model_name: 'gpt-5.6-sol',
        request_family: 'codex',
        request_count: 2,
        avg_duration_ms: 0
      },
      {
        model_name: 'claude-haiku-4-5',
        request_family: 'claude',
        request_count: 0,
        avg_duration_ms: 0
      }
    ]
  });

  assert.equal(rows[0].hoverText, 'Claude · 平均耗时 5.1s');
  assert.equal(rows[0].sharePercent, 40);
  assert.equal(rows[0].shareText, '40%');
  assert.equal(rows[1].sharePercent, 1);
  assert.equal(rows[1].shareText, '1%');
  assert.equal(rows[2].sharePercent, 0);
  assert.equal(rows[2].shareText, '—');
});

test('模型表：同名模型分属多个请求族时才标注请求族', () => {
  const rows = buildModelRows({
    total_requests: 30,
    rows: [
      { model_name: 'shared-model', request_family: 'claude', request_count: 20 },
      { model_name: 'shared-model', request_family: 'codex', request_count: 6 },
      { model_name: 'gpt-6-luna', request_family: 'codex', request_count: 3 },
      { model_name: 'unknown', request_family: 'codex', request_count: 1 }
    ]
  });

  assert.deepEqual(rows.map((row) => row.showFamilyLabel), [true, true, false]);
  assert.deepEqual(rows.map((row) => row.key), ['shared-model|claude', 'shared-model|codex', 'gpt-6-luna|codex']);
  assert.equal(rows[2].hoverText, 'Codex · 平均耗时 —');
});

test('模型表：不足 1% 的请求占比显示 <1%', () => {
  const [row] = buildModelRows({
    total_requests: 1000,
    rows: [{ model_name: 'm', request_family: 'codex', request_count: 1 }]
  });

  assert.equal(row.sharePercent, 0);
  assert.equal(row.shareText, '<1%');
});

test('请求占比格式化：零值占位、小占比与四舍五入边界', () => {
  for (const [count, total] of [[0, 100], [-1, 100], [10, 0], [10, -100], [undefined, undefined]]) {
    assert.deepEqual(formatRequestShare(count, total), { sharePercent: 0, shareText: '—' });
  }
  assert.deepEqual(formatRequestShare(1, 1000), { sharePercent: 0, shareText: '<1%' });
  assert.deepEqual(formatRequestShare(8, 1000), { sharePercent: 1, shareText: '<1%' });
  assert.deepEqual(formatRequestShare(1, 100), { sharePercent: 1, shareText: '1%' });
  assert.deepEqual(formatRequestShare(41, 200), { sharePercent: 21, shareText: '21%' });
});

test('Token 分布：Claude 的输入与缓存为独立分项', () => {
  const breakdown = buildTokenBreakdown({
    total_tokens: 1000,
    input_tokens: 500,
    output_tokens: 100,
    cache_creation_tokens: 0,
    cache_read_tokens: 400
  });

  assert.equal(breakdown.total, 1000);
  assert.equal(breakdown.totalText, '1.0K');
  assert.equal(breakdown.hasData, true);
  assert.equal(breakdown.items[0].percentText, '50%');
  assert.equal(breakdown.items[2].percentText, '—');
});

test('Token 分布：Codex 缓存读写从输入中拆出，不重复累计', () => {
  const breakdown = buildTokenBreakdown({
    total_tokens: 273748,
    input_tokens: 272077,
    output_tokens: 1671,
    cache_creation_tokens: 2000,
    cache_read_tokens: 265159
  });

  assert.equal(breakdown.total, 273748);
  assert.equal(breakdown.items[0].name, '非缓存输入');
  assert.deepEqual(breakdown.items.map((item) => item.value), [4918, 1671, 2000, 265159]);
  assert.equal(breakdown.items.reduce((sum, item) => sum + item.value, 0), breakdown.total);
  assert.ok(Math.abs(breakdown.items.reduce((sum, item) => sum + item.percent, 0) - 100) < 1e-10);
});

test('Token 分布：混合协议使用后端全量汇总，不从 Top N 模型行推算', () => {
  const breakdown = buildTokenBreakdown({
    total_tokens: 273928,
    input_tokens: 272177,
    output_tokens: 1721,
    cache_creation_tokens: 2010,
    cache_read_tokens: 265179,
    rows: [],
    truncated: true
  });

  assert.equal(breakdown.total, 273928);
  assert.deepEqual(breakdown.items.map((item) => item.value), [5018, 1721, 2010, 265179]);
  assert.equal(breakdown.items.reduce((sum, item) => sum + item.value, 0), breakdown.total);
});

test('Token 分布：全零时标记无数据', () => {
  const breakdown = buildTokenBreakdown({});

  assert.equal(breakdown.hasData, false);
  assert.equal(breakdown.totalText, '0');
  assert.ok(breakdown.items.every((item) => item.percentText === '—'));
});

test('成本排行：按成本降序、过滤零成本、宽度归一', () => {
  const rows = buildCostRanking([
    { name: 'a', cost: 0.3, tokens: 100 },
    { name: 'b', cost: 0.6, tokens: 200 },
    { name: 'c', cost: 0, tokens: 300 },
    { name: '', cost: 0.15, tokens: 50 }
  ]);

  assert.equal(rows.length, 3);
  assert.deepEqual(rows.map((row) => row.name), ['b', 'a', '未知上游']);
  assert.equal(rows[0].percent, 100);
  assert.equal(rows[1].percent, 50);
  assert.equal(rows[0].costText, '$0.600');
});

test('成本排行：空数据返回空列表', () => {
  assert.deepEqual(buildCostRanking(undefined), []);
  assert.deepEqual(buildCostRanking([{ name: 'a', cost: 0 }]), []);
});

test('成本排行：同名同类型合并、不同类型分开、缺失类型不显示标签', () => {
  const rows = buildCostRanking([
    { name: 'shared', upstream_type: 'account', cost: 0.2, tokens: 100 },
    { name: 'shared', upstream_type: 'account', cost: 0.3, tokens: 200 },
    { name: 'shared', upstream_type: 'endpoint', cost: 0.4, tokens: 300 },
    { name: 'untyped', cost: 0.1, tokens: 50 }
  ]);

  assert.equal(rows.length, 3);
  assert.deepEqual(rows.map((row) => row.key), ['account|shared', 'endpoint|shared', '|untyped']);
  assert.equal(rows[0].cost, 0.5);
  assert.equal(rows[1].cost, 0.4);
  assert.equal(rows[0].typeLabel, '账号池');
  assert.equal(rows[2].typeLabel, '');
});

test('趋势摘要：分桶口径下窗口内总量就是各桶之和', () => {
  const summary = buildTrendSummary([
    { total: 100, success: 90, fail: 10 },
    { total: 60, success: 50, fail: 10 },
    { total: 40, success: 30, fail: 10 }
  ]);

  assert.equal(summary.hasData, true);
  assert.equal(summary.requestsText, '200');
  assert.equal(summary.successText, '170');
  assert.equal(summary.failedText, '30');
});

test('趋势摘要：补出其他状态并只在窗口内有其他时显示', () => {
  const points = withTrendOther([
    { total: 12, success: 8, fail: 2 },
    { total: 3, success: 3, fail: 0 },
    { total: 2, success: 4, fail: 0 }
  ]);
  assert.deepEqual(points.map((point) => point.other), [2, 0, 0]);

  const summary = buildTrendSummary(points);
  assert.equal(summary.otherText, '2');
  assert.equal(summary.hasOther, true);
  assert.equal(buildTrendSummary([{ total: 3, success: 3, fail: 0 }]).hasOther, false);
});

test('趋势摘要：空数据标记无数据', () => {
  assert.equal(buildTrendSummary([]).hasData, false);
  assert.equal(buildTrendSummary(undefined).requestsText, '0');
});

test('趋势桶标签：天桶只显示月-日，其余显示时:分', () => {
  const formatters = {
    formatTimeOnly: () => '10:05:00',
    formatMonthDayTime: () => '09-09 10:00'
  };

  assert.equal(formatTrendBucketLabel('2026-09-09T02:05:00Z', 'day', formatters), '09-09');
  assert.equal(formatTrendBucketLabel('2026-09-09T02:05:00Z', 'hour', formatters), '10:05');
  assert.equal(formatTrendBucketLabel('', 'hour', formatters), '');
});

test('格式化：耗时与成功率的口径', () => {
  assert.equal(formatDurationMs(0), '—');
  assert.equal(formatDurationMs(428), '428ms');
  assert.equal(formatDurationMs(1800), '1.8s');
  assert.equal(formatSuccessRate(99.21, 10), '99.21%');
  assert.equal(formatSuccessRate(0, 0), '—');
});

test('快照滚动窗口精确到毫秒，24 小时不随 DST 改变', () => {
  const range = buildOverviewRange('1d', new Date('2026-11-01T06:30:45.123Z'));
  assert.deepEqual(range, { start_date: '2026-10-31T06:30:45.123Z', end_date: '2026-11-01T06:30:45.123Z' });
  assert.deepEqual(buildOverviewRange('7d', new Date('2026-09-09T02:25:30.123Z')), {
    start_date: '2026-09-02T02:25:30.123Z', end_date: '2026-09-09T02:25:30.123Z'
  });
});

test('时段查询：「今日」从配置时区的今天 0 点到快照时刻，按小时分桶；其余选项是滚动窗口', () => {
  const now = new Date('2026-10-24T02:54:30.123Z');
  // 配置时区的今日范围由 TimezoneContext 给出（墙钟时间，后端按配置时区解释）
  const todayRange = { start_date: '2026-10-24T00:00', end_date: '2026-10-25T00:00' };

  assert.deepEqual(buildOverviewQuery('today', now, todayRange), {
    period: '1d', start_date: '2026-10-24T00:00', end_date: '2026-10-24T02:54:30.123Z'
  });
  assert.deepEqual(buildOverviewQuery('7d', now, todayRange), {
    period: '7d', start_date: '2026-10-17T02:54:30.123Z', end_date: '2026-10-24T02:54:30.123Z'
  });
  // 未知值按默认 24h 处理
  assert.deepEqual(buildOverviewQuery('不存在', now, todayRange), {
    period: '1d', start_date: '2026-10-23T02:54:30.123Z', end_date: '2026-10-24T02:54:30.123Z'
  });
});

test('跳转复用快照边界，保留微秒，停留页面后也不重算', () => {
  const snapshot = { start_time: '2026-09-02T02:25:30.123456Z', end_time: '2026-09-09T02:25:30.654321Z' };
  const before = buildRequestsRangeFromSnapshot(snapshot);
  const after = buildRequestsRangeFromSnapshot(snapshot);
  assert.deepEqual(before, { startDate: snapshot.start_time, endDate: snapshot.end_time });
  assert.deepEqual(after, before);
  assert.throws(() => buildRequestsRangeFromSnapshot({}), /时间为空/);
  assert.throws(() => buildRequestsRangeFromSnapshot({ start_time: snapshot.end_time, end_time: snapshot.start_time }), /范围无效/);
});

test('迷你趋势使用真实桶样本，无请求成功率和无耗时保持断点', () => {
  const trends = buildKpiTrends({ points: [
    { total: 10, success: 9, fail: 1, duration_count: 2, avg_duration_ms: 250 },
    { total: 0, success: 0, fail: 0, duration_count: 0, avg_duration_ms: 0 },
    { total: 3, success: 1, fail: 2, duration_count: 1, avg_duration_ms: 800 }
  ] });
  assert.deepEqual(trends.requests.map(p => p.value), [10, 0, 3]);
  assert.deepEqual(trends['success-rate'].slice(0, 2).map(p => p.value), [90, null]);
  assert.ok(Math.abs(trends['success-rate'][2].value - 100 / 3) < 1e-10);
  assert.deepEqual(trends.latency.map(p => p.value), [250, null, 800]);
  assert.deepEqual(trends.failed.map(p => p.value), [1, 0, 2]);
  assert.deepEqual(buildKpiTrends().latency, []);
});

test('失败提示随实际失败数变化，与较昨日改善分别表达', () => {
  assert.equal(buildKpiCards({ total_requests: 100, failed_requests: 5 }, { total_requests: 100, failed_requests: 20 })[3].attention, '需要关注');
  assert.equal(buildKpiCards({ total_requests: 100, failed_requests: 0 })[3].attention, '暂无失败');
  assert.equal(buildKpiCards({})[3].attention, '暂无请求');
});
