import test from 'node:test';
import assert from 'node:assert/strict';
import { resolveErrorMessage, toError } from './errors.js';

test('Error 实例取 message', () => {
  assert.equal(resolveErrorMessage(new Error('网络超时')), '网络超时');
});

test('ErrorFormatter 回传的 { message } 对象取 message', () => {
  const reason = { message: '账号「venlacy」仍被 Codex 模型 gpt-6-astra 使用，请先在「模型路由」中取消绑定' };
  assert.equal(resolveErrorMessage(reason), reason.message);
});

test('Wails 内部路径回传的字符串原样使用', () => {
  assert.equal(resolveErrorMessage('error parsing arguments: invalid ID'), 'error parsing arguments: invalid ID');
});

test('空值一律回退到兜底文案', () => {
  assert.equal(resolveErrorMessage(undefined), '操作失败');
  assert.equal(resolveErrorMessage(null), '操作失败');
  assert.equal(resolveErrorMessage(''), '操作失败');
  assert.equal(resolveErrorMessage('   '), '操作失败');
  assert.equal(resolveErrorMessage({}), '操作失败');
  assert.equal(resolveErrorMessage({ message: '  ' }), '操作失败');
  assert.equal(resolveErrorMessage(undefined, '删除失败'), '删除失败');
});

test('toError 保留 Error 实例，其余形态包装成 Error', () => {
  const original = new Error('原始错误');
  assert.equal(toError(original), original);
  assert.equal(toError({ message: '守卫拦截' }).message, '守卫拦截');
  assert.equal(toError('后端字符串').message, '后端字符串');
  assert.equal(toError({}).message, '操作失败');
});
