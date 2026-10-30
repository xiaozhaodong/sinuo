import { parseTimestamp } from '../../../utils/timezone.js';
import { UNSETTLED_STATUSES } from './constants.js';
import { formatTpsBadge } from './timing.js';

const ms = (value) => Number.isFinite(value) ? Math.max(0, value) : null;
const absoluteMs = (value) => {
  if (!value) return null;
  try { return parseTimestamp(value).getTime(); } catch { return null; }
};

export const hasStreamContent = (request) => request.isStreaming !== false && ms(request.firstContentMs) !== null;

// W ≤ E ≤ C ≤ T ≤ D；缺完成时刻时保留已观测时间，残差交给终态段。
export const streamPhaseBounds = (request) => {
  if (!hasStreamContent(request)) return null;
  const limit = request.duration > 0 ? request.duration : Infinity;
  const write = Math.min(ms(request.upstreamWriteMs) ?? 0, limit);
  const event = Math.min(write + (ms(request.firstTokenMs) ?? 0), limit);
  const done = ms(request.completionMs) === null ? null : Math.min(event + ms(request.completionMs), limit);
  const content = Math.min(Math.max(event, request.firstContentMs), done ?? limit);
  const text = Math.min(Math.max(content, ms(request.firstTextMs) ?? content), done ?? limit);
  return { write, event, content, text, done };
};

export const reasoningTokenCount = (request) => {
  if (request.requestFamily !== 'codex') return 0;
  const value = /(?:^|\s)reasoning_tokens:(\d+)(?:\s|$)/.exec(request.responseReasoning || '');
  return value ? Number(value[1]) : 0;
};

const hasClaudeThinking = (request) => request.requestFamily === 'claude'
  && (/(?:^|\s)thinking:present(?:\s|$)/.test(request.responseReasoning || '')
    || Boolean(request.reasoningHidden) || ms(request.firstReasoningMs) !== null);

// 两种速度共用采样边界；边界值本身有效。
const invalidSpeedWindow = (tokens, windowMs) => windowMs < 100 || tokens * 1000 > 10000 * windowMs;

// 生成吞吐只使用首事件到完成的实测耗时；不扣推理 token，也不以总耗时兜底。
export const generationThroughput = (request) => {
  const result = { value: null, reason: 'unknown', title: '缺少完整的输出用量或生成耗时' };
  if (UNSETTLED_STATUSES.has(request.status) || request.usageKnown === false
    || !Number.isFinite(request.outputTokens) || request.outputTokens < 0
    || (request.outputTokens === 0 && request.usageKnown !== true)
    || !Number.isFinite(request.completionMs) || request.completionMs < 0) return result;
  // 非流式按设计记 0ms 生成耗时，不是上游集中返回，不给「耗时过短」说明。
  if (request.isStreaming === false) return { ...result, reason: 'non-streaming', title: '非流式响应没有生成耗时' };
  if (invalidSpeedWindow(request.outputTokens, request.completionMs)) {
    return { ...result, reason: 'window', title: '生成耗时过短（上游一次性返回），无法计算' };
  }
  return { value: request.outputTokens * 1000 / request.completionMs, reason: '',
    title: '总输出 token ÷ 生成耗时（首个事件到完成）；包含推理 token，不含本地处理、建连、发送和首响' };
};

// Claude思考token无法扣除，从首事件起算；其余沿用magpie口径，时间点允许真实0。
// 缺失内容计时的旧记录只显示生成吞吐，不混用另一种分母冒充流式速度。
export const streamSpeed = (request) => {
  const bounds = streamPhaseBounds(request);
  if (!bounds) {
    return { value: null, reason: 'no-timing', title: '缺少流式内容计时，无法计算流式输出速度' };
  }
  const claudeThinking = hasClaudeThinking(request);
  const reasoning = reasoningTokenCount(request);
  const result = { value: null, reasoning, reason: 'unknown', title: '缺少完整的输出用量或计时' };
  if (reasoning > 0 && ms(request.firstTextMs) === null) {
    return { ...result, reason: 'no-text', title: '上游报告了推理 token，但未采集到正文增量时间；工具调用或仅推理响应可能没有正文' };
  }
  const tokens = request.outputTokens - reasoning;
  let start = reasoning > 0 ? bounds.text : bounds.content;
  if (claudeThinking) start = bounds.event;
  const windowMs = bounds.done === null ? null : bounds.done - start;
  let title = reasoning > 0 ? `扣除推理 ${reasoning} tok，从首个正文起算` : '扣除推理 0 tok，从首个内容起算';
  if (claudeThinking) title = 'Claude 不单报思考 token：输出含思考，从首个事件起算';
  if (!Number.isFinite(tokens) || !(tokens > 0) || windowMs === null
    || request.usageKnown === false || UNSETTLED_STATUSES.has(request.status)) return result;
  if (!claudeThinking && ms(request.streamFlowMs) > 0 && request.streamFlowMs * 4 < windowMs) {
    return { ...result, reason: 'burst', title: '内容几乎同时到达，无法计算速度：上游憋住后一次性放出' };
  }
  if (invalidSpeedWindow(tokens, windowMs)) {
    return { ...result, reason: 'window', title: '输出窗口短于100ms或超过10000 tok/s，无法计算速度' };
  }
  if (reasoning > 0 && tokens * (bounds.text - bounds.content) > 20 * reasoning * windowMs) {
    return { ...result, reason: 'held-text', title: '正文相对思考过快，可能被上游缓冲后放出，无法计算速度' };
  }
  return { ...result, value: tokens * 1000 / windowMs, reason: '', title, tokens, windowMs };
};

