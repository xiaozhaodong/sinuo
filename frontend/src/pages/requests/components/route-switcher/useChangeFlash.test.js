import test from 'node:test';
import assert from 'node:assert/strict';
import { changedKeys } from './useChangeFlash.js';

test('首次加载与空列表不产生高亮', () => {
  assert.deepEqual(changedKeys(new Map(), new Map([['auto', 'auto']])), []);
  assert.deepEqual(changedKeys(new Map(), new Map()), []);
});

test('值相同或仅调整排列顺序不产生高亮', () => {
  const previous = new Map([[1, 'true|auto|'], [2, 'true|manual_fixed|a']]);
  const next = new Map([[2, 'true|manual_fixed|a'], [1, 'true|auto|']]);
  assert.deepEqual(changedKeys(previous, next), []);
});

test('同一目标由优选改为固定也算策略变化', () => {
  const previous = new Map([[1, 'true|manual_preferred|a'], [2, 'true|auto|']]);
  const next = new Map([[1, 'true|manual_fixed|a'], [2, 'true|auto|']]);
  assert.deepEqual(changedKeys(previous, next), [1]);
});

test('只新增或删除条目不产生高亮', () => {
  assert.deepEqual(changedKeys(new Map([['a', '']]), new Map([['a', ''], ['b', 'active']])), []);
  assert.deepEqual(changedKeys(new Map([['a', 'active'], ['b', '']]), new Map([['b', '']])), []);
});

test('批量刷新只返回两边都存在且发生变化的键', () => {
  const previous = new Map([['removed', 'active'], ['auto', 'auto'], ['a', ''], ['same', '']]);
  const next = new Map([['auto', ''], ['a', 'active'], ['same', ''], ['new', 'active']]);
  assert.deepEqual(changedKeys(previous, next), ['auto', 'a']);
});

test('值为 undefined 的已有条目仍能识别后续变化', () => {
  assert.deepEqual(changedKeys(new Map([[7, undefined]]), new Map([[7, 'active'], [8, undefined]])), [7]);
});

test('键保持原始类型，不混淆字符串和数字 ID', () => {
  assert.deepEqual(changedKeys(new Map([[7, 'a']]), new Map([['7', 'b']])), []);
});
