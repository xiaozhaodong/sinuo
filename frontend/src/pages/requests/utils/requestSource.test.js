import test from 'node:test';
import assert from 'node:assert/strict';

import {
  buildUpstreamFilterOptions,
  filterUpstreamOptionsByFamily,
  getRequestFamilyMeta,
  normalizeRequestSource,
  resolveRequestUpstream,
  resolveUpstreamFilterOption
} from './requestSource.js';

test('request source uses request_family and upstream_name as primary fields', () => {
  const request = normalizeRequestSource({ request_family: 'claude', upstream_name: 'claude-primary', endpoint_name: 'legacy' });
  assert.equal(request.requestFamily, 'claude');
  assert.equal(request.upstreamName, 'claude-primary');
  assert.equal(getRequestFamilyMeta(request.requestFamily).label, 'Claude');
});

test('unknown family remains stable as Other and upstream fallback is non-empty', () => {
  assert.equal(normalizeRequestSource({ request_family: 'mystery' }).requestFamily, 'other');
  assert.equal(resolveRequestUpstream({}), '未知上游');
});

test('upstream options follow selected request family', () => {
  const rows = [
    { request_family: 'claude', upstream_name: 'ep-a' },
    { request_family: 'codex', upstream_name: 'account-a' },
    { request_family: 'claude', upstream_name: 'ep-b' }
  ];
  assert.deepEqual(filterUpstreamOptionsByFamily(rows, 'claude'), [
    { value: 'ep-a', label: 'ep-a' },
    { value: 'ep-b', label: 'ep-b' }
  ]);
  assert.deepEqual(filterUpstreamOptionsByFamily(rows, 'codex'), [
    { value: 'account-a', label: 'account-a' }
  ]);
});

// 订阅直通的展示名固定为「订阅直通」，但筛选候选值仍是真实存储的
// upstream_name —— 上游筛选按该列精确查库。
test('订阅直通行显示固定标签，候选值保留真实存储值', () => {
  const subscription = { request_family: 'codex', upstream_type: 'subscription', upstream_name: 'chatgpt-subscription' };
  const account = { request_family: 'codex', upstream_type: 'account', upstream_name: 'account-a' };

  assert.equal(resolveRequestUpstream(subscription), '订阅直通');
  assert.equal(normalizeRequestSource(subscription).upstreamName, '订阅直通');
  assert.deepEqual(
    filterUpstreamOptionsByFamily([subscription, account], 'codex').map((option) => option.value).sort(),
    ['account-a', 'chatgpt-subscription']
  );
  assert.deepEqual(
    filterUpstreamOptionsByFamily([subscription, account], 'codex').find((option) => option.value === 'chatgpt-subscription'),
    { value: 'chatgpt-subscription', label: '订阅直通' }
  );
});

test('Codex 与 Claude Code 的订阅直通各有一个筛选候选，不限请求族时标签带上请求族', () => {
  const codex = { request_family: 'codex', upstream_type: 'subscription', upstream_name: 'chatgpt-subscription' };
  const claude = { request_family: 'claude', upstream_type: 'subscription', upstream_name: 'claude-subscription' };

  assert.equal(resolveRequestUpstream(claude), '订阅直通');
  assert.deepEqual(filterUpstreamOptionsByFamily([codex, claude], 'claude'), [
    { value: 'claude-subscription', label: '订阅直通' }
  ]);
  assert.deepEqual(
    filterUpstreamOptionsByFamily([codex, claude], 'all').map((option) => [option.value, option.label]).sort(),
    [['chatgpt-subscription', '订阅直通 · Codex'], ['claude-subscription', '订阅直通 · Claude']]
  );
});

test('空上游按终态码归类：候选为空显示「无可用上游」而非「未知上游」', () => {
  assert.equal(
    resolveRequestUpstream({ failure_reason: 'no_endpoints_available: no routable endpoint candidates' }),
    '无可用上游'
  );
  assert.equal(resolveRequestUpstream({ failureReason: 'no_healthy' }), '无可用上游');
  assert.equal(resolveRequestUpstream({ failureReason: 'all_endpoints_failed: budget exhausted' }), '无可用上游');
  // 规则表与 lifecycle.js 共享，manual_fixed 历史空上游记录同样归类
  assert.equal(resolveRequestUpstream({ failureReason: 'manual_fixed_endpoint_disabled: ...' }), '无可用上游');
});

