import test from 'node:test';
import assert from 'node:assert/strict';
import {
  appendPeriod,
  buildRule,
  buildSheet,
  isSplitMultiplier,
  isUnifiedMultiplierInvalid,
  normalizeRuleDraft,
  parseLimits,
  nextPeriodIndices,
  ruleDraft,
  sheetDraft,
  unconfiguredRateRows,
  validateCostMultipliers,
  withoutContextTiers,
  withoutPeriods
} from './billingForms.js';

test('首次分时依程序时区，新加时段和重开保留已选时区，关闭后再启用依当前程序时区', () => {
  const plain = sheetDraft({ rows: [{ input: 1, output: 2 }] });
  assert.equal(buildSheet(plain).timezone, '');
  const first = appendPeriod(plain, 'America/New_York');
  assert.equal(buildSheet(first).timezone, 'America/New_York');
  const saved = buildSheet({ ...first, timezone: 'Pacific/Auckland' });
  const reopened = sheetDraft(saved);
  assert.equal(reopened.timezone, 'Pacific/Auckland');
  assert.equal(appendPeriod(reopened, 'UTC').timezone, 'Pacific/Auckland');
  assert.equal(buildSheet(appendPeriod(withoutPeriods(reopened), 'UTC')).timezone, 'UTC');
});

test('结束时间使用下一计费时段的开始值，跨日连接首段，单段连接自身', () => {
  const draft = sheetDraft({
    periods: [{ name: '夜', start_minute: 1320 }, { name: '早', start_minute: 360 }, { name: '午', start_minute: 750 }],
    rows: []
  });
  const next = nextPeriodIndices(draft.periods);
  assert.deepEqual(next, [1, 2, 0]);
  draft.periods[next[1]].time = '14:00';
  draft.periods[next[0]].time = '05:30';
  assert.deepEqual(buildSheet(draft, next).periods, [
    { name: '早', start_minute: 330 }, { name: '午', start_minute: 840 }, { name: '夜', start_minute: 1320 }
  ]);
  assert.deepEqual(nextPeriodIndices([draft.periods[0]]), [0]);
  assert.deepEqual(nextPeriodIndices([]), []);
});

test('新时段逐段接续，清空、重复或跨过其他时段的分界时间无法发布', () => {
  const periods = ['早', '午', '夜'].map((name, i) => ({ key: name, name, time: i ? '' : '00:00' }));
  const draft = { timezone: 'Asia/Shanghai', limits: null, periods, rows: [] };
  const next = nextPeriodIndices(periods);
  assert.deepEqual(next, [1, 2, 0]);
  assert.throws(() => buildSheet(draft, next), /开始时间/);
  periods[next[1]].time = '18:00';
  periods[next[0]].time = '18:00';
  assert.throws(() => buildSheet(draft, next), /开始时间不能重复/);
  periods[next[0]].time = '19:00';
  assert.throws(() => buildSheet(draft, next), /跨过了其他时段/);
  periods[next[0]].time = '12:00';
  assert.equal(buildSheet(draft, next).periods[1].start_minute, 720);
});

test('时段改名保留各行归属，未知与零价可分别发布', () => {
  const draft = sheetDraft({
    timezone: 'Asia/Shanghai',
    context_limits: [1000],
    periods: [
      { name: '峰', start_minute: 360 },
      { name: '谷', start_minute: 1320 }
    ],
    rows: [
      { period: '峰', input: 2, output: 0, cache_read: null },
      { period: '谷', input: 1, output: 0, cache_read: 0 }
    ]
  });
  draft.periods[0].name = '谷';
  assert.throws(() => buildSheet(draft), /唯一/);
  draft.periods[0].name = '白天';
  const sheet = buildSheet(draft);
  assert.deepEqual(
    sheet.rows.map((r) => [r.period, r.cache_read, r.output]),
    [
      ['白天', null, 0],
      ['谷', 0, 0]
    ]
  );
  assert.equal(sheet.periods[1].start_minute, 1320);
});

test('普通价表默认关闭两项分档，启用上下文分档后须填写阈值', () => {
  const draft = sheetDraft({ rows: [{ tier: 0, input: 1, output: 2 }] });
  assert.equal(draft.limits, null);
  assert.deepEqual(buildSheet(draft).periods, []);
  assert.deepEqual(buildSheet(draft).context_limits, []);
  assert.throws(() => buildSheet({ ...draft, limits: ' ' }), /请填写上下文阈值/);
  assert.deepEqual(buildSheet({ ...draft, limits: '128000' }).context_limits, [128000]);
});

test('缺档提示只覆盖已启用的速度和档位，仍可添加第一行 Fast', () => {
  const standard = { period: '', tier: 0, fast: false };
  const fast = { ...standard, fast: true };
  assert.deepEqual(unconfiguredRateRows([standard], [''], [0]), { missing: [], next: fast });
  assert.deepEqual(unconfiguredRateRows([standard, fast], [''], [0]), { missing: [], next: undefined });

  const day = { ...standard, period: '日' };
  const night = { ...standard, period: '夜' };
  const longDay = { ...day, tier: 1 };
  const longNight = { ...night, tier: 1 };
  assert.deepEqual(unconfiguredRateRows([day, night], ['日', '夜'], [0, 1]), {
    missing: [longDay, longNight], next: longDay
  });
  const missing = [{ ...longDay, fast: true }, { ...night, fast: true }, longNight, { ...longNight, fast: true }];
  assert.deepEqual(unconfiguredRateRows([day, { ...day, fast: true }, longDay, night], ['日', '夜'], [0, 1]), {
    missing, next: missing[0]
  });
});

