import test from 'node:test';
import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

const here = dirname(fileURLToPath(import.meta.url));
const pageSourcePath = join(here, 'index.jsx');

test('隐私扫描参数保存失败时保留编辑浮层，仅在保存成功后关闭', async () => {
  const source = await readFile(pageSourcePath, 'utf8');

  assert.match(
    source,
    /const handleSave = async \(\) => \{\s*const saved = await onSave\(\{[\s\S]*?\}\);\s*if \(saved\) \{\s*setOpen\(false\);\s*\}\s*\};/
  );
  assert.match(
    source,
    /const runAction = async \(action\) => \{[\s\S]*?await action\(\);\s*return true;\s*\} catch \(err\) \{\s*setActionError\(err\.message \|\| String\(err\)\);\s*return false;\s*\}/
  );
});
