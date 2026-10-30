// ============================================
// lifecycle - 生命周期分段推导纯函数（方案 F2）
// 2026-08-13
// 输入：normalizeRequest 后的权威行数据（effective）。
// 新数据额外记录 upstreamFirstDispatchMs：本地处理 → 重试 → 最终尝试；未写完按规则6降级。
// 时间契约：timestamp / routeDecisionAt 均为固定微秒精度 UTC ISO（…Z）绝对时刻，
// 直接相减即真实毫秒差；解析失败降级为 null，不制造假段。
// ============================================

import { parseTimestamp } from '../../../utils/timezone.js';
import { getRequestPresentation } from './requestPresentation.js';
import { streamPhaseSegments, streamPhaseBounds } from './streamPhases.js';

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

// resolveRawQueueMs 旧数据本地处理段原始值：routeDecisionAt - startTime（毫秒）。
export const resolveRawQueueMs = (request = {}) => {
  const routeAt = parseTimestampOrNull(request.routeDecisionAt);
  const startAt = parseTimestampOrNull(request.timestamp);
  if (!routeAt || !startAt) return null;
  return Math.max(0, routeAt.getTime() - startAt.getTime());
};

// resolveQueueMs 旧数据本地处理段：与 upstreamWriteMs 同时存在时钳制到上游写偏移，禁止重叠。
export const resolveQueueMs = (request = {}) => {
  const rawQueueMs = resolveRawQueueMs(request);
  const upstreamWriteMs = toNullableMs(request.upstreamWriteMs);
  if (rawQueueMs === null) return null;
  if (upstreamWriteMs !== null) return Math.min(rawQueueMs, upstreamWriteMs);
  return rawQueueMs;
};

// resolveConnectMs 旧数据连接段：upstreamWriteMs - 本地处理段。
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

// 本地处理段的悬停说明按链路区分：出站限速与 429 重试等待只在端点管线，鉴权刷新与按账号压缩只在账号池；
// 直通链路（订阅直通、图像接口）不经调度，没有选路与失败重试。
const LOCAL_SEGMENT_DETAIL = {
  endpoint: '读取与解析请求、选路、模型改写、隐私扫描与构造请求；另含出站限速等待、此前失败的尝试及其重试等待（如有）',
  account: '读取与解析请求、选路、模型改写、隐私扫描、鉴权准备（可能刷新 OAuth token）与请求体压缩（按账号配置）；另含此前失败的尝试（如有）',
  codexSubscription: '读取与解析请求、隐私扫描、请求体重新压缩（客户端发 zstd 且正文被脱敏改写时）与构造上游请求',
  direct: '读取与解析请求、隐私扫描与构造上游请求'
};

const NEW_LOCAL_SEGMENT_DETAIL = {
  ...LOCAL_SEGMENT_DETAIL,
  endpoint: '读取与解析请求、选路、模型改写、隐私扫描与构造请求；另含出站限速等待（如有）',
  account: '读取与解析请求、选路、模型改写、隐私扫描、鉴权准备（可能刷新 OAuth token）与请求体压缩（按账号配置）'
};

const resolveLocalSegmentDetail = (request) => {
  const detail = toNullableMs(request.upstreamFirstDispatchMs) === null ? LOCAL_SEGMENT_DETAIL : NEW_LOCAL_SEGMENT_DETAIL;
  if (request.upstreamType === 'subscription') {
    return request.requestFamily === 'codex' ? detail.codexSubscription : detail.direct;
  }
  if (request.requestFamily === 'image') return detail.direct;
  return request.upstreamType === 'account' ? detail.account : detail.endpoint;
};

// 分段边界：新数据以首次取连接结束本地处理，最终取连接结束重试段。
// 旧数据缺 first，保持原有五项降级规则；规则6：有 first、未写完时只画本地处理与终态残差。
const resolvePrefixTiming = (request) => {
  const writeMs = toNullableMs(request.upstreamWriteMs);
  const connMs = toNullableMs(request.upstreamConnMs);
  const firstDispatchMs = toNullableMs(request.upstreamFirstDispatchMs);
  const dispatchMs = toNullableMs(request.upstreamDispatchMs);
  const hasConnectionTiming = connMs !== null && writeMs !== null;
  const readyMs = hasConnectionTiming ? Math.min(connMs, writeMs) : null;
  const boundaryMs = hasConnectionTiming && dispatchMs !== null ? Math.min(dispatchMs, readyMs) : null;
  const queueMs = firstDispatchMs !== null
    ? Math.min(firstDispatchMs, boundaryMs ?? writeMs ?? firstDispatchMs)
    : (hasConnectionTiming ? boundaryMs : resolveQueueMs(request));
  const connectMs = hasConnectionTiming
    ? Math.max(0, readyMs - (boundaryMs ?? queueMs ?? 0))
    : (firstDispatchMs === null ? resolveConnectMs(request) : (writeMs === null ? null : Math.max(0, writeMs - queueMs)));
  const retryMs = firstDispatchMs !== null && boundaryMs !== null ? Math.max(0, boundaryMs - queueMs) : 0;
  return { writeMs, readyMs, queueMs, connectMs, retryMs, hasConnectionTiming, firstDispatchMs };
};

const connectionSegment = (request, { hasConnectionTiming, connectMs, queueMs }) => {
  if (hasConnectionTiming) {
    return { key: 'connect', label: '建连', ms: connectMs, detail: `取得上游连接：新建连接含 DNS、TCP、TLS 与代理握手，复用连接接近 0${queueMs === null ? '；本条记录未单独计本地处理，已并入此段' : ''}` };
  }
  if (queueMs !== null) return { key: 'connect', label: '连接', ms: connectMs };
  return { key: 'connect', label: request.upstreamType === 'subscription' ? '准备与发送' : '准备（本地处理+连接）', ms: connectMs };
};

