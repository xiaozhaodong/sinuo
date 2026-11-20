import test from 'node:test';
import assert from 'node:assert/strict';

import {
  assignStableZebraParity,
  buildRowEnterAnimations,
  collectRequestIds,
  STAGGER_STEP_MS,
  MAX_STAGGER_STEPS
} from './rowEnterAnimation.js';

const rows = (...ids) => ids.map((requestId) => ({ requestId }));
const idsOf = (...ids) => new Set(ids);

test('只有新增的行拿到入场动画，已存在的行不重播', () => {
  const result = buildRowEnterAnimations(rows('new-1', 'old-a', 'old-b'), idsOf('old-a', 'old-b'), true);

  assert.equal(result.size, 1);
  assert.deepEqual(result.get('new-1'), { delayMs: 0 });
  assert.equal(result.get('old-a'), undefined);
});

test('多条同时到达时按顺序错峰', () => {
  const result = buildRowEnterAnimations(rows('n1', 'n2', 'n3', 'old'), idsOf('old'), true);

  assert.equal(result.get('n1').delayMs, 0);
  assert.equal(result.get('n2').delayMs, STAGGER_STEP_MS);
  assert.equal(result.get('n3').delayMs, STAGGER_STEP_MS * 2);
});

test('延迟累计封顶，避免整批入场拖太久', () => {
  const many = Array.from({ length: MAX_STAGGER_STEPS + 6 }, (_, i) => `n${i}`);
  const result = buildRowEnterAnimations(rows(...many), idsOf('old'), true);

  const maxDelay = Math.max(...[...result.values()].map((entry) => entry.delayMs));
  assert.equal(maxDelay, MAX_STAGGER_STEPS * STAGGER_STEP_MS);
  // 超过档位上限的行共用同一个最大延迟，不会无限往后排
  assert.equal(result.get(`n${MAX_STAGGER_STEPS}`).delayMs, maxDelay);
  assert.equal(result.get(`n${MAX_STAGGER_STEPS + 5}`).delayMs, maxDelay);
});

test('封顶后整批最长等待不超过 100ms', () => {
  // 入场是 200ms 的轻微位移，封顶保证「最后一条」与「第一条」之间
  // 拉不开到能读出先后的程度 —— 否则一批到达会读成一行行蹦。
  assert.ok(MAX_STAGGER_STEPS * STAGGER_STEP_MS <= 100,
    `最长等待 ${MAX_STAGGER_STEPS * STAGGER_STEP_MS}ms 超出预算`);
});

test('未启用 / 首次渲染 / 无新增 时都不产出动画', () => {
  assert.equal(buildRowEnterAnimations(rows('n1'), idsOf('old'), false).size, 0, '非实时刷新不播');
  assert.equal(buildRowEnterAnimations(rows('n1'), null, true).size, 0, '首次渲染无上一批可比');
  assert.equal(buildRowEnterAnimations(rows('a'), idsOf('a'), true).size, 0, '没有新增行');
  assert.equal(buildRowEnterAnimations(null, idsOf('a'), true).size, 0, '非数组输入');
});

test('无新增时返回同一个共享实例，不推无谓的全表重渲染', () => {
  const first = buildRowEnterAnimations(rows('a'), idsOf('a'), true);
  const second = buildRowEnterAnimations(rows('a'), idsOf('a'), true);

  assert.equal(first, second, '每次 new Map() 都会让调用方 state 变，白白重渲染整表');
});

test('缺少 requestId 的行被跳过，不影响其余行的序号', () => {
  const malformed = [{ requestId: 'n1' }, null, { }, { requestId: 'n2' }];
  const result = buildRowEnterAnimations(malformed, idsOf('old'), true);

  assert.equal(result.size, 2);
  assert.equal(result.get('n1').delayMs, 0);
  assert.equal(result.get('n2').delayMs, STAGGER_STEP_MS);
});

test('collectRequestIds 去重并跳过脏数据', () => {
  const ids = collectRequestIds([{ requestId: 'a' }, { requestId: 'a' }, null, { }, { requestId: 'b' }]);

  assert.deepEqual([...ids].sort(), ['a', 'b']);
  assert.equal(collectRequestIds(null).size, 0);
});

test('assignStableZebraParity 顶部插入新行时既有行奇偶零翻转，相邻行始终严格交替', () => {
  // 首次加载按奇偶 0, 1, 0, 1
  const initial = assignStableZebraParity(rows('r1', 'r2', 'r3', 'r4'), null);
  assert.deepEqual([...initial.entries()], [['r1', 0], ['r2', 1], ['r3', 0], ['r4', 1]]);

  // 顶部插入 1 条新行：既有行全部保持原底色，新行取反色 1
  const afterOne = assignStableZebraParity(rows('n1', 'r1', 'r2', 'r3', 'r4'), initial);
  assert.deepEqual([...afterOne.entries()], [['n1', 1], ['r1', 0], ['r2', 1], ['r3', 0], ['r4', 1]]);

  // 再连续插入 3 条新行：既有行依然保持原底色，整表严格白灰交替
  const afterThree = assignStableZebraParity(rows('n4', 'n3', 'n2', 'n1', 'r1', 'r2'), afterOne);
  assert.deepEqual([...afterThree.entries()], [['n4', 0], ['n3', 1], ['n2', 0], ['n1', 1], ['r1', 0], ['r2', 1]]);

  // 翻页/切筛选（无交集）：重新按 0, 1, 0 分配
  const nextPage = assignStableZebraParity(rows('p1', 'p2', 'p3'), afterThree);
  assert.deepEqual([...nextPage.entries()], [['p1', 0], ['p2', 1], ['p3', 0]]);
});
