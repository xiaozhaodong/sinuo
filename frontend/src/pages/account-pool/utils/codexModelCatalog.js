import { resolveErrorMessage } from '../../../utils/errors.js';

export const MAX_CATALOG_BYTES = 16 * 1024 * 1024;
export const MAX_CATALOG_MODELS = 1000;
const MAX_MODEL_CAPABILITY_BYTES = 2 * 1024 * 1024;
const MAX_MODEL_ID_CHARS = 256;
const MAX_DISPLAY_NAME_BYTES = 4096;
const MAX_DESCRIPTION_BYTES = 16384;
const INT32_MIN = -2147483648;
const INT32_MAX = 2147483647;

// 表单中的常用思考等级及其顺序；Codex 也接受其他非空等级名，编辑时原样保留。
export const REASONING_LEVELS = ['none', 'minimal', 'low', 'medium', 'high', 'xhigh', 'max', 'ultra'];
// Codex 缺省 input_modalities 时按文本 + 图片处理。
export const DEFAULT_INPUT_MODALITIES = ['text', 'image'];

// 目录里找不到同等级说明时的兜底文案，取自 Codex 0.156.1 内置目录。
const REASONING_LEVEL_DESCRIPTIONS = {
  none: 'Respond without extended reasoning',
  minimal: 'Minimal reasoning for the fastest responses',
  low: 'Fast responses with lighter reasoning',
  medium: 'Balances speed and reasoning depth for everyday tasks',
  high: 'Greater reasoning depth for complex problems',
  xhigh: 'Extra high reasoning depth for complex problems',
  max: 'Maximum reasoning depth for the hardest problems',
  ultra: 'Maximum reasoning with automatic task delegation'
};

// 以下规则与后端 internal/service/codex_model_capabilities.go 保持一致（Codex 0.156.1 ModelInfo 解析规则 + 少量语义护栏），
// 共用用例见 internal/service/testdata/codex_capabilities_cases.json。
const IDENTITY_FIELDS = ['slug', 'display_name', 'description'];
const REQUIRED_FIELDS = [
  'supported_reasoning_levels', 'shell_type', 'visibility', 'supported_in_api',
  'priority', 'support_verbosity', 'truncation_policy', 'experimental_supported_tools'
];
// nullable 对应 Codex 的 Option 字段；带默认值的非 Option 字段可以缺省，但不能为 null。
const CHOICE_FIELDS = [
  { key: 'shell_type', nullable: false, choices: ['unified_exec', 'disabled', 'default', 'local', 'shell_command'] },
  { key: 'visibility', nullable: false, choices: ['list', 'hide', 'none'] },
  { key: 'default_reasoning_summary', nullable: false, choices: ['auto', 'concise', 'detailed', 'none'] },
  { key: 'web_search_tool_type', nullable: false, choices: ['text', 'text_and_image'] },
  { key: 'default_verbosity', nullable: true, choices: ['low', 'medium', 'high'] },
  { key: 'apply_patch_tool_type', nullable: true, choices: ['freeform'] }
];
const BOOL_FIELDS = [
  'supported_in_api', 'support_verbosity', 'include_skills_usage_instructions',
  'include_plugin_usage_instructions', 'include_apps_usage_instructions',
  'supports_reasoning_summary_parameter', 'supports_image_detail_original',
  'supports_search_tool', 'supports_experimental_context', 'use_responses_lite',
  'supports_reasoning_effort_updates', 'node_repl_auto_review_required', 'node_repl_disabled'
];
const INPUT_MODALITIES = ['text', 'image', 'audio'];
const TRUNCATION_MODES = ['bytes', 'tokens'];

const isObject = value => value !== null && typeof value === 'object' && !Array.isArray(value);
const byteLength = text => new TextEncoder().encode(text).length;
const isStringArray = value => Array.isArray(value) && value.every(item => typeof item === 'string');
const isPositiveSafeInteger = value => Number.isSafeInteger(value) && value > 0;

// 拒绝无法在表单中无损往返的数值，避免未知扩展字段被悄悄舍入。
const assertSafeNumbers = value => {
  if (typeof value === 'number' && (!Number.isFinite(value) || (Number.isInteger(value) && !Number.isSafeInteger(value)))) {
    throw new Error('目录包含无法无损编辑的数值，请使用安全范围内的数值');
  }
  if (value && typeof value === 'object') Object.values(value).forEach(assertSafeNumbers);
};

// 读取可选字段：缺失、或允许为 null 时的 null 返回 undefined；不允许的 null 直接报错。
const readOptional = (metadata, key, nullable) => {
  if (!Object.hasOwn(metadata, key)) return undefined;
  if (metadata[key] !== null) return metadata[key];
  if (nullable) return undefined;
  throw new Error(`${key} 不能为 null`);
};

const checkRequiredFields = metadata => {
  const missing = REQUIRED_FIELDS.find(key => metadata[key] === undefined || metadata[key] === null);
  if (missing) throw new Error(`能力目录缺少 ${missing}，请导入完整模型或使用模板`);
};

