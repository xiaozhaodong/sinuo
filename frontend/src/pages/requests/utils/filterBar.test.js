import test from 'node:test';
import assert from 'node:assert/strict';
import {
  buildChipPatch,
  buildClearChipsPatch,
  countActiveChips,
  describeTimeChip,
  isChipActive,
  isValidCustomRange,
  resolveStoredPageSize
} from './filterBar.js';
import { DEFAULT_FILTERS, PAGINATION_CONFIG } from './constants.js';
import { getTodayTimeRange } from './filterTime.js';

test('会话标签参与计数、单项清除和清除全部条件', () => {
  const filters = { ...DEFAULT_FILTERS, sessionId: 'session-12345678', model: 'gpt-test' };
  assert.equal(isChipActive(filters, 'sessionId'), true);
  assert.equal(countActiveChips(filters), 2);
  assert.deepEqual(buildChipPatch('sessionId', ''), { sessionId: '', sessionMissing: false });
  assert.equal(countActiveChips({ ...filters, ...buildChipPatch('sessionId', '') }), 1);
  const cleared = { ...filters, ...buildClearChipsPatch() };
  assert.equal(cleared.sessionId, '');
  assert.equal(countActiveChips(cleared), 0);
});

test('无ID筛选有明确标签和计数，清除全部及选择具体会话时取消', () => {
  const filters = { ...DEFAULT_FILTERS, sessionMissing: true };
  assert.equal(countActiveChips(filters), 1);
  assert.equal(isChipActive(filters, 'sessionMissing'), true);
  assert.equal(buildClearChipsPatch().sessionMissing, false);
  assert.deepEqual(buildChipPatch('sessionId', 'full-id'), { sessionId: 'full-id', sessionMissing: false });
  assert.deepEqual(buildChipPatch('sessionMissing', true), { sessionId: '', sessionMissing: true });
});

test('默认筛选没有生效条件', () => {
  assert.equal(countActiveChips(DEFAULT_FILTERS), 0);
  assert.equal(countActiveChips({ ...DEFAULT_FILTERS, startDate: '2026-10-01T00:00' }), 0, '时间不计入');
});

test('各条件按默认值判断是否生效，「仅生成」只在显式 false 时生效', () => {
  const filters = { ...DEFAULT_FILTERS, model: 'claude-opus-4-7', status: 'failed', includeCompaction: false };
  assert.equal(isChipActive(filters, 'model'), true);
  assert.equal(isChipActive(filters, 'status'), true);
  assert.equal(isChipActive(filters, 'includeCompaction'), true);
  assert.equal(isChipActive(filters, 'requestFamily'), false);
  assert.equal(countActiveChips(filters), 3);
});

test('改类型或来源时清掉已选的上游', () => {
  assert.deepEqual(buildChipPatch('requestFamily', 'codex'), { requestFamily: 'codex', upstreamName: 'all' });
  assert.deepEqual(buildChipPatch('sourceView', 'account'), { sourceView: 'account', upstreamName: 'all' });
  assert.deepEqual(buildChipPatch('model', 'gpt-5.5'), { model: 'gpt-5.5' });
  assert.deepEqual(buildChipPatch('model', ''), { model: '' }, '空值回到默认');
  assert.deepEqual(buildChipPatch('status', null), { status: 'all' });
});

test('清除筛选只重置条件，不碰时间', () => {
  const patch = buildClearChipsPatch();
  assert.equal(patch.includeCompaction, true);
  assert.equal(patch.model, '');
  assert.equal('startDate' in patch, false);
  assert.equal(countActiveChips({ ...DEFAULT_FILTERS, model: 'x', ...patch }), 0);
});

test('时间按钮文字：预设显示名称，自定义显示区间', () => {
  assert.equal(describeTimeChip('today'), '今天');
  assert.equal(describeTimeChip('7days'), '近 7 天');
  assert.equal(describeTimeChip('custom', '2026-10-06T00:00', '2026-10-07T18:30:00'), '10/6 00:00 – 10/7 18:30');
  assert.equal(describeTimeChip('custom', '2026-10-06T00:00', ''), '10/6 00:00 – 今');
  assert.equal(describeTimeChip('custom', '', ''), '不限');
});

test('自定义区间开始必须早于结束', () => {
  assert.equal(isValidCustomRange('2026-10-06T00:00', '2026-10-07T00:00'), true);
  assert.equal(isValidCustomRange('2026-10-07T00:00', '2026-10-06T00:00'), false);
  assert.equal(isValidCustomRange('2026-10-07T00:00', '2026-10-07T00:00'), false);
  assert.equal(isValidCustomRange('2026-10-07T00:00', '', {endDate:'2026-10-08T00:00'}), true);
  assert.equal(isValidCustomRange('', ''), false);
});

test('单端时间按业务今日隐含边界校验，等于边界也不可提交', () => {
  const defaults=getTodayTimeRange('Asia/Shanghai',new Date('2026-11-02T17:00:00Z'));
  assert.deepEqual(defaults,{startDate:'2026-11-03T00:00',endDate:'2026-11-04T00:00'});
  for(const [start,end,want] of [
    ['', '2026-11-02T12:00',false],
    ['', '2026-11-03T00:00',false],
    ['', '2026-11-03T00:00:00.000',false],
    ['', '2026-11-03T12:00',true],
    ['2026-11-04T12:00','',false],
    ['2026-11-04T00:00','',false],
    ['2026-11-04T00:00:00.000000','',false],
    ['2026-11-03T12:00','',true],
    ['2026-11-02T00:00','2026-11-02T12:00',true],
    ['2026-11-03T12:00:00','2026-11-03T12:00:00.000',false],
    ['2026-11-03T12:00:00.000001','2026-11-03T12:00:00.000002',true]
  ]) assert.equal(isValidCustomRange(start,end,defaults),want,`${start} → ${end}`);
});

test('每页条数只认可选项，默认 30', () => {
  assert.equal(PAGINATION_CONFIG.DEFAULT_PAGE_SIZE, 30);
  assert.equal(resolveStoredPageSize('50'), 50);
  assert.equal(resolveStoredPageSize(null), 30);
  assert.equal(resolveStoredPageSize('10'), 30, '旧的 10 条不再提供');
  assert.equal(resolveStoredPageSize('200'), 30, '后端单页上限 100');
});