test('归类只锚定 code 前缀，detail 文本与其他终态不受影响', () => {
  // detail 里含 endpoint= 不得被误吞
  assert.equal(
    resolveRequestUpstream({ failure_reason: 'rate_limited: endpoint=xuanwulei status=429' }),
    '未知上游'
  );
  // 自由文本首词不可信，回退未知
  assert.equal(resolveRequestUpstream({ cancelReason: 'stream processing cancelled' }), '未知上游');
  // 有上游时终态码不参与判定
  assert.equal(
    resolveRequestUpstream({ upstream_name: 'xuanwulei', failure_reason: 'no_endpoints_available' }),
    'xuanwulei'
  );
});

// 上游筛选按 upstream_name 精确查库（filterTime.js 传 upstream_name 参数），
// 合成标签在库里对应空值，进选项就是必然返回 0 条的死选项。
test('合成上游标签不进筛选选项', () => {
  const rows = [
    { request_family: 'claude', upstream_name: 'xuanwulei' },
    { request_family: 'claude', upstream_name: '', failure_reason: 'no_endpoints_available: no routable endpoint candidates' },
    { request_family: 'claude', upstream_name: '', failure_reason: 'request_body_read_failed: Failed to read request body' }
  ];
  assert.deepEqual(filterUpstreamOptionsByFamily(rows, 'claude').map((option) => option.value), ['xuanwulei']);
  assert.deepEqual(filterUpstreamOptionsByFamily(rows, 'all').map((option) => option.value), ['xuanwulei']);
});

test('单条请求的上游筛选候选与列表候选同口径（右键「只看此上游」用）', () => {
  assert.deepEqual(resolveUpstreamFilterOption({ request_family: 'claude', upstream_name: 'xuanwulei' }), { value: 'xuanwulei', label: 'xuanwulei' });
  assert.deepEqual(
    resolveUpstreamFilterOption({ request_family: 'codex', upstream_type: 'subscription', upstream_name: 'chatgpt-subscription' }),
    { value: 'chatgpt-subscription', label: '订阅直通 · Codex' }
  );
  assert.equal(resolveUpstreamFilterOption({ request_family: 'claude', upstream_name: '' }), null, '合成标签不可筛选');
});

// 回归：候选曾只按当前页生成，最近一页全是订阅直通时下拉里只剩「订阅直通」，选中一个上游后也切不到别的上游
test('上游筛选候选合并后端范围查询结果与当前页', () => {
  const catalog = [
    { request_family: 'claude', upstream_type: 'endpoint', upstream_name: 'duckcoding' },
    { request_family: 'claude', upstream_type: 'subscription', upstream_name: 'claude-subscription' },
    { request_family: 'codex', upstream_type: 'account', upstream_name: 'acct-1' }
  ];
  const pageRows = [
    { request_family: 'claude', upstream_type: 'subscription', upstream_name: 'claude-subscription' },
    // 在途请求或刚出现的新上游：后端查询结果里还没有
    { request_family: 'claude', upstream_type: 'endpoint', upstream_name: 'new-endpoint' }
  ];

  assert.deepEqual(
    buildUpstreamFilterOptions({ catalog, requests: pageRows, family: 'claude' }),
    [
      { value: 'claude-subscription', label: '订阅直通' },
      { value: 'duckcoding', label: 'duckcoding' },
      { value: 'new-endpoint', label: 'new-endpoint' }
    ]
  );
  assert.deepEqual(
    buildUpstreamFilterOptions({ catalog, requests: pageRows, family: 'all' }).map((option) => option.value).sort(),
    ['acct-1', 'claude-subscription', 'duckcoding', 'new-endpoint']
  );
});

test('上游筛选候选在后端不可用时退回当前页', () => {
  const pageRows = [{ request_family: 'claude', upstream_name: 'ep-a' }];
  assert.deepEqual(buildUpstreamFilterOptions({ catalog: null, requests: pageRows }), [{ value: 'ep-a', label: 'ep-a' }]);
});

test('当前选中的上游不在候选里时补一项', () => {
  assert.deepEqual(
    buildUpstreamFilterOptions({ catalog: [], requests: [], family: 'claude', selected: 'gone-endpoint' }),
    [{ value: 'gone-endpoint', label: 'gone-endpoint' }]
  );
  assert.deepEqual(
    buildUpstreamFilterOptions({ catalog: [], requests: [], family: 'all', selected: 'chatgpt-subscription' }),
    [{ value: 'chatgpt-subscription', label: '订阅直通 · Codex' }]
  );
  assert.deepEqual(
    buildUpstreamFilterOptions({ catalog: [{ request_family: 'claude', upstream_name: 'ep-a' }], selected: 'ep-a' }),
    [{ value: 'ep-a', label: 'ep-a' }],
    '已在候选里时不重复'
  );
  assert.deepEqual(buildUpstreamFilterOptions({ catalog: [], selected: 'all' }), [], '「全部上游」不补');
});
