import test from 'node:test';
import assert from 'node:assert/strict';
import {
  formatPriceCell,
  isPricingFormDirty,
  sortAndFilterPricings,
  suggestCachePrices,
  toPricingForm,
  toPricingPayload,
  validatePricingForm
} from './pricingForm.js';

const opus = {
  modelName: 'claude-opus-4-7',
  displayName: 'Claude Opus 4.7',
  description: '',
  inputPrice: 5,
  outputPrice: 25,
  cacheCreationPrice5m: 6.25,
  cacheCreationPrice1h: 10,
  cacheReadPrice: 0.5,
  isDefault: false
};

test('价格在表单里是字符串，保存时转回数字', () => {
  const form = toPricingForm(opus);
  assert.equal(form.inputPrice, '5');
  assert.equal(form.cacheReadPrice, '0.5');
  const payload = toPricingPayload({ ...form, displayName: '  Opus  ', inputPrice: '5.5' });
  assert.equal(payload.inputPrice, 5.5);
  assert.equal(payload.displayName, 'Opus');
  assert.equal(payload.cacheCreationPrice1h, 10);
});

test('新建表单沿用旧默认价，模型名称必填', () => {
  const form = toPricingForm(null);
  assert.equal(form.modelName, '');
  assert.equal(form.outputPrice, '15');
  assert.deepEqual(Object.keys(validatePricingForm(form, { isNew: true })), ['modelName']);
  assert.deepEqual(validatePricingForm({ ...form, modelName: 'x' }, { isNew: true }), {});
});

test('价格为空、非数字或负数都判为无效；输入中间态 "0." 合法', () => {
  const form = toPricingForm(opus);
  const errors = validatePricingForm({ ...form, inputPrice: '', outputPrice: 'abc', cacheReadPrice: '-1' }, { isNew: false });
  assert.deepEqual(Object.keys(errors).sort(), ['cacheReadPrice', 'inputPrice', 'outputPrice']);
  assert.deepEqual(validatePricingForm({ ...form, inputPrice: '0.' }, { isNew: false }), {});
});

test('脏检查：数值写法不同但值相同时不算修改', () => {
  const base = toPricingForm(opus);
  assert.equal(isPricingFormDirty(base, base), false);
  assert.equal(isPricingFormDirty({ ...base, inputPrice: ' 5 ' }, base), false);
  assert.equal(isPricingFormDirty({ ...base, inputPrice: '6' }, base), true);
  assert.equal(isPricingFormDirty({ ...base, isDefault: true }, base), true);
});

test('按输入价推算缓存价：×1.25 / ×2 / ×0.1，保留 3 位小数', () => {
  const next = suggestCachePrices({ ...toPricingForm(opus), inputPrice: '3' });
  assert.equal(next.cacheCreationPrice5m, '3.75');
  assert.equal(next.cacheCreationPrice1h, '6');
  assert.equal(next.cacheReadPrice, '0.3');
});

test('表格价格：缓存写为 0 显示破折号，输入 / 输出的 0 照常显示', () => {
  assert.equal(formatPriceCell(0, { zeroAsDash: true }), '—');
  assert.equal(formatPriceCell(0), '$0');
  assert.equal(formatPriceCell(0.125), '$0.125');
  assert.equal(formatPriceCell(0.1 + 0.2), '$0.3');
});

test('默认定价置顶，其余按模型 ID 排序；搜索同时匹配 ID 与显示名', () => {
  const list = [
    { modelName: 'gpt-5.5', displayName: 'GPT-5.5', isDefault: false },
    { modelName: '_default', displayName: '默认定价', isDefault: true },
    { modelName: 'claude-opus-4-7', displayName: 'Claude Opus 4.7', isDefault: false }
  ];
  assert.deepEqual(sortAndFilterPricings(list, '').map((item) => item.modelName), ['_default', 'claude-opus-4-7', 'gpt-5.5']);
  assert.deepEqual(sortAndFilterPricings(list, 'opus').map((item) => item.modelName), ['claude-opus-4-7']);
  assert.deepEqual(sortAndFilterPricings(list, 'GPT').map((item) => item.modelName), ['gpt-5.5']);
});
