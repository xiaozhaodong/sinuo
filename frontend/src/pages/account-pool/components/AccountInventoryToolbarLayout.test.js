import test from 'node:test';
import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const sourcePath = path.resolve(__dirname, './AccountInventoryFilters.jsx');
const sectionSourcePath = path.resolve(__dirname, './AccountInventorySection.jsx');

test('top inventory toolbar keeps selected-count copy out of the header row so filters do not get squeezed into a wrap', async () => {
  const source = await readFile(sourcePath, 'utf8');

  assert.doesNotMatch(source, /已选 \{selectedCount\}/);
  assert.match(source, /\{selectedCount\} 个已选/);
});

test('top inventory toolbar keeps query and result tools on one shared baseline', async () => {
  const source = await readFile(sourcePath, 'utf8');

  assert.match(source, /单行工具栏：查询条件与结果工具共用一条基线/);
  assert.match(source, /flex flex-wrap items-center gap-x-3 gap-y-2/);
  assert.match(source, /FILTER_SHORT_LABELS/);
  // 2026-10-07 桌面化：筛选改为标签写在按钮里的弹出按钮（不再靠逐项定宽防折行），选项走锚定菜单
  assert.match(source, /aria-haspopup="menu"/);
  assert.match(source, /onOpenMenu\?\.\(event\.currentTarget/);
  assert.match(source, /当前筛选结果：\$\{resultCount\} 个账号/);
  assert.match(source, /<FilterField[\s\S]*config=\{sortConfig\}[\s\S]*showLabel=\{false\}/);
  assert.match(source, /aria-label="重置搜索、筛选与排序"/);
  assert.doesNotMatch(source, /overflow-hidden rounded-lg border/);
  assert.doesNotMatch(source, /flex-\[3_1_560px\]/);
  assert.doesNotMatch(source, /结果控制行/);

  const sectionSource = await readFile(sectionSourcePath, 'utf8');
  assert.match(sectionSource, /<ViewModeSwitcher[^>]*compact/);
  assert.match(sectionSource, /onOpenMenu=\{openMenuAt\}/);
});
