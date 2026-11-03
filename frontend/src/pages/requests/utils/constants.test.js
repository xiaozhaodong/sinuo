import test from 'node:test';
import assert from 'node:assert/strict';

import { DEFAULT_VISIBLE_COLUMNS, TABLE_COLUMNS } from './constants.js';
import { buildColumnMenuItems } from './columnMenu.js';

test('会话和缓存率默认隐藏，列菜单可单独开启并恢复默认', () => {
  let visible = [...DEFAULT_VISIBLE_COLUMNS];
  const menu = () => buildColumnMenuItems(TABLE_COLUMNS, visible, (id) => { visible = [...visible, id]; }, () => { visible = [...DEFAULT_VISIBLE_COLUMNS]; });
  assert.deepEqual(TABLE_COLUMNS.filter((col) => !visible.includes(col.id)).map((col) => col.id), ['sessionId', 'cacheRatio']);
  for (const label of ['会话', '缓存率']) {
    const item = () => menu().find((entry) => entry.label === label);
    assert.equal(item().checked, false);
    item().onSelect();
    assert.equal(item().checked, true);
    menu().at(-1).onSelect();
    assert.equal(item().checked, false);
  }
});