// 详情和列表悬停共用标签、单位、缺失原因；两项指标始终保持各自的计算口径。
export const requestSpeedMetrics = (request) => {
  if (UNSETTLED_STATUSES.has(request.status)) return [];
  const throughput = generationThroughput(request);
  const metrics = [{
    label: '生成吞吐', value: Number.isFinite(throughput.value) ? `${formatTpsBadge(throughput.value)} tok/s` : '—',
    title: throughput.title, note: throughput.reason === 'window' ? throughput.title : undefined, tone: 'tone-slate'
  }];
  if (request.isStreaming === false || request.requestFamily === 'image') return metrics;
  const speed = streamSpeed(request);
  let value = Number.isFinite(speed.value) ? `${formatTpsBadge(speed.value)} tok/s` : '—';
  if (speed.reason === 'burst') value = '—（集中下发）';
  else if (speed.reason === 'no-text') value = '—（缺少正文计时）';
  else if (speed.reason === 'no-timing') value = '—（缺少流式计时）';
  metrics.push({ label: '流式输出速度', value, title: speed.title,
    tone: speed.reason === 'burst' ? 'tone-amber' : 'tone-slate' });
  return metrics;
};

const STOP_REASONS = {
  end_turn: ['正常结束'], completed: ['正常结束'], tool_use: ['调用工具'],
  stop_sequence: ['停止序列'], pause_turn: ['暂停（服务端工具）'],
  max_tokens: ['达到输出上限', 'warn'], 'incomplete:max_output_tokens': ['达到输出上限', 'warn'],
  refusal: ['拒答', 'danger'], model_context_window_exceeded: ['上下文超限', 'warn'],
  'incomplete:content_filter': ['内容过滤', 'warn']
};

export const stopReasonView = (raw) => {
  const [label, level = 'normal'] = Object.hasOwn(STOP_REASONS, raw) ? STOP_REASONS[raw] : [raw || ''];
  const className = level === 'danger' ? 'text-rose-700 dark:text-rose-300'
    : level === 'warn' ? 'text-amber-700 dark:text-amber-300' : '';
  return { label, level, className, warning: level !== 'normal', raw };
};

export const streamPhaseSegments = (request) => {
  const b = streamPhaseBounds(request);
  if (!b) return null;
  const claudeThinking = hasClaudeThinking(request);
  const thinking = claudeThinking || Boolean(request.reasoningHidden) || ms(request.firstReasoningMs) !== null;
  const hidden = Boolean(request.reasoningHidden) || (claudeThinking && ms(request.firstReasoningMs) === null);
  const thinkingDetail = hidden ? '模型在思考；上游不下发思考内容（不可见推理）' : '模型在思考（可见的思考内容）';
  const start = thinking && ms(request.firstTextMs) !== null ? b.text : b.content;
  const outputMs = b.done === null ? 0 : b.done - start;
  const burst = ms(request.streamFlowMs) > 0 && request.streamFlowMs * 4 < outputMs;
  return [
    { key: 'first', label: '首响', ms: b.event - b.write, detail: '写完请求到上游第一个事件（受理、排队）' },
    { key: thinking ? 'think' : 'await', label: thinking ? '思考' : '等待内容', ms: start - b.event,
      detail: thinking ? thinkingDetail : '没有思考信号：上游开始回应后到第一个内容（预填充等）' },
    { key: 'output', label: '输出', ms: outputMs,
      detail: `从首个正文（无正文时首个内容）到流完成${burst ? '；内容几乎一次性下发（上游憋住后放出）' : ''}` }
  ].filter((segment) => segment.ms > 0);
};

// 首字只用于详情标签和列表悬停；实时内容事件先到时用绝对时刻计算C−W。
export const firstContentDelayMs = (request) => {
  if (request.isStreaming !== true || request.requestFamily === 'image') return null;
  const content = ms(request.firstContentMs);
  const write = ms(request.upstreamWriteMs);
  if (content !== null && write !== null) return Math.max(0, content - write);
  const contentAt = absoluteMs(request.firstContentAt);
  const sentAt = absoluteMs(request.firstTokenStartAt);
  if (contentAt === null || sentAt === null) return null;
  const delay = contentAt - sentAt;
  return Number.isFinite(delay) ? Math.max(0, delay) : null;
};