test('关闭一项计费规则只移除对应档位，保留的标准和 Fast 费率可重新载入', () => {
  const periods = [{ name: '日', start_minute: 360 }, { name: '夜', start_minute: 1320 }];
  const rows = periods.flatMap(({ name }, i) => [0, 1].flatMap((tier) =>
    [false, true].map((fast) => ({ period: name, tier, fast, input: i * 10 + tier * 2 + Number(fast), output: 1 }))
  ));
  const draft = sheetDraft({ periods, rows, context_limits: [128000], timezone: 'Asia/Shanghai' });
  const allDay = buildSheet(withoutPeriods(draft));
  assert.equal(allDay.timezone, '');
  assert.deepEqual(allDay.context_limits, [128000]);
  assert.deepEqual(allDay.rows.map((r) => [r.period, r.tier, r.input]), [
    ['', 0, 0], ['', 0, 1], ['', 1, 2], ['', 1, 3]
  ]);
  const base = buildSheet(withoutContextTiers(draft));
  assert.deepEqual(base.periods, periods);
  assert.deepEqual(base.rows.map((r) => [r.period, r.fast, r.input]), [
    ['日', false, 0], ['日', true, 1], ['夜', false, 10], ['夜', true, 11]
  ]);
  const simple = sheetDraft(buildSheet(withoutContextTiers(withoutPeriods(draft))));
  assert.deepEqual(simple.periods, []);
  assert.equal(simple.limits, null);
  assert.deepEqual(simple.rows.map((r) => r.input), ['0', '1']);
});

test('渠道显式零、继承和自定义长档分别往返', () => {
  const rule = {
    period: '峰',
    fast_multiplier: 0,
    tiers: [
      { threshold: null, multiplier: 0.25 },
      { threshold: 500000, multiplier: 2 }
    ]
  };
  assert.deepEqual(buildRule(ruleDraft(rule), [200000]), rule);
  assert.deepEqual(buildRule(ruleDraft({}), []), { period: '', fast_multiplier: null, tiers: [] });
  assert.throws(
    () => buildRule({ ...ruleDraft({}), tiers: [{ threshold: '200', multiplier: '' }] }, []),
    /同时填写/
  );
  assert.throws(() => parseLimits('300,200'), /从小到大/);
});

test('成本倍率模式：0 即分项，统一模式留空与非正数都判为无效', () => {
  assert.equal(isSplitMultiplier(0), true);
  assert.equal(isSplitMultiplier('0'), true);
  assert.equal(isSplitMultiplier(''), false);
  assert.equal(isSplitMultiplier('  '), false);
  assert.equal(isSplitMultiplier(1), false);
  assert.equal(isSplitMultiplier(undefined), false);

  assert.equal(isUnifiedMultiplierInvalid(''), true);
  assert.equal(isUnifiedMultiplierInvalid(0), true);
  assert.equal(isUnifiedMultiplierInvalid('-1'), true);
  assert.equal(isUnifiedMultiplierInvalid('abc'), true);
  assert.equal(isUnifiedMultiplierInvalid('0.5'), false);
  assert.equal(isUnifiedMultiplierInvalid(1), false);
  // 账号表单留空由后端按 1 处理，允许留空，但非空必须大于 0
  assert.equal(isUnifiedMultiplierInvalid('', true), false);
  assert.equal(isUnifiedMultiplierInvalid('0', true), true);

  assert.deepEqual(validateCostMultipliers('split', { inputCostMultiplier: '0', outputCostMultiplier: '' }), {
    error: '分项倍率须大于 0，留空按 1.0 处理',
    invalidFields: ['inputCostMultiplier']
  });
  assert.deepEqual(validateCostMultipliers('split', { inputCostMultiplier: '0.5', outputCostMultiplier: '' }), {
    error: '',
    invalidFields: []
  });
  assert.deepEqual(
    validateCostMultipliers('split', { cacheCreationCostMultiplier1h: '0' }, { channelType: 'account' }),
    { error: '', invalidFields: [] }
  );
  assert.deepEqual(
    validateCostMultipliers('split', { cacheCreationCostMultiplier1h: '0' }, { channelType: 'endpoint' }),
    { error: '分项倍率须大于 0，留空按 1.0 处理', invalidFields: ['cacheCreationCostMultiplier1h'] }
  );
  assert.deepEqual(validateCostMultipliers('unified', { costMultiplier: '0' }), {
    error: '统一倍率须大于 0',
    invalidFields: ['costMultiplier']
  });
});

test('规则草稿裁剪尾端空长档与空白阈值，空 rows 价表可安全读取', () => {
  assert.deepEqual(
    normalizeRuleDraft({ period: '', fast: ' ', tiers: [{ threshold: ' ', multiplier: '' }] }),
    normalizeRuleDraft(ruleDraft({}))
  );
  assert.deepEqual(
    buildRule({ period: '', fast: '', tiers: [{ threshold: '  ', multiplier: '1.5' }] }, [200000]),
    { period: '', fast_multiplier: null, tiers: [{ threshold: null, multiplier: 1.5 }] }
  );
  assert.deepEqual(sheetDraft({ rows: null }).rows, []);
  assert.deepEqual(withoutPeriods({ timezone: 'UTC', periods: [], limits: null, rows: [] }).periods, []);
});
