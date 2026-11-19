import test from 'node:test';
import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const sourcePath = path.resolve(__dirname, './AccountFormDialog.jsx');
const constantsPath = path.resolve(__dirname, '../utils/constants.js');
const formHookPath = path.resolve(__dirname, '../hooks/useAccountPoolForm.js');

test('AccountFormDialog supports 0.01 multiplier precision for api_key accounts', async () => {
  const source = await readFile(sourcePath, 'utf8');

  // 倍率输入框搬进了共用的 ChannelBillingSection，账号表单通过 step 参数传入 0.01
  assert.match(
    source,
    /<ChannelBillingSection[\s\S]*?step="0\.01"/,
    'expected account multiplier inputs to use step="0.01" so values like 0.05 are not rejected by native form validation'
  );
});

test('AccountFormDialog exposes explicit group selection and group-order copy', async () => {
  const source = await readFile(sourcePath, 'utf8');

  assert.match(source, /FormField label="组别"/);
  assert.match(source, /主组/);
  assert.match(source, /备组/);
  assert.match(source, /冷备/);
  assert.match(source, /组内顺序|组内优先级/);
  assert.match(source, /主组\s*->\s*备组\s*->\s*冷备|主组.*备组.*冷备/);
  assert.doesNotMatch(source, /相同 priority 视为同一层/);
});

test('AccountFormDialog uses 10-based validation for in-group order values', async () => {
  const source = await readFile(sourcePath, 'utf8');

  assert.match(source, /FormField label="组内顺序"/);
  assert.match(source, /min="10"/);
  assert.match(source, /step="10"/);
});

test('AccountFormDialog exposes editable model compatibility controls', async () => {
  const [dialogSource, hookSource] = await Promise.all([
    readFile(sourcePath, 'utf8'),
    readFile(formHookPath, 'utf8')
  ]);

  assert.match(dialogSource, /模型兼容/);
  assert.match(dialogSource, /启用模型兼容改写/);
  assert.match(dialogSource, /匹配模型/);
  assert.match(dialogSource, /替代模型/);
  assert.match(dialogSource, /添加规则/);
  assert.match(dialogSource, /删除模型兼容规则/);
  assert.match(dialogSource, /modelRewriteRules/);
  assert.doesNotMatch(dialogSource, /value="gpt-5\.4\*"/);
  assert.match(hookSource, /buildCodexModelRewriteRules/);
  assert.match(hookSource, /rules:\s*accountForm\.modelRewriteRules/);
  assert.match(hookSource, /model_rewrite_rules:\s*modelRewriteRules/);
});

test('AccountFormDialog exposes the API Key and ChatGPT OAuth zstd request compression switch', async () => {
  const [dialogSource, hookSource, constantsSource] = await Promise.all([
    readFile(sourcePath, 'utf8'),
    readFile(formHookPath, 'utf8'),
    readFile(constantsPath, 'utf8')
  ]);

  assert.match(dialogSource, /请求压缩/);
  assert.match(dialogSource, /向上游发送 zstd/);
  assert.match(dialogSource, /disabled={!supportsRequestCompression}/);
  assert.match(dialogSource, /API Key 和 ChatGPT OAuth 账号均可开启/);
  assert.match(hookSource, /enable_request_compression/);
  assert.match(constantsSource, /enableRequestCompression:\s*false/);
});

test('account form defaults new records to primary group and preserves group_key during edit and submit', async () => {
  const [constantsSource, hookSource] = await Promise.all([
    readFile(constantsPath, 'utf8'),
    readFile(formHookPath, 'utf8')
  ]);

  assert.match(constantsSource, /group_key:\s*'primary'/);
  assert.match(constantsSource, /priority:\s*'10'/);
  assert.match(hookSource, /group_key:\s*account\.(group_key|groupKey)/);
  assert.match(hookSource, /group_key:\s*accountForm\.group_key/);
});
