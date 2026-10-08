import test from 'node:test';
import assert from 'node:assert/strict';
import { clampPaneHeight, parseStoredPaneHeight, parseStoredPaneVisible } from './paneSize.js';

test('clampPaneHeight 钳到上下限并取整', () => {
  assert.equal(clampPaneHeight(300.4, { min: 160, max: 500 }), 300);
  assert.equal(clampPaneHeight(80, { min: 160, max: 500 }), 160);
  assert.equal(clampPaneHeight(900, { min: 160, max: 500 }), 500);
});

test('clampPaneHeight 容器过矮时保底最小高度', () => {
  assert.equal(clampPaneHeight(300, { min: 160, max: 90 }), 160);
  assert.equal(clampPaneHeight(300, { min: 160, max: Number.NEGATIVE_INFINITY }), 160);
});

test('parseStoredPaneHeight 只接受正数', () => {
  assert.equal(parseStoredPaneHeight('320', 280), 320);
  assert.equal(parseStoredPaneHeight(null, 280), 280);
  assert.equal(parseStoredPaneHeight('', 280), 280);
  assert.equal(parseStoredPaneHeight('abc', 280), 280);
  assert.equal(parseStoredPaneHeight('-10', 280), 280);
  assert.equal(parseStoredPaneHeight('0', 280), 280);
});

test('parseStoredPaneVisible 只认精确的 true / false', () => {
  assert.equal(parseStoredPaneVisible('false'), false);
  assert.equal(parseStoredPaneVisible('true', false), true);
  assert.equal(parseStoredPaneVisible(null), true);
  assert.equal(parseStoredPaneVisible('0', false), false);
});