const checkChoiceFields = metadata => {
  CHOICE_FIELDS.forEach(({ key, nullable, choices }) => {
    const value = readOptional(metadata, key, nullable);
    if (value !== undefined && !choices.includes(value)) throw new Error(`${key} 的值无效`);
  });
};

const checkBoolFields = metadata => {
  BOOL_FIELDS.forEach(key => {
    if (Object.hasOwn(metadata, key) && typeof metadata[key] !== 'boolean') throw new Error(`${key} 必须为布尔值`);
  });
};

const checkPriority = ({ priority }) => {
  if (!Number.isInteger(priority) || priority < INT32_MIN || priority > INT32_MAX) throw new Error('priority 必须为 32 位整数');
};

const readPositiveInteger = (metadata, key, nullable) => {
  const value = readOptional(metadata, key, nullable);
  if (value !== undefined && !isPositiveSafeInteger(value)) throw new Error(`${key} 必须为正的安全整数`);
  return value;
};

const checkContextWindow = metadata => {
  const window = readPositiveInteger(metadata, 'context_window', true);
  const maximum = readPositiveInteger(metadata, 'max_context_window', true);
  if (window !== undefined && maximum !== undefined && maximum < window) throw new Error('最大上下文不能小于上下文窗口');
  const percent = readPositiveInteger(metadata, 'effective_context_window_percent', false);
  if (percent !== undefined && percent > 100) throw new Error('有效上下文比例必须为 1–100');
};

const checkReasoningLevels = metadata => {
  const levels = metadata.supported_reasoning_levels;
  if (!Array.isArray(levels)) throw new Error('supported_reasoning_levels 必须是思考等级数组');
  const efforts = levels.map(level => (isObject(level) ? level.effort : undefined));
  const invalid = levels.some(level => !isObject(level) || typeof level.effort !== 'string' || !level.effort || typeof level.description !== 'string');
  if (invalid || new Set(efforts).size !== efforts.length) throw new Error('思考等级必须非空、不重复且包含 description');
  const defaultLevel = readOptional(metadata, 'default_reasoning_level', true);
  if (defaultLevel !== undefined && !efforts.includes(defaultLevel)) throw new Error('默认思考等级必须属于支持的等级集合');
};

const checkStringArrays = metadata => {
  ['experimental_supported_tools', 'additional_speed_tiers', 'input_modalities'].forEach(key => {
    if (!Object.hasOwn(metadata, key)) return;
    if (!isStringArray(metadata[key])) throw new Error(`${key} 必须是字符串数组`);
  });
  const modalities = metadata.input_modalities;
  if (modalities === undefined) return;
  if (!modalities.length) throw new Error('至少选择一种输入能力');
  if (modalities.some(value => !INPUT_MODALITIES.includes(value))) throw new Error(`输入能力仅支持 ${INPUT_MODALITIES.join('/')}`);
};

const checkTruncationPolicy = ({ truncation_policy: policy }) => {
  const valid = isObject(policy) && TRUNCATION_MODES.includes(policy.mode) && Number.isSafeInteger(policy.limit) && policy.limit >= 0;
  if (!valid) throw new Error('truncation_policy 必须包含 bytes/tokens 模式和非负整数 limit');
};

// 要求非空指令：Codex 只要求字段存在，但空指令会让模型失去系统提示。
const checkInstructions = metadata => {
  const instructions = readOptional(metadata, 'base_instructions', true);
  if (instructions !== undefined && typeof instructions !== 'string') throw new Error('base_instructions 必须为字符串或 null');
  const messages = readOptional(metadata, 'model_messages', true);
  const template = messages === undefined ? undefined : messages.instructions_template;
  if (messages !== undefined && (!isObject(messages) || (template != null && typeof template !== 'string'))) {
    throw new Error('model_messages 必须为对象或 null，其中 instructions_template 必须为字符串或 null');
  }
  if (!instructions?.trim() && !template?.trim()) throw new Error('需要完整的 base_instructions 或 model_messages.instructions_template；请先导入完整目录或复制已有模型');
};

export const validateCapabilities = metadata => {
  [
    checkRequiredFields,
    checkChoiceFields,
    checkBoolFields,
    checkPriority,
    checkContextWindow,
    checkReasoningLevels,
    checkStringArrays,
    checkTruncationPolicy,
    checkInstructions
  ].forEach(check => check(metadata));
};

export const parseCapabilities = model => {
  if (!model?.capabilities_json?.trim()) return null;
  if (byteLength(model.capabilities_json) > MAX_MODEL_CAPABILITY_BYTES) throw new Error('单个模型的能力目录超过 2 MiB');
  const metadata = JSON.parse(model.capabilities_json);
  if (!isObject(metadata)) throw new Error('能力 JSON 必须是对象');
  assertSafeNumbers(metadata);
  const duplicated = IDENTITY_FIELDS.find(key => Object.hasOwn(metadata, key));
  if (duplicated) throw new Error(`请在基本信息中填写 ${duplicated}，不要在能力 JSON 中重复定义`);
  return metadata;
};

