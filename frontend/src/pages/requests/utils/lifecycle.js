// ============================================
// lifecycle - 生命周期分段推导纯函数（方案 F2）
// 2026-08-13
// 输入：normalizeRequest 后的权威行数据（effective）。
// 时间契约：timestamp / routeDecisionAt 均为固定微秒精度 UTC ISO（…Z）绝对时刻，
// 直接相减即真实毫秒差；解析失败降级为 null，不制造假段。
// ============================================

import { parseTimestamp } from '../../../utils/timezone.js';
import { getRequestPresentation } from './requestPresentation.js';

// 收尾残差超过该阈值才单独显示「收尾」微段，否则并入末段。
export const TAIL_THRESHOLD_MS = 80;

const toNullableMs = (value) => {
  if (value === null || value === undefined) return null;
  const ms = Number(value);
  return Number.isFinite(ms) ? Math.max(ms, 0) : null;
};

// parseTimestampOrNull 复用严格绝对时刻解析；失败返回 null。
export const parseTimestampOrNull = (value) => {
  try {
    return parseTimestamp(value);
  } catch {
    return null;
  }
};

// resolveRawQueueMs 排队段原始值：routeDecisionAt - startTime（毫秒）。
export const resolveRawQueueMs = (request = {}) => {
  const routeAt = parseTimestampOrNull(request.routeDecisionAt);
  const startAt = parseTimestampOrNull(request.timestamp);
  if (!routeAt || !startAt) return null;
  return Math.max(0, routeAt.getTime() - startAt.getTime());
};

// resolveQueueMs 排队段：与 upstreamWriteMs 同时存在时钳制到上游写偏移，禁止重叠。
export const resolveQueueMs = (request = {}) => {
  const rawQueueMs = resolveRawQueueMs(request);
  const upstreamWriteMs = toNullableMs(request.upstreamWriteMs);
  if (rawQueueMs === null) return null;
  if (upstreamWriteMs !== null) return Math.min(rawQueueMs, upstreamWriteMs);
  return rawQueueMs;
};

// resolveConnectMs 连接段：upstreamWriteMs - 排队段。
export const resolveConnectMs = (request = {}) => {
  const upstreamWriteMs = toNullableMs(request.upstreamWriteMs);
  if (upstreamWriteMs === null) return null;
  const queueMs = resolveQueueMs(request);
  return Math.max(0, upstreamWriteMs - (queueMs ?? 0));
};

// 无可用上游类终态的统一标签。requestSource.js 的空上游归属判定复用同一套规则
// （经 isNoUpstreamReason），避免列表上游列与生命周期分段条出现两套分类。
export const NO_UPSTREAM_LABEL = '无可用上游';

// 终态短标签归类规则（按优先级匹配错误码）。顺序约束逐条注释，改动前先跑
// lifecycle.test.js 的「后端真实 failureReason 全量归类」基线。
const REASON_LABEL_RULES = [
  [/rate_limit/, '限流'],
  // oauth_unavailable 须排在 auth 前：codex_search_oauth_unavailable 是「没有可用的 OAuth 账号」，
  // 被 /auth/ 吞成「鉴权失败」会把排查引向凭据问题。
  [/oauth_unavailable/, NO_UPSTREAM_LABEL],
  [/auth/, '鉴权失败'],
  // privacy_scan / privacy_filter_error 是扫描引擎自身故障，须排在 privacy 前，
  // 与 privacy_blocked 的规则命中拦截区分开。
  [/privacy_scan|privacy_filter_error/, '隐私扫描失败'],
  [/privacy/, '隐私拦截'],
  // timeout 须排在 connection 前，保证 connection_timeout 归入「超时」。
  [/timeout/, '超时'],
  [/network|eof|transport_error/, '网络错误'],
  [/no_healthy|no_endpoints|no_available_providers|pool_exhausted|pool_not_ready|pool_load_failed|pool_disabled|pool_empty|all_endpoints_failed|endpoint_missing|endpoint_disabled/, NO_UPSTREAM_LABEL],
  // 端点负缓存命中（RouteBlock endpoint_capability_mismatch 的 FailureClass）。
  // 这三条须写全码而非 `unsupported` 通配，否则会抢在 stream 规则前吞掉 stream_flusher_unsupported。
  [/model_unsupported/, '模型不支持'],
  [/schema_incompatible/, '协议不兼容'],
  [/count_tokens_unsupported/, '不支持计数'],
  [/payload_too_large|body_too_large|window_too_large/, '请求过大'],
  [/unsupported_content_encoding/, '编码不支持'],
  [/method_not_allowed/, '方法不支持'],
  [/config_error|invalid_config|not_configured/, '配置错误'],
  // invalid_request 须排在 request_error 前，否则 invalid_request_error 会被后者吞成「请求构造失败」。
  [/invalid_zstd|invalid_request/, '请求无效'],
  [/server_error/, '服务器错误'],
  [/client_error/, '上游拒绝'],
  [/invalid_upstream|upstream_error|ambiguous_failure/, '上游错误'],
  [/request_error/, '请求构造失败'],
  [/write_error|read_error|read_failed/, '读写错误'],
  [/parsing/, '解析错误'],
  [/empty_response/, '空响应'],
  // stream 只匹配码首或 `_` 后，避免误吞 upstream 系码。
  [/(?:^|_)stream/, '流错误'],
  [/http_error/, 'HTTP 错误'],
  [/unknown/, '未知错误'],
  [/connection/, '网络错误'],
  [/rejected|cooldown/, '冷却拦截'],
  [/cancel|disconnect/, '已取消'],
  // endpoint_pipeline.go 流式失败的兜底 status 字面量，无附加语义，等同自由文本回退。
  [/^error$/, '失败']
];

