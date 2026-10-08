import test from 'node:test';
import assert from 'node:assert/strict';
import { isListNavigationKey, nextListId } from './listNavigation.js';

const ids = ['a', 'b', 'c'];

test('↑↓ 逐行移动，到边界停住', () => {
  assert.equal(nextListId(ids, 'a', 'ArrowDown'), 'b');
  assert.equal(nextListId(ids, 'c', 'ArrowDown'), 'c');
  assert.equal(nextListId(ids, 'b', 'ArrowUp'), 'a');
  assert.equal(nextListId(ids, 'a', 'ArrowUp'), 'a');
});

test('Home / End 跳首尾；没有选中项时 ↓ 落首行、↑ 落末行', () => {
  assert.equal(nextListId(ids, 'b', 'Home'), 'a');
  assert.equal(nextListId(ids, 'b', 'End'), 'c');
  assert.equal(nextListId(ids, null, 'ArrowDown'), 'a');
  assert.equal(nextListId(ids, 'missing', 'ArrowUp'), 'c');
});

test('空列表与非导航键返回 null', () => {
  assert.equal(nextListId([], 'a', 'ArrowDown'), null);
  assert.equal(nextListId(ids, 'a', 'Enter'), null);
  assert.equal(isListNavigationKey('Tab'), false);
});
