import test from 'node:test';
import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { suggestCachePrices, toPricingForm, toPricingPayload, validatePricingForm } from './utils/pricingForm.js';

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const inspectorPath = path.resolve(__dirname, './components/PricingInspector.jsx');
const pagePath = path.resolve(__dirname, './index.jsx');

test('定价支持 0.001 精度：文本输入不受原生 step 校验限制，3.125 原样保存', async () => {
  const source = await readFile(inspectorPath, 'utf8');
  assert.match(source, /inputMode="decimal"/);
  assert.doesNotMatch(source, /type="number"/);

  const form = { ...toPricingForm(null), modelName: 'x', inputPrice: '3.125' };
  assert.deepEqual(validatePricingForm(form, { isNew: true }), {});
  assert.equal(toPricingPayload(form).inputPrice, 3.125);
});

test('缓存价格建议保留 3 位小数', () => {
  const next = suggestCachePrices({ ...toPricingForm(null), inputPrice: '2.4' });
  assert.equal(next.cacheCreationPrice5m, '3');
  assert.equal(next.cacheCreationPrice1h, '4.8');
  assert.equal(next.cacheReadPrice, '0.24');
  assert.equal(suggestCachePrices({ ...toPricingForm(null), inputPrice: '0.0123' }).cacheReadPrice, '0.001');
});

test('页面不再使用 window.alert（WKWebView 不弹窗），错误走页内提示', async () => {
  const source = await readFile(pagePath, 'utf8');
  assert.doesNotMatch(source, /\balert\(/);
  assert.match(source, /setActionError\(resolveErrorMessage/);
});