// resolveTerminalReasonLabel 解析终态码并归类，返回不带 status 后缀的纯标签。
// 存储格式（tracker.RecordRequestFinalFailure）：`code: detail`。只对 code 做规则匹配，
// detail 文本不参与，避免 `rate_limited: endpoint=x status=429` 被 detail 里的词误归类。
// 归类失败时返回码本身；自由文本（首词后跟空格）首词不可信，返回 null 由调用方回退。
const resolveTerminalReasonLabel = (reason) => {
  const text = String(reason || '').trim();
  if (!text) return null;
  const codeMatch = text.match(/^([a-z][a-z0-9_]*)(:|\s|$)/i);
  if (!codeMatch) return null;
  // 自由文本首词不可信，须先回退，否则会撞规则表被误归类：
  // 例如 cancelReason `stream processing cancelled` 首词命中 stream 规则会错标成「流错误」。
  const isStructuredCode = codeMatch[2] === ':' || codeMatch[2] === '';
  if (!isStructuredCode) return null;
  const code = codeMatch[1].toLowerCase();
  const rule = REASON_LABEL_RULES.find(([pattern]) => pattern.test(code));
  return rule ? rule[1] : code;
};

// isNoUpstreamReason 判定终态码是否属于「没有上游可用」类。
// 供上游归属列在 upstream_name 为空时决定显示语义，与分段条标签共用规则表。
export const isNoUpstreamReason = (reason) => resolveTerminalReasonLabel(reason) === NO_UPSTREAM_LABEL;

// summarizeTerminalReason 从原始终态串提取短标签。
// detail 常含 `status=NNN`，命中时作为后缀补在标签后。
export const summarizeTerminalReason = (reason) => {
  const label = resolveTerminalReasonLabel(reason);
  if (label === null) return null;
  const statusMatch = String(reason).match(/\b(?:upstream_)?status=(\d{3})\b/);
  return `${label}${statusMatch ? ` ${statusMatch[1]}` : ''}`;
};

// terminalSegment 终态标注段：failed/cancelled 时返回，否则 null。
// label 只放短标签，原始全文经 detail 供 tooltip 展示（完整错误另见详情页错误卡片）。
const terminalSegment = (request = {}, ms = 0) => {
  if (getRequestPresentation(request).isInterrupted) {
    return { key: 'cancelled', label: '已中断', ms, detail: '客户端连接断开，未收到完整结束事件' };
  }
  const status = String(request.status || '').toLowerCase();
  if (status === 'failed' || status === 'error') {
    const detail = request.failureReason || '';
    return { key: 'failure', label: summarizeTerminalReason(detail) || '失败', ms, detail };
  }
  if (status === 'cancelled') {
    const detail = request.cancelReason || '';
    return { key: 'cancelled', label: summarizeTerminalReason(detail) || '已取消', ms, detail };
  }
  return null;
};

