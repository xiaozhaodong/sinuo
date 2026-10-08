// Claude 模型目录的解析、校验与列表操作纯函数。

export const CLAUDE_MODELS_CATEGORY = 'claude_models';
export const CLAUDE_MODELS_KEY = 'catalog';
export const MAX_CLAUDE_MODELS = 1000;
export const MAX_CLAUDE_TEXT_LENGTH = 256;

const utf8Encoder = new TextEncoder();

// compareUtf8Bytes 复刻 Go 的 string `<`（UTF-8 字节序），与后端 NormalizeClaudeModelCatalog 对齐。
// localeCompare 依赖运行时 locale，JS 的 `<` 则按 UTF-16 code unit 比较；补充平面字符
// （例如 U+10000）与 U+E000 的顺序会因此和后端不一致。
export const compareUtf8Bytes = (left, right) => {
  const shared = Math.min(left.length, right.length);
  for (let index = 0; index < shared; index += 1) {
    if (left[index] !== right[index]) return left[index] - right[index];
  }
  return left.length - right.length;
};

const compareModelIds = (left, right) => compareUtf8Bytes(
  utf8Encoder.encode(left.id),
  utf8Encoder.encode(right.id)
);

export const parseClaudeCatalog = (rawValue) => {
  if (rawValue == null || rawValue === '') return { enabled: true, models: [] };

  let value = rawValue;
  if (typeof rawValue === 'string') {
    value = JSON.parse(rawValue);
  }

  return {
    enabled: value?.enabled !== false,
    models: Array.isArray(value?.models)
      ? value.models.map((model) => ({
        id: String(model?.id ?? ''),
        display_name: String(model?.display_name ?? ''),
        enabled: model?.enabled !== false
      }))
      : []
  };
};

export const normalizeClaudeCatalogForSave = (catalog) => {
  const models = (Array.isArray(catalog?.models) ? catalog.models : []).map((model) => {
    const id = String(model?.id ?? '').trim();
    return {
      id,
      display_name: String(model?.display_name ?? '').trim() || id,
      enabled: Boolean(model?.enabled)
    };
  });

  models.sort((left, right) => {
    if (left.enabled !== right.enabled) return left.enabled ? -1 : 1;
    return compareModelIds(left, right);
  });
  return { enabled: catalog?.enabled !== false, models };
};

export const validateClaudeModelDraft = (draft = {}, models = [], originalId = '') => {
  const errors = {};
  const id = String(draft.id ?? '').trim();
  const displayName = String(draft.display_name ?? '').trim();

  if (!id) {
    errors.id = '请输入模型 ID';
  } else if ([...id].length > MAX_CLAUDE_TEXT_LENGTH) {
    errors.id = `模型 ID 超过 ${MAX_CLAUDE_TEXT_LENGTH} 个字符`;
  } else if (/\s/u.test(id) || /[\p{Cc}\p{Cf}]/u.test(id)) {
    errors.id = `模型 ID “${id}”不能包含空白或控制字符`;
  } else if (models.some((model) => model.id.trim() === id && model.id.trim() !== originalId)) {
    errors.id = `模型 ID “${id}”重复`;
  }

  if ([...displayName].length > MAX_CLAUDE_TEXT_LENGTH) {
    errors.name = `显示名称超过 ${MAX_CLAUDE_TEXT_LENGTH} 个字符`;
  } else if (/[\p{Cc}\p{Cf}]/u.test(displayName)) {
    errors.name = '显示名称不能包含控制字符';
  }

  return errors;
};

export const upsertClaudeModel = (models = [], model, originalId = '') => {
  const next = Array.isArray(models) ? [...models] : [];
  const index = originalId ? next.findIndex((item) => item.id === originalId) : -1;
  if (index < 0) return [...next, model];
  next[index] = model;
  return next;
};

export const removeClaudeModel = (models = [], id) => models.filter((model) => model.id !== id);

export const setClaudeModelEnabled = (models = [], id, enabled) => models.map((model) => (
  model.id === id ? { ...model, enabled: Boolean(enabled) } : model
));

export const filterClaudeModels = (models = [], { query = '', filter = 'all' } = {}) => {
  const normalizedQuery = String(query || '').trim().toLowerCase();
  return models
    .filter((model) => filter === 'all' || (filter === 'enabled' ? model.enabled : !model.enabled))
    .filter((model) => !normalizedQuery
      || String(model.id || '').toLowerCase().includes(normalizedQuery)
      || String(model.display_name || '').toLowerCase().includes(normalizedQuery))
    .sort(compareModelIds);
};