// 不抛错的解析：已保存的能力损坏时交给编辑器进入高级模式修复，而不是让界面渲染失败。
export const tryParseCapabilities = model => {
  try {
    return { metadata: parseCapabilities(model), error: '' };
  } catch (err) {
    return { metadata: null, error: resolveErrorMessage(err, '能力 JSON 无法解析') };
  }
};

export const hasValidCapabilities = model => Boolean(model?.capabilities_json?.trim()) && !model.capabilities_error;

const isValidModelId = id => id && [...id].length <= MAX_MODEL_ID_CHARS && !/[\s\p{Cc}]/u.test(id);

export const validateModelDraft = model => {
  const id = String(model.id ?? '').trim();
  if (!isValidModelId(id)) throw new Error(`模型 ID 必须为 1–${MAX_MODEL_ID_CHARS} 个字符且不能含空白或控制字符`);
  if (typeof model.display_name !== 'string' || typeof model.description !== 'string') throw new Error('名称和描述必须为文本');
  if (byteLength(model.display_name) > MAX_DISPLAY_NAME_BYTES || byteLength(model.description) > MAX_DESCRIPTION_BYTES) throw new Error('名称或描述过长');
  const metadata = parseCapabilities(model);
  if (metadata) validateCapabilities(metadata);
  // 通过前端校验后清除旧的后端错误标记，保存时后端会再次严格校验。
  return { ...model, id, capabilities_error: '' };
};

const toImportedModel = (metadata, index, current) => {
  if (!isObject(metadata) || typeof metadata.slug !== 'string' || typeof metadata.display_name !== 'string') {
    throw new Error(`第 ${index + 1} 个模型缺少 slug/display_name`);
  }
  const { slug, display_name, description, ...capabilities } = metadata;
  const id = slug.trim();
  const existing = current.get(id);
  try {
    return validateModelDraft({
      id,
      object: existing?.object || 'model',
      owned_by: existing?.owned_by || 'openai',
      source: existing?.source || 'custom',
      enabled: existing?.enabled ?? true,
      deprecated: existing?.deprecated ?? false,
      display_name,
      // Codex 的 description 是可选字段，null 与缺失等价。
      description: description ?? '',
      capabilities_json: JSON.stringify(capabilities)
    });
  } catch (err) {
    throw new Error(`模型 ${id || index + 1}：${resolveErrorMessage(err, '校验失败')}`);
  }
};

export const parseCatalogImport = (text, currentModels = []) => {
  if (byteLength(text) > MAX_CATALOG_BYTES) throw new Error('目录文件不能超过 16 MiB');
  const catalog = JSON.parse(text);
  if (!isObject(catalog) || !Array.isArray(catalog.models) || !catalog.models.length) throw new Error('请选择包含非空 models 数组的完整 Codex 目录');
  if (catalog.models.length > MAX_CATALOG_MODELS) throw new Error(`模型目录最多允许 ${MAX_CATALOG_MODELS} 个条目`);
  assertSafeNumbers(catalog);
  const current = new Map(currentModels.map(model => [model.id, model]));
  const seen = new Set();
  const models = catalog.models.map((metadata, index) => {
    const model = toImportedModel(metadata, index, current);
    if (seen.has(model.id)) throw new Error(`导入目录中模型 ID ${model.id} 重复`);
    seen.add(model.id);
    return model;
  });
  return { models, conflicts: models.filter(model => current.has(model.id)).map(model => model.id) };
};

export const mergeImportedModels = (currentModels, importedModels) => {
  const merged = new Map(currentModels.map(model => [model.id, model]));
  importedModels.forEach(model => merged.set(model.id, model));
  return [...merged.values()];
};

export const createModelDraft = template => ({
  id: '', object: 'model', owned_by: template?.owned_by || 'openai', source: 'custom',
  enabled: true, deprecated: false, display_name: '', description: template?.description || '',
  capabilities_json: template?.capabilities_json || '', capabilities_error: ''
});

const levelRank = effort => {
  const index = REASONING_LEVELS.indexOf(effort);
  return index < 0 ? REASONING_LEVELS.length : index;
};

// 新增等级的说明优先沿用目录中同等级的文案，Codex 会把它展示在思考等级菜单里。
const findLevelDescription = (templates, effort) => {
  for (const template of templates) {
    const levels = tryParseCapabilities(template).metadata?.supported_reasoning_levels;
    const match = Array.isArray(levels) && levels.find(level => level?.effort === effort && typeof level.description === 'string');
    if (match) return match.description;
  }
  return REASONING_LEVEL_DESCRIPTIONS[effort] || effort;
};

// 勾选时按常用顺序插入（Codex 按数组顺序展示），非标准等级保持原相对顺序排在末尾。
export const toggleReasoningLevel = (levels, effort, templates = []) => {
  if (levels.some(level => level.effort === effort)) return levels.filter(level => level.effort !== effort);
  const next = [...levels, { effort, description: findLevelDescription(templates, effort) }];
  return next.sort((a, b) => levelRank(a.effort) - levelRank(b.effort));
};