// buildLifecycleSegments 分段推导（毫秒，全部防御 NULL 与非单调输入）。
// 降级规则（自旧向新）：
//   1. 全无 timing → 单段「总耗时」
//   2. 只有 first/completion（旧数据）→ 前置 / 首字 / 流式输出
//   3. 有 upstreamWriteMs 无 routeDecisionAt → 准备（排队+连接）/ 首字 / 流式输出
//   4. 全量 → 排队 · 连接 · 首字 · 流式输出（· 收尾）
//   5. failed/cancelled → 已知段照画，末尾补红/灰标注段
export const buildLifecycleSegments = (request = {}) => {
  const duration = toNullableMs(request.duration) ?? 0;
  const firstMs = toNullableMs(request.firstTokenMs);
  const completionMs = toNullableMs(request.completionMs);
  const isStreaming = request.isStreaming !== false;
  const queueMs = resolveQueueMs(request);
  const connectMs = resolveConnectMs(request);

  const hasTiming = firstMs !== null || completionMs !== null || queueMs !== null || connectMs !== null;

  // 降级规则 1：全无 timing。
  if (!hasTiming) {
    const terminal = terminalSegment(request, duration);
    return terminal ? [terminal] : [{ key: 'total', label: '总耗时', ms: duration }];
  }

  const segments = [];

  if (queueMs !== null && queueMs > 0) {
    segments.push({ key: 'queue', label: '排队', ms: queueMs });
  }
  if (connectMs !== null && connectMs > 0) {
    // 降级规则 3：无 routeDecisionAt 时排队与连接不可分，合并为「准备」段。
    segments.push(queueMs === null
      ? { key: 'connect', label: '准备（排队+连接）', ms: connectMs }
      : { key: 'connect', label: '连接', ms: connectMs });
  }
  // 降级规则 2：无排队/连接信息时，用总耗时扣除首字与流式推算「前置」。
  if (queueMs === null && connectMs === null && firstMs !== null) {
    const preMs = Math.max(0, duration - ((firstMs ?? 0) + (completionMs ?? 0)));
    if (preMs > 0) {
      segments.push({ key: 'pre', label: '前置', ms: preMs });
    }
  }

  if (firstMs !== null) {
    segments.push({ key: 'first', label: isStreaming ? '首字' : '响应', ms: firstMs });
  }
  // 非流式 completionMs=0，不出流式段。
  if (isStreaming && completionMs !== null && completionMs > 0) {
    segments.push({ key: 'stream', label: '流式输出', ms: completionMs });
  }

  const knownSum = segments.reduce((sum, seg) => sum + seg.ms, 0);

  // 降级规则 5：终态标注段吸收残差。
  const terminal = terminalSegment(request, 0);
  if (terminal) {
    terminal.ms = Math.max(0, duration - knownSum);
    segments.push(terminal);
    return segments;
  }

  // 收尾残差：> 80ms 单独显示，否则并入末段。
  const tailMs = Math.max(0, duration - knownSum);
  if (tailMs > TAIL_THRESHOLD_MS) {
    segments.push({ key: 'tail', label: '收尾', ms: tailMs });
  } else if (tailMs > 0 && segments.length > 0) {
    segments[segments.length - 1].ms += tailMs;
  }

  return segments;
};

// lifecycleSegmentColors 分段配色（实心色块，深浅主题各给一套）。
// 不走 tone-*：色块只需要底色，且暗色下要压深到能承载浅色标签文字。
export const lifecycleSegmentColors = {
  queue: 'bg-slate-200 dark:bg-slate-600',
  connect: 'bg-indigo-200 dark:bg-indigo-800',
  pre: 'bg-slate-200 dark:bg-slate-600',
  first: 'bg-violet-300 dark:bg-violet-700',
  stream: 'bg-emerald-300 dark:bg-emerald-700',
  tail: 'bg-slate-200 dark:bg-slate-600',
  total: 'bg-slate-200 dark:bg-slate-600',
  failure: 'bg-rose-300 dark:bg-rose-800',
  cancelled: 'bg-slate-300 dark:bg-slate-500'
};

// railSegmentColors 同一套分段语义的「轨道版」，供列表里 3px 高的 TraceRail 使用。
// 与上面那套刻意分叉，不要合并：
//   - 详情条是 26px 色块且要承载浅色标签文字，暗色下必须压深（violet-700 起）；
//   - 轨道只有 3px、不放文字，需要的是亮度 —— 同样的 violet-700 画在 3px 上
//     会直接读成「这段没渲染出来」。
// first / stream / failure 双主题同值：violet-400 / emerald-400 / rose-400 在
// 两个背景上都够亮，再分叉只会让两个主题的比例观感对不上。
export const railSegmentColors = {
  queue: 'bg-slate-300 dark:bg-slate-500',
  connect: 'bg-indigo-300 dark:bg-indigo-400',
  pre: 'bg-slate-300 dark:bg-slate-500',
  first: 'bg-violet-400',
  stream: 'bg-emerald-400',
  tail: 'bg-slate-300 dark:bg-slate-500',
  total: 'bg-slate-300 dark:bg-slate-500',
  failure: 'bg-rose-400',
  cancelled: 'bg-slate-300 dark:bg-slate-500'
};

// countAccountSwitches 账号池请求的换号次数 = 实际尝试过的账号数 - 1。
// 每次尝试都会给对应候选写 runtime_outcome（成功也写），所以「有 outcome 的候选」即已尝试账号。
// 不能只数失败候选：流式中途出错时唯一的账号也是失败，那并不是换号。
export const countAccountSwitches = (candidates = []) => {
  const attempted = candidates.filter((candidate) => candidate?.runtime_outcome || candidate?.runtimeOutcome).length;
  return Math.max(attempted - 1, 0);
};
