// ============================================
// modelConsistency - 模型与思考等级一致性判定（纯函数）
// 2026-09-22（请求追踪 · 模型与思考等级一致性监控 §7）
//
// 后端只存原始列（request_model / upstream_model / response_model /
// request_reasoning / response_reasoning），判定完全在这里派生：
//   - 一致性只看 responseModel，永远不看 model（计费模型，可回退请求模型）
//   - 别名只认「日期快照」展开：reference + '-' + YYYYMMDD（Anthropic）或 YYYY-MM-DD（OpenAI）
//   - 思考等级：Codex 比 effort；Claude 只能校验有无思考块
// ============================================

export const MODEL_LEVELS = Object.freeze({
  CONSISTENT: 'consistent',
  ALIAS: 'alias',
  MISMATCH: 'mismatch',
  NO_RESPONSE: 'no_response',
  LEGACY: 'legacy'
});

export const REASONING_LEVELS = Object.freeze({
  CONSISTENT: 'consistent',
  MISMATCH: 'mismatch',
  HINT: 'hint',
  UNVERIFIABLE: 'unverifiable',
  NONE: 'none'
});

const MISSING_MODEL_TOKENS = new Set(['', 'unknown', 'default']);
const DATE_SNAPSHOT_SUFFIX = /^-(\d{8}|\d{4}-\d{2}-\d{2})$/;
const LATEST_SUFFIX = '-latest';

const normalizeModel = (value) => {
  const normalized = String(value ?? '').trim().toLowerCase();
  return MISSING_MODEL_TOKENS.has(normalized) ? '' : normalized;
};

/**
 * 解析归一化思考串：'thinking:adaptive effort:xhigh' → { thinking, effort, ... }
 * 非法片段忽略；与后端 internal/reasoning 同文法。
 */
export const parseReasoning = (str) => {
  const fields = {};
  String(str ?? '').split(/\s+/).forEach((part) => {
    const index = part.indexOf(':');
    if (index <= 0 || index === part.length - 1) {
      return;
    }
    fields[part.slice(0, index)] = part.slice(index + 1);
  });
  return fields;
};

const formatNumber = (value) => {
  const number = Number(value);
  return Number.isFinite(number) ? number.toLocaleString() : String(value);
};

/**
 * 人类可读的思考描述：'adaptive · xhigh' / 'xhigh' / '有思考块' / '未指定'
 */
export const formatReasoning = (str) => {
  const fields = parseReasoning(str);
  const parts = [];
  if (fields.thinking === 'present') {
    parts.push('有思考块');
  } else if (fields.thinking === 'absent') {
    parts.push('无思考块');
  } else if (fields.thinking) {
    parts.push(fields.thinking);
  }
  if (fields.budget) {
    parts.push(`预算 ${formatNumber(fields.budget)}`);
  }
  if (fields.effort) {
    parts.push(fields.effort);
  }
  if (fields.reasoning_tokens !== undefined) {
    parts.push(`推理 ${formatNumber(fields.reasoning_tokens)} tok`);
  }
  return parts.length > 0 ? parts.join(' · ') : '未指定';
};

const isDateSnapshotOf = (response, base) => (
  response.startsWith(base) && DATE_SNAPSHOT_SUFFIX.test(response.slice(base.length))
);

/**
 * 模型一致性判定（§7.1）
 * @returns {{ level: string, rewritten: boolean, reference: string, response: string }}
 */
export const assessModel = ({ requestModel, upstreamModel, responseModel } = {}) => {
  const request = normalizeModel(requestModel);
  const upstream = normalizeModel(upstreamModel);
  const response = normalizeModel(responseModel);
  const reference = upstream || request;
  const rewritten = Boolean(upstream && request && upstream !== request);
  const base = { rewritten, reference, response };

  if (!reference) {
    return { ...base, level: MODEL_LEVELS.LEGACY };
  }
  if (!response) {
    return { ...base, level: MODEL_LEVELS.NO_RESPONSE };
  }
  if (response === reference) {
    return { ...base, level: MODEL_LEVELS.CONSISTENT };
  }
  if (isDateSnapshotOf(response, reference)) {
    return { ...base, level: MODEL_LEVELS.ALIAS };
  }
  if (reference.endsWith(LATEST_SUFFIX) && isDateSnapshotOf(response, reference.slice(0, -LATEST_SUFFIX.length))) {
    return { ...base, level: MODEL_LEVELS.ALIAS };
  }
  return { ...base, level: MODEL_LEVELS.MISMATCH };
};

