import test from 'node:test';
import assert from 'node:assert/strict';
import {
  countDecisions,
  resolveEffectiveFailover,
  resolveReasonLabel,
  resolveRouteScopeLabel,
  resolveSnapshotOutcome,
  routeModeLabels,
  runtimeLabels
} from './scheduleSnapshotView.js';

test('快照成功与全部候选失败保持结论、颜色和异常标记', () => {
  assert.deepEqual(resolveSnapshotOutcome({ finalOutcome: 'success' }), {
    label: '成功', className: 'tone-emerald', isAbnormal: false
  });
  assert.deepEqual(resolveSnapshotOutcome({ finalOutcome: 'all_candidates_failed' }), {
    label: '全部候选失败', className: 'tone-amber', isAbnormal: true
  });
});

test('未知结论保留原值并提示异常', () => {
  assert.deepEqual(resolveSnapshotOutcome({ finalOutcome: 'new_outcome' }), {
    label: 'new_outcome', className: 'tone-amber', isAbnormal: true
  });
});

test('空结论显示未完成，不触发端点页异常提示', () => {
  const expected = { label: '未完成', className: 'tone-slate', isAbnormal: false };
  assert.deepEqual(resolveSnapshotOutcome(), expected);
  assert.deepEqual(resolveSnapshotOutcome({ finalOutcome: '' }), expected);
  assert.deepEqual(resolveSnapshotOutcome({ finalOutcome: '  ' }), expected);
});

test('结论兼容 snake_case 字段及大小写和前后空格', () => {
  assert.deepEqual(resolveSnapshotOutcome({ final_outcome: ' SUCCESS ' }), {
    label: '成功', className: 'tone-emerald', isAbnormal: false
  });
});

test('其余结论保留原抽屉文案、颜色和异常语义', () => {
  const outcomes = [
    ['pending', '进行中', 'tone-blue', false],
    ['passthrough_raw', '原样透传', 'tone-slate', false],
    ['passthrough_error', '上游失败', 'tone-amber', true],
    ['cancelled', '已取消', 'tone-slate', true],
    ['quality_incomplete', '响应不完整', 'tone-amber', true],
    ['failed_after_commit', '响应阶段失败', 'tone-amber', true],
    ['privacy_blocked', '隐私策略拦截', 'tone-amber', true],
    ['no_candidates', '无可用候选', 'tone-amber', true],
    ['manual_fixed_blocked', '固定目标不可用', 'tone-amber', true],
    ['model_routing_unassigned', '模型未配置上游端点', 'tone-amber', true],
    ['rejected_by_failure_tracker', '失败阈值拦截', 'tone-amber', true],
    ['rate_limited', '限流', 'tone-amber', true]
  ];
  for (const [finalOutcome, label, className, isAbnormal] of outcomes) {
    assert.deepEqual(resolveSnapshotOutcome({ finalOutcome }), { label, className, isAbnormal });
  }
});

test('决策计数只统计 candidate 与 skipped，不把未知决策算作跳过', () => {
  const decisions = [
    { decision: 'candidate' }, { decision: 'skipped' },
    { decision: 'candidate' }, { decision: 'unknown' }, {}
  ];
  assert.deepEqual(countDecisions(decisions), { candidates: 2, skipped: 1 });
  assert.deepEqual(countDecisions([]), { candidates: 0, skipped: 0 });
  assert.deepEqual(countDecisions(), { candidates: 0, skipped: 0 });
  assert.equal(decisions.length, 5);
});

test('路由范围区分模型已分配、未分配与全局路由', () => {
  assert.equal(resolveRouteScopeLabel({ routeScopeKind: 'upstream_group' }), '模型路由：已分配');
  assert.equal(resolveRouteScopeLabel({ routeScopeKind: 'unassigned' }), '模型路由：未分配');
  assert.equal(resolveRouteScopeLabel({ routeScopeKind: 'global' }), '全局路由');
  assert.equal(resolveRouteScopeLabel({ routeScopeKind: 'unknown' }), '全局路由');
  assert.equal(resolveRouteScopeLabel(), '全局路由');
});

test('故障转移全局开启且请求路由允许时显示已启用', () => {
  assert.deepEqual(resolveEffectiveFailover({ failoverEnabled: true, routeMode: 'auto' }), {
    enabled: true, label: '已启用'
  });
  assert.deepEqual(resolveEffectiveFailover({ failoverEnabled: true, routeFallbackEnabled: true, routeMode: 'manual_preferred' }), {
    enabled: true, label: '已启用'
  });
});

test('全局关闭或请求路由关闭故障转移时显示已关闭', () => {
  for (const snapshot of [{ failoverEnabled: false }, { failoverEnabled: true, routeFallbackEnabled: false }, {}]) {
    assert.deepEqual(resolveEffectiveFailover(snapshot), { enabled: false, label: '已关闭' });
  }
});

test('手动固定始终关闭请求内故障转移，保持专用提示', () => {
  for (const failoverEnabled of [true, false]) {
    assert.deepEqual(resolveEffectiveFailover({ failoverEnabled, routeFallbackEnabled: true, routeMode: 'manual_fixed' }), {
      enabled: false, label: '固定模式关闭'
    });
  }
});

test('决策原因保留优先级、模型路由与手动固定的原文案', () => {
  assert.equal(resolveReasonLabel('auto_priority'), '自动调度优先级');
  assert.equal(resolveReasonLabel('fallback'), '故障转移候选');
  assert.equal(resolveReasonLabel('model_routing_unassigned'), '模型未配置上游端点');
  assert.equal(resolveReasonLabel('manual_fixed_failure_threshold_tripped'), '手动固定目标触发失败阈值');
  assert.equal(resolveReasonLabel('manual_fixed_availability_disabled'), '手动固定目标：端点已硬关闭');
  assert.equal(resolveReasonLabel('unknown_reason'), 'unknown_reason');
  assert.equal(resolveReasonLabel(), '-');
});

test('负缓存原因与手动固定前缀可组合，未知类别保留原值', () => {
  assert.equal(resolveReasonLabel('negative_cache_model_unsupported'), '路由负缓存：模型不支持');
  assert.equal(resolveReasonLabel('negative_cache_count_tokens_unsupported'), '路由负缓存：不支持 count_tokens');
  assert.equal(resolveReasonLabel('negative_cache_unknown'), '路由负缓存：unknown');
  assert.equal(resolveReasonLabel('manual_fixed_negative_cache_schema_incompatible'), '手动固定目标：路由负缓存：请求 schema 不兼容');
});

test('运行结果与路由模式继续使用原抽屉文案', () => {
  assert.equal(runtimeLabels.success, '成功命中');
  assert.equal(runtimeLabels.try_next, '失败，尝试下一候选');
  assert.equal(runtimeLabels.attempting, '尝试中');
  assert.deepEqual(routeModeLabels, { auto: '自动调度', manual_preferred: '手动优选', manual_fixed: '手动固定' });
});
