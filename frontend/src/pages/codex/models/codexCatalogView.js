import { tryParseCapabilities } from '../../account-pool/utils/codexModelCatalog.js';

export const formatContextWindow = (value) => {
  if (!Number.isInteger(value) || value <= 0) return '—';
  if (value >= 1_000_000) return `${Math.round(value / 100_000) / 10}M`;
  if (value >= 1_000) return `${Math.round(value / 100) / 10}K`;
  return String(value);
};

export const summarizeCodexModel = (model) => {
  const { metadata } = tryParseCapabilities(model);
  const levels = Array.isArray(metadata?.supported_reasoning_levels)
    ? metadata.supported_reasoning_levels.map((level) => level?.effort).filter(Boolean)
    : [];
  return {
    contextWindow: formatContextWindow(metadata?.context_window),
    reasoningRange: levels.length > 1 ? `${levels[0]}–${levels.at(-1)}` : levels[0] || '—',
    reasoningTitle: levels.join('、'),
    visibility: metadata?.visibility
  };
};

export const codexModelStatusTags = (model, summary = summarizeCodexModel(model)) => {
  const tags = [];
  if (model.capabilities_error) {
    tags.push({ key: 'invalid', label: '能力无效', tone: 'danger', title: model.capabilities_error });
  } else if (!model.capabilities_json?.trim()) {
    // 停用的模型本来就不对外展示，待补全只做灰色提示
    tags.push({ key: 'incomplete', label: '能力待补全', tone: model.enabled ? 'warn' : 'muted', title: '能力待补全：不会出现在 Codex 模型菜单' });
  }
  if (model.deprecated) tags.push({ key: 'deprecated', label: '已弃用', tone: 'muted' });
  if (summary.visibility === 'hide') {
    tags.push({ key: 'hidden', label: '菜单隐藏', tone: 'muted', title: '/v1/models 中保留，Codex 模型菜单不显示' });
  } else if (summary.visibility === 'none') {
    tags.push({ key: 'unlisted', label: '不列出', tone: 'muted' });
  }
  return tags;
};

// 能力无效不论是否启用都要处理：后端保存时校验整份目录，任一条无效都会让保存失败。
// 待补全只在启用时才算：停用的模型（如默认关闭的历史预设）不会出现在 Codex 模型菜单里。
export const codexModelNeedsAttention = (model) => Boolean(model.capabilities_error)
  || (model.enabled && !model.capabilities_json?.trim());

export const filterCodexModels = (models, { query = '', filter = 'all' } = {}) => {
  const needle = query.trim().toLowerCase();
  return models.filter((model) => {
    if (filter === 'enabled' && !model.enabled) return false;
    if (filter === 'attention' && !codexModelNeedsAttention(model)) return false;
    return !needle || [model.id, model.display_name].some((value) => String(value || '').toLowerCase().includes(needle));
  }).sort((a, b) => Number(Boolean(a.deprecated)) - Number(Boolean(b.deprecated)) || a.id.localeCompare(b.id));
};

export const STATUS_TAG_CLASSES = {
  danger: 'border-danger-line text-danger',
  warn: 'border-warn-line text-warn',
  muted: 'border-line text-fg-muted'
};