const assessCodexReasoning = (request, response) => {
  if (!request.effort) {
    return { level: REASONING_LEVELS.NONE, note: '' };
  }
  if (!response.effort) {
    return { level: REASONING_LEVELS.UNVERIFIABLE, note: '上游未回显' };
  }
  return request.effort === response.effort
    ? { level: REASONING_LEVELS.CONSISTENT, note: '' }
    : { level: REASONING_LEVELS.MISMATCH, note: '' };
};

const assessClaudeReasoning = (request, response) => {
  if (!request.thinking) {
    return { level: REASONING_LEVELS.NONE, note: '' };
  }
  if (!response.thinking) {
    return { level: REASONING_LEVELS.UNVERIFIABLE, note: '流未收尾' };
  }
  if (request.thinking === 'disabled') {
    return response.thinking === 'present'
      ? { level: REASONING_LEVELS.MISMATCH, note: '' }
      : { level: REASONING_LEVELS.CONSISTENT, note: '' };
  }
  const effortNote = request.effort ? '等级无法校验' : '';
  if (response.thinking === 'absent') {
    // enabled 是强制思考，上游未返回思考块视为不一致；adaptive 允许简单请求不思考，只提示
    return request.thinking === 'enabled'
      ? { level: REASONING_LEVELS.MISMATCH, note: '' }
      : { level: REASONING_LEVELS.HINT, note: 'adaptive 在简单请求上可能不思考' };
  }
  return { level: REASONING_LEVELS.CONSISTENT, note: effortNote };
};

/**
 * 思考等级判定（§7.2）
 * @returns {{ level: string, requestLabel: string, responseLabel: string, note: string }}
 */
export const assessReasoning = ({ requestFamily, requestReasoning, responseReasoning } = {}) => {
  const request = parseReasoning(requestReasoning);
  const response = parseReasoning(responseReasoning);
  const labels = {
    requestLabel: formatReasoning(requestReasoning),
    responseLabel: formatReasoning(responseReasoning)
  };
  const hasRequest = Object.keys(request).length > 0;
  const hasResponse = Object.keys(response).length > 0;
  if (!hasRequest && !hasResponse) {
    return { ...labels, level: REASONING_LEVELS.NONE, note: '' };
  }
  const verdict = requestFamily === 'codex'
    ? assessCodexReasoning(request, response)
    : assessClaudeReasoning(request, response);
  return { ...labels, ...verdict };
};

/**
 * 列表 ModelTag 的告警：仅 mismatch（模型或思考任一）触发，否则 null。
 * @returns {{ level: 'mismatch', lines: string[] } | null}
 */
export const buildModelAlert = (request = {}) => {
  const model = assessModel(request);
  const reasoning = assessReasoning(request);
  const modelMismatch = model.level === MODEL_LEVELS.MISMATCH;
  const reasoningMismatch = reasoning.level === REASONING_LEVELS.MISMATCH;
  if (!modelMismatch && !reasoningMismatch) {
    return null;
  }
  const lines = [];
  if (modelMismatch) {
    // 发生改写时 reference 是改写后实际发出的上游模型，不是客户端请求模型
    lines.push(`${model.rewritten ? '上游' : '请求'}  ${model.reference}`);
    lines.push(`响应  ${model.response}`);
  }
  if (reasoningMismatch) {
    lines.push(`思考  ${reasoning.requestLabel} → ${reasoning.responseLabel}`);
  }
  if (modelMismatch) {
    lines.push(model.rewritten ? '上游返回与改写后的模型不一致' : '上游返回与请求不一致（未应用改写）');
  } else {
    lines.push('上游思考等级与请求不一致');
  }
  return { level: 'mismatch', lines };
};
