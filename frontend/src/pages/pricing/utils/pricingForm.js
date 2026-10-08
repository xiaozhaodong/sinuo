// ============================================
// 定价检查器表单的纯逻辑：字段定义、默认值、校验、脏检查、缓存价格建议
// 2026-10-07
// ============================================
// 价格在表单里按字符串保存（输入「0.」这类中间态时不被强转成 0），保存时再转成数字。

export const PRICE_FIELDS = Object.freeze([
  { key: 'inputPrice', label: '输入' },
  { key: 'outputPrice', label: '输出' },
  { key: 'cacheCreationPrice5m', label: '缓存写 5m', hint: '输入 × 1.25' },
  { key: 'cacheCreationPrice1h', label: '缓存写 1h', hint: '输入 × 2' },
  { key: 'cacheReadPrice', label: '缓存读取', hint: '输入 × 0.1' }
]);

const TEXT_FIELDS = ['modelName', 'displayName', 'description'];

// 新建定价的初始值沿用旧表单（Claude Sonnet 档位）
const NEW_PRICING_DEFAULTS = Object.freeze({
  modelName: '',
  displayName: '',
  description: '',
  inputPrice: '3',
  outputPrice: '15',
  cacheCreationPrice5m: '3.75',
  cacheCreationPrice1h: '6',
  cacheReadPrice: '0.3',
  isDefault: false
});

const CACHE_PRICE_DECIMALS = 3;

const toPriceText = (value) => (Number.isFinite(Number(value)) ? String(Number(value)) : '0');

export const toPricingForm = (pricing) => {
  if (!pricing) return { ...NEW_PRICING_DEFAULTS };
  return {
    modelName: pricing.modelName || '',
    displayName: pricing.displayName || '',
    description: pricing.description || '',
    ...Object.fromEntries(PRICE_FIELDS.map(({ key }) => [key, toPriceText(pricing[key])])),
    isDefault: Boolean(pricing.isDefault)
  };
};

const isValidPrice = (text) => String(text).trim() !== '' && Number.isFinite(Number(text)) && Number(text) >= 0;

export const validatePricingForm = (form, { isNew }) => {
  const errors = {};
  if (isNew && !form.modelName.trim()) errors.modelName = '模型名称不能为空';
  for (const { key } of PRICE_FIELDS) {
    if (!isValidPrice(form[key])) errors[key] = '请输入不小于 0 的数字';
  }
  return errors;
};

export const isPricingFormDirty = (form, base) => (
  TEXT_FIELDS.some((key) => form[key] !== base[key])
  || PRICE_FIELDS.some(({ key }) => String(form[key]).trim() !== String(base[key]))
  || form.isDefault !== base.isDefault
);

export const toPricingPayload = (form) => ({
  modelName: form.modelName.trim(),
  displayName: form.displayName.trim(),
  description: form.description.trim(),
  ...Object.fromEntries(PRICE_FIELDS.map(({ key }) => [key, Number(form[key])])),
  isDefault: form.isDefault
});

// 按 Anthropic 官方规则由输入价推算缓存价：5 分钟写 ×1.25、1 小时写 ×2、读取 ×0.1
export const suggestCachePrices = (form) => {
  const input = Number(form.inputPrice) || 0;
  const round = (value) => String(parseFloat(value.toFixed(CACHE_PRICE_DECIMALS)));
  return {
    ...form,
    cacheCreationPrice5m: round(input * 1.25),
    cacheCreationPrice1h: round(input * 2),
    cacheReadPrice: round(input * 0.1)
  };
};

// 表格展示：缓存写价为 0 表示该模型没有这项计费（如 OpenAI），显示「—」；输入 / 输出的 0 是真实价格
export const formatPriceCell = (value, { zeroAsDash = false } = {}) => {
  const number = Number(value) || 0;
  if (number === 0 && zeroAsDash) return '—';
  return `$${parseFloat(number.toFixed(4))}`;
};

// 默认定价置顶，其余按模型 ID 排序；query 同时匹配模型 ID 与显示名
export const sortAndFilterPricings = (pricings, query) => {
  const keyword = query.trim().toLowerCase();
  return pricings
    .filter((item) => !keyword
      || item.modelName.toLowerCase().includes(keyword)
      || (item.displayName || '').toLowerCase().includes(keyword))
    .sort((a, b) => (Number(b.isDefault) - Number(a.isDefault)) || a.modelName.localeCompare(b.modelName));
};
