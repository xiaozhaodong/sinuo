import test from 'node:test';
import assert from 'node:assert/strict';
import { splitByKeyword } from './highlight.js';

test('按关键词切片，不区分大小写并保留原文大小写', () => {
  assert.deepEqual(splitByKeyword('Proxy proxy OK', 'proxy'), [
    { text: 'Proxy', match: true },
    { text: ' ', match: false },
    { text: 'proxy', match: true },
    { text: ' OK', match: false }
  ]);
});

test('正则特殊字符按字面量匹配，不再抛异常', () => {
  assert.deepEqual(splitByKeyword('call fn(a) [x]', '(a)'), [
    { text: 'call fn', match: false },
    { text: '(a)', match: true },
    { text: ' [x]', match: false }
  ]);
  assert.doesNotThrow(() => splitByKeyword('abc', '['));
});

test('没有关键词或文本为空时原样返回', () => {
  assert.deepEqual(splitByKeyword('abc', ''), [{ text: 'abc', match: false }]);
  assert.deepEqual(splitByKeyword(null, 'a'), []);
});
