import test from 'node:test';
import assert from 'node:assert/strict';

import { DEFAULT_VISIBLE_COLUMNS, TABLE_COLUMNS } from './constants.js';
import { buildColumnMenuItems } from './columnMenu.js';

test('会话列默认隐藏，可从列菜单开启，恢复默认后再次隐藏', () => {
  let visible = [...DEFAULT_VISIBLE_COLUMNS];
  const session = TABLE_COLUMNS.find((column) => column.id === 'sessionId');
  assert.equal(session.defaultHidden, true);
  assert.equal(visible.includes('sessionId'), false);
  assert.deepEqual(visible, TABLE_COLUMNS.filter((column) => column.id !== 'sessionId').map((column) => column.id));
  const menu = buildColumnMenuItems(TABLE_COLUMNS, visible, (id) => { visible = [...visible, id]; }, () => { visible = [...DEFAULT_VISIBLE_COLUMNS]; });
  const item = menu.find((entry) => entry.label === '会话');
  assert.equal(item.checked, false);
  item.onSelect();
  assert.equal(visible.includes('sessionId'), true);
  menu.at(-1).onSelect();
  assert.equal(visible.includes('sessionId'), false);
});