const buildPrefixSegments = (request, timing) => {
  const { writeMs, readyMs, queueMs, connectMs, retryMs, hasConnectionTiming, firstDispatchMs } = timing;
  const segments = [];
  if (queueMs !== null && queueMs > 0) {
    const local = { key: 'queue', label: '本地处理', ms: queueMs };
    if (hasConnectionTiming || firstDispatchMs !== null) local.detail = resolveLocalSegmentDetail(request);
    segments.push(local);
  }
  if (retryMs > 0) {
    segments.push({ key: 'retry', label: '重试', ms: retryMs,
      detail: `从第一次联系上游到最终那次：此前失败的尝试、重试前的等待（429 等待、出站限速），以及后续尝试的本地准备。共联系上游 ${(request.retryCount ?? 0) + 1} 次` });
  }
  if (connectMs !== null && connectMs > 0) segments.push(connectionSegment(request, timing));
  if (hasConnectionTiming && writeMs > readyMs) {
    segments.push({ key: 'send', label: '发送', ms: writeMs - readyMs, detail: '写出请求头与请求体；请求体越大、上行越慢，这段越长' });
  }
  return segments;
};

const finishLifecycleSegments = (request, segments, duration) => {
  const knownSum = segments.reduce((sum, seg) => sum + seg.ms, 0);
  const terminal = terminalSegment(request, 0);
  if (terminal) {
    terminal.ms = Math.max(0, duration - knownSum);
    return [...segments, terminal];
  }
  const tailMs = Math.max(0, duration - knownSum);
  if (tailMs > TAIL_THRESHOLD_MS) {
    segments.push({ key: 'tail', label: '收尾', ms: tailMs });
  } else if (tailMs > 0 && segments.length > 0) {
    segments[segments.length - 1].ms += tailMs;
  }
  return segments;
};

// buildLifecycleSegments 新数据拆出重试；旧数据继续按原有五项规则推导，不回填或猜测。
export const buildLifecycleSegments = (request = {}, { streamPhases = true } = {}) => {
  const duration = toNullableMs(request.duration) ?? 0;
  const firstMs = toNullableMs(request.firstTokenMs);
  const completionMs = toNullableMs(request.completionMs);
  const isStreaming = request.isStreaming !== false;
  const bounds = streamPhases ? streamPhaseBounds(request) : null;
  const timing = resolvePrefixTiming(bounds ? {
    ...request, upstreamWriteMs: bounds.write,
    upstreamConnMs: toNullableMs(request.upstreamConnMs) === null ? null : Math.min(request.upstreamConnMs, bounds.write),
    upstreamDispatchMs: toNullableMs(request.upstreamDispatchMs) === null ? null : Math.min(request.upstreamDispatchMs, bounds.write),
    upstreamFirstDispatchMs: toNullableMs(request.upstreamFirstDispatchMs) === null ? null : Math.min(request.upstreamFirstDispatchMs, bounds.write)
  } : request);
  const { queueMs, connectMs } = timing;
  const hasTiming = firstMs !== null || completionMs !== null || queueMs !== null || connectMs !== null;
  if (!hasTiming) {
    const terminal = terminalSegment(request, duration);
    return terminal ? [terminal] : [{ key: 'total', label: '总耗时', ms: duration }];
  }
  const segments = buildPrefixSegments(request, timing);
  const phases = streamPhases ? streamPhaseSegments(request) : null;
  if (phases) return finishLifecycleSegments(request, [...segments, ...phases], duration);
  // 旧数据降级规则2：首响与生成之外的残差为前置。
  if (queueMs === null && connectMs === null && firstMs !== null) {
    const preMs = Math.max(0, duration - (firstMs + (completionMs ?? 0)));
    if (preMs > 0) segments.push({ key: 'pre', label: '前置', ms: preMs });
  }
  if (firstMs !== null) segments.push({ key: 'first', label: isStreaming ? '首响' : '响应', ms: firstMs });
  if (isStreaming && completionMs !== null && completionMs > 0) {
    segments.push({ key: 'stream', label: '流式输出', ms: completionMs });
  }
  return finishLifecycleSegments(request, segments, duration);
};

// 列表轨道沿用既有分段，内容阶段只在详情条展示。
export const buildRailLifecycleSegments = (request) => buildLifecycleSegments(request, { streamPhases: false });

// lifecycleSegmentColors 分段配色（实心色块，深浅主题各给一套）。
// 不走 tone-*：色块只需要底色，且暗色下要压深到能承载浅色标签文字。
export const lifecycleSegmentColors = {
  think: 'bg-fuchsia-200 dark:bg-fuchsia-800',
  await: 'bg-slate-200 dark:bg-slate-600',
  output: 'bg-emerald-300 dark:bg-emerald-700',
  queue: 'bg-slate-200 dark:bg-slate-600',
  retry: 'bg-amber-200 dark:bg-amber-800',
  connect: 'bg-indigo-200 dark:bg-indigo-800',
  send: 'bg-sky-200 dark:bg-sky-800',
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
  think: 'bg-fuchsia-300 dark:bg-fuchsia-400',
  await: 'bg-slate-300 dark:bg-slate-500',
  output: 'bg-emerald-400',
  queue: 'bg-slate-300 dark:bg-slate-500',
  retry: 'bg-amber-300 dark:bg-amber-400',
  connect: 'bg-indigo-300 dark:bg-indigo-400',
  send: 'bg-sky-300 dark:bg-sky-400',
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
