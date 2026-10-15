// lifecycle 分段推导单测（方案 F2 测试计划）
import test from 'node:test';
import assert from 'node:assert/strict';
import {
  buildLifecycleSegments,
  countAccountSwitches,
  resolveConnectMs,
  resolveQueueMs,
  resolveRawQueueMs,
  summarizeTerminalReason,
  TAIL_THRESHOLD_MS
} from './lifecycle.js';
import { normalizeRequest } from './normalizeRequest.js';

const base = {
  duration: 10000,
  timestamp: '2026-08-13T00:00:00.000000Z',
  routeDecisionAt: '2026-08-13T00:00:02.000000Z',
  firstTokenMs: 4000,
  completionMs: 5000,
  isStreaming: true,
  status: 'completed'
};

test('全量：UTC 微秒 ISO 绝对时刻差 + 本地处理/连接拆分', () => {
  const request = {
    ...base,
    upstreamWriteMs: 2500 // 本地处理 2000 + 连接 500
  };
  const segments = buildLifecycleSegments(request);
  assert.deepEqual(
    segments.map((seg) => [seg.key, seg.ms]),
    [['queue', 2000], ['connect', 500], ['first', 4000], ['stream', 5000]]
  );
});

test('routeDecisionAt 晚于 upstreamWriteMs：本地处理钳制、连接为 0', () => {
  const request = {
    ...base,
    routeDecisionAt: '2026-08-13T00:00:05.000000Z',
    upstreamWriteMs: 1000
  };
  assert.equal(resolveQueueMs(request), 1000);
  assert.equal(resolveConnectMs(request), 0);
});

test('空串 / 无时区 / 无效 ISO：本地处理与连接降级为 null', () => {
  assert.equal(resolveRawQueueMs({ ...base, routeDecisionAt: '' }), null);
  assert.equal(resolveRawQueueMs({ ...base, routeDecisionAt: '2026-08-13 00:00:01' }), null);
  assert.equal(resolveRawQueueMs({ ...base, routeDecisionAt: 'not-a-date' }), null);
  assert.equal(resolveConnectMs({ ...base, upstreamWriteMs: null }), null);
});

test('降级 2：只有 first/completion（旧数据）→ 前置/首字/流式输出', () => {
  const segments = buildLifecycleSegments({
    duration: 10000,
    firstTokenMs: 3000,
    completionMs: 6000,
    isStreaming: true,
    status: 'completed'
  });
  assert.deepEqual(
    segments.map((seg) => [seg.key, seg.ms]),
    [['pre', 1000], ['first', 3000], ['stream', 6000]]
  );
});

test('降级 3：有 upstreamWriteMs 无 routeDecisionAt → 准备（本地处理+连接）', () => {
  const segments = buildLifecycleSegments({
    duration: 10000,
    upstreamWriteMs: 1500,
    firstTokenMs: 3000,
    completionMs: 5000,
    isStreaming: true,
    status: 'completed'
  });
  assert.deepEqual(segments[0], { key: 'connect', label: '准备（本地处理+连接）', ms: 1500 });
});

test('降级 1：全无 timing → 单段总耗时', () => {
  const segments = buildLifecycleSegments({ duration: 8000, status: 'completed', isStreaming: true });
  assert.deepEqual(segments, [{ key: 'total', label: '总耗时', ms: 8000 }]);
});

test('收尾残差 > 80ms 单独显示，否则并入末段', () => {
  const withTail = buildLifecycleSegments({
    ...base,
    duration: 20000 // 已知段和 11000，残差 9000 > 80
  });
  assert.equal(withTail[withTail.length - 1].key, 'tail');

  const merged = buildLifecycleSegments({
    ...base,
    duration: 11550, // 已知段和 11500，残差 50 <= 80，并入流式段
    upstreamWriteMs: 2500
  });
  const stream = merged.find((seg) => seg.key === 'stream');
  assert.equal(stream.ms, 5050);
  assert.equal(merged.some((seg) => seg.key === 'tail'), false);
  assert.ok(TAIL_THRESHOLD_MS > 50 && TAIL_THRESHOLD_MS < 9000);
});

test('非流式：completion=0 不出流式段，first 段命名「响应」', () => {
  const segments = buildLifecycleSegments({
    duration: 5000,
    firstTokenMs: 5000,
    completionMs: 0,
    isStreaming: false,
    status: 'completed'
  });
  assert.equal(segments.some((seg) => seg.key === 'stream'), false);
  const first = segments.find((seg) => seg.key === 'first');
  assert.equal(first.label, '响应');
});

test('failed/cancelled：已知段照画，末尾补标注段（短标签 + detail 全文）', () => {
  const rawReason = 'rate_limited: endpoint=xuanwulei status=429 content_type=text/event-stream body={"error":{"message":"Service Unavailable","type":"error"},"type":"error"}';
  const failed = buildLifecycleSegments({
    ...base,
    duration: 11000,
    status: 'failed',
    failureReason: rawReason
  });
  const failureSeg = failed[failed.length - 1];
  assert.equal(failureSeg.key, 'failure');
  assert.equal(failureSeg.label, '限流 429');
  assert.equal(failureSeg.detail, rawReason);

  const cancelled = buildLifecycleSegments({
    ...base,
    status: 'cancelled',
    cancelReason: 'client disconnected during streaming'
  });
  const cancelSeg = cancelled[cancelled.length - 1];
  assert.equal(cancelSeg.key, 'cancelled');
  assert.equal(cancelSeg.label, '已取消');
  assert.equal(cancelSeg.detail, 'client disconnected during streaming');
});

test('summarizeTerminalReason：错误码归类 + status 提取', () => {
  assert.equal(
    summarizeTerminalReason('rate_limited: endpoint=x status=429 body={"error":"..."}'),
    '限流 429'
  );
  assert.equal(summarizeTerminalReason('auth_error: token expired'), '鉴权失败');
  assert.equal(summarizeTerminalReason('privacy_blocked: rule=id_card'), '隐私拦截');
  assert.equal(summarizeTerminalReason('connection_timeout: dial tcp'), '超时');
  assert.equal(summarizeTerminalReason('network_error'), '网络错误');
  assert.equal(
    summarizeTerminalReason('upstream_no_available_providers: status=503 no providers'),
    '无可用上游 503'
  );
  assert.equal(summarizeTerminalReason('server_error: status=502'), '服务器错误 502');
  assert.equal(summarizeTerminalReason('upstream_client_error: status=400'), '上游拒绝 400');
  assert.equal(summarizeTerminalReason('incomplete_stream'), '流错误');
  assert.equal(summarizeTerminalReason('rejected_by_failure_tracker: cooldown'), '冷却拦截');
  assert.equal(summarizeTerminalReason('client_cancelled: closed'), '已取消');
});

test('summarizeTerminalReason：upstream 系码不被 stream 规则误吞', () => {
  assert.equal(
    summarizeTerminalReason('image_generation_upstream_error: upstream_status=502 reason=bad_gateway'),
    '上游错误 502'
  );
  assert.equal(
    summarizeTerminalReason('image_api_invalid_upstream_response: content_type=text/html'),
    '上游错误'
  );
  // stream 规则仍覆盖码首与 `_` 后的 stream。
  assert.equal(summarizeTerminalReason('stream_truncated'), '流错误');
  assert.equal(summarizeTerminalReason('account_stream_error: broken pipe'), '流错误');
});

test('summarizeTerminalReason：未识别结构化码原样显示，自由文本与空值回退 null', () => {
  // 结构化未识别码：显示码本身，避免整串全文。
  assert.equal(summarizeTerminalReason('some_new_code: detail here'), 'some_new_code');
  // 整串即码（无冒号无空格）视为结构化。
  assert.equal(summarizeTerminalReason('weird_code_123'), 'weird_code_123');
  // 自由英文句子：首词不可信，返回 null 由调用方回退「失败/已取消」。
  assert.equal(summarizeTerminalReason('something went wrong badly'), null);
  // 回归：自由文本首词即便撞上规则表也必须回退，不能被误归类。
  // endpoint_pipeline.go 的 cancelReason `stream processing cancelled` 曾被错标成「流错误」。
  assert.equal(summarizeTerminalReason('stream processing cancelled'), null);
  assert.equal(summarizeTerminalReason('rate_limit hit while writing'), null);
  assert.equal(summarizeTerminalReason(''), null);
  assert.equal(summarizeTerminalReason(null), null);
  assert.equal(summarizeTerminalReason(undefined), null);
});

test('summarizeTerminalReason：隐私扫描故障与规则命中拦截区分开', () => {
  assert.equal(summarizeTerminalReason('privacy_scan_failed: engine panic'), '隐私扫描失败');
  assert.equal(summarizeTerminalReason('privacy_blocked: rule=id_card'), '隐私拦截');
});

test('后端真实 cancelReason 均落到「已取消」段', () => {
  // 取值来源：endpoint_pipeline.go / account_pipeline.go / lifecycle_manager.go 的 CancelRequest 调用点，
  // 外加 hot_pool.go 直接赋值的 req.CancelReason。
  const reasons = [
    'stream processing cancelled',
    'account stream processing cancelled',
    'client disconnected',
    'context canceled',
    'hot pool shutdown'
  ];
  for (const cancelReason of reasons) {
    const segments = buildLifecycleSegments({ ...base, status: 'cancelled', cancelReason });
    const seg = segments[segments.length - 1];
    assert.equal(seg.key, 'cancelled', `${cancelReason} 段类型应为 cancelled`);
    assert.equal(seg.label, '已取消', `${cancelReason} 应标为「已取消」`);
    assert.equal(seg.detail, cancelReason);
  }
});

// 后端真实 failureReason 全量归类基线。
// 新增后端错误码时同步补这里；若某码归类结果等于码本身，说明规则表漏了它，
// 用户会在分段条上看到裸英文码。
//
// 全量口径 = 所有 failure_reason 写入入口的实参。字面量之外，8 个动态实参各自追到底：
//   bodyErr.Code    → request_body.go 的 requestBodyCode* 常量
//   code            → image_generation.go failImageGenerationRequest 的调用点
//   failureKey      → handler.go handleUnavailableAccountPipeline
//   failureReason   → account_pipeline.go:133/137、streamErr.GetFailureReason()（token_parser.go）
//   block.Reason    → route_state.go 的 RouteBlock：两个字面量 + HasNegativeHit 的 FailureClass，
//                     以及 classifyEndpointRoutable（scheduler.go:628）的四类 return：
//                     endpoint_missing / cooldown / count_tokens_scoped_cooldown /
//                     "negative_cache_" + FailureClass（后缀取值见 endpoint_failure_policy.go
//                     与 handlers/count_tokens.go 的 FailureClass 赋值点，共 4 种）
//   decision.Reason → endpoint_failure_policy.go 的 PassthroughError 决策（3 个）
//   status          → endpoint_pipeline.go:487，默认字面量 "error"
//   PrivacyFailureReason(policyErr) → handlers/privacy.go
const BACKEND_FAILURE_REASONS = {
  // account_pipeline.go / handler.go —— Codex 账号池链路
  account_pool_not_ready: '无可用上游',
  account_pool_load_failed: '无可用上游',
  account_pool_exhausted: '无可用上游',
  account_pool_disabled: '无可用上游',
  account_pool_empty: '无可用上游',
  codex_search_oauth_unavailable: '无可用上游', // 含 "oauth"，须先于 /auth/ 命中
  upstream_no_available_providers: '无可用上游',
  upstream_client_error: '上游拒绝',
  account_pipeline_response_write_error: '读写错误',
  account_response_read_error: '读写错误',
  account_response_write_error: '读写错误',
  account_stream_error: '流错误',
  stream_flusher_unsupported: '流错误',
  empty_response_body: '空响应',
  // request_body.go —— 请求体读取/解压链路（经 handler.go 的 bodyErr.Code）
  request_body_read_failed: '读写错误',
  request_body_too_large: '请求过大',
  decompressed_request_body_too_large: '请求过大',
  zstd_window_too_large: '请求过大',
  invalid_zstd_request_body: '请求无效',
  unsupported_content_encoding: '编码不支持',
  // endpoint_pipeline.go —— Claude 端点管线
  no_endpoints_available: '无可用上游',
  all_endpoints_failed: '无可用上游',
  rejected_by_failure_tracker: '冷却拦截',
  rate_limited: '限流',
  response_read_error: '读写错误',
  response_write_error: '读写错误',
  endpoint_response_write_error: '读写错误',
  error: '失败', // 流式失败兜底 status（endpoint_pipeline.go:487 的字面量默认值）
  // route_state.go —— 手动固定端点的 RouteBlock
  manual_fixed_endpoint_missing: '无可用上游',
  manual_fixed_endpoint_disabled: '无可用上游',
  // scheduler.go classifyEndpointRoutable 的四类 return
  endpoint_missing: '无可用上游',
  cooldown: '冷却拦截',
  count_tokens_scoped_cooldown: '冷却拦截',
  negative_cache_model_unsupported: '模型不支持',
  negative_cache_schema_incompatible: '协议不兼容',
  negative_cache_payload_too_large: '请求过大',
  negative_cache_count_tokens_unsupported: '不支持计数',
  // route_state.go HasNegativeHit → FailureClass（endpoint_capability_mismatch，无 negative_cache_ 前缀）
  model_unsupported: '模型不支持',
  schema_incompatible: '协议不兼容',
  payload_too_large: '请求过大',
  count_tokens_unsupported: '不支持计数',
  client_cancel: '已取消',
  // endpoint_failure_policy.go —— PassthroughError 决策（3 个，server_error 见下方错误分类映射）
  ambiguous_failure_after_wrote_headers: '上游错误',
  empty_response: '空响应',
  // image_generation.go —— 独立图像 API
  method_not_allowed: '方法不支持',
  image_generation_config_error: '配置错误',
  image_generation_invalid_config: '配置错误',
  image_generation_not_configured: '配置错误',
  invalid_request_error: '请求无效',
  privacy_filter_error: '隐私扫描失败', // 引擎故障，非规则命中；须先于 /privacy/ 命中
  image_generation_request_error: '请求构造失败',
  image_generation_transport_error: '网络错误',
  image_generation_timeout: '超时',
  image_generation_response_write_error: '读写错误',
  image_generation_upstream_error: '上游错误',
  image_api_invalid_upstream_response: '上游错误',
  // token_parser.go —— 流完整性判定
  stream_truncated: '流错误',
  incomplete_stream: '流错误',
  // lifecycle_manager.go MapErrorTypeToFailureReason
  server_error: '服务器错误',
  network_error: '网络错误',
  eof_error: '网络错误',
  connection_timeout: '超时',
  response_timeout: '超时',
  timeout: '超时',
  http_error: 'HTTP 错误',
  auth_error: '鉴权失败',
  stream_error: '流错误',
  parsing_error: '解析错误',
  no_healthy: '无可用上游',
  unknown_error: '未知错误',
  client_cancelled: '已取消',
  // handlers/privacy.go
  privacy_blocked: '隐私拦截',
  privacy_scan_failed: '隐私扫描失败'
};

test('后端真实 failureReason 全量归类，无一落到裸码兜底', () => {
  for (const [code, expected] of Object.entries(BACKEND_FAILURE_REASONS)) {
    assert.equal(summarizeTerminalReason(code), expected, `${code} 应归类为「${expected}」`);
    // 带 detail 的完整存储格式（tracker.RecordRequestFinalFailure 的 `code: detail`）同样归类。
    assert.equal(
      summarizeTerminalReason(`${code}: endpoint=x status=503`),
      `${expected} 503`,
      `${code} 带 detail 时应归类为「${expected} 503」`
    );
  }
});

test('规则表顺序约束：靠后的宽规则不得抢走靠前的精确语义', () => {
  // `unsupported` 通配会吞掉 stream_flusher_unsupported。
  assert.equal(summarizeTerminalReason('stream_flusher_unsupported'), '流错误');
  // /auth/ 会吞掉含 oauth 的「无可用账号」码。
  assert.equal(summarizeTerminalReason('codex_search_oauth_unavailable'), '无可用上游');
  assert.equal(summarizeTerminalReason('auth_error'), '鉴权失败');
  // /privacy/ 会吞掉引擎故障码，掩盖系统故障与策略生效的区别。
  assert.equal(summarizeTerminalReason('privacy_filter_error'), '隐私扫描失败');
  assert.equal(summarizeTerminalReason('privacy_blocked'), '隐私拦截');
  // /request_error/ 会吞掉 invalid_request_error。
  assert.equal(summarizeTerminalReason('invalid_request_error'), '请求无效');
  assert.equal(summarizeTerminalReason('image_generation_request_error'), '请求构造失败');
  // /timeout/ 须先于 /connection/。
  assert.equal(summarizeTerminalReason('connection_timeout'), '超时');
  // negative_cache_ 前缀不得掩盖后缀语义：同为 count_tokens 相关，
  // scoped_cooldown 是冷却（可自愈），negative_cache_*_unsupported 是能力不匹配（换端点才行）。
  assert.equal(summarizeTerminalReason('count_tokens_scoped_cooldown'), '冷却拦截');
  assert.equal(summarizeTerminalReason('negative_cache_count_tokens_unsupported'), '不支持计数');
});

test('hot pool 清理的自由文本 failureReason 回退「失败」', () => {
  // hot_pool.go 直接赋值，不走 FailRequest，属自由文本而非结构化码。
  const reason = 'hot pool cleanup: request exceeded max age';
  assert.equal(summarizeTerminalReason(reason), null);
  const segments = buildLifecycleSegments({ ...base, status: 'failed', failureReason: reason });
  const seg = segments[segments.length - 1];
  assert.equal(seg.label, '失败');
  assert.equal(seg.detail, reason);
});

test('换号次数：只试一个账号且流式中途失败不算换号', () => {
  const candidates = [
    { account_id: 1, runtime_outcome: 'transient_failure' },
    { account_id: 2 },
    { account_id: 3 }
  ];
  assert.equal(countAccountSwitches(candidates), 0);
});

test('换号次数：失败后换到下一个账号成功算 1 次', () => {
  const candidates = [
    { account_id: 1, runtime_outcome: 'transient_failure' },
    { account_id: 2, runtimeOutcome: 'success' },
    { account_id: 3 }
  ];
  assert.equal(countAccountSwitches(candidates), 1);
});

test('换号次数：三个账号全部失败算 2 次，空候选为 0', () => {
  const candidates = [
    { account_id: 1, runtime_outcome: 'auth_failed' },
    { account_id: 2, runtime_outcome: 'transient_failure' },
    { account_id: 3, runtime_outcome: 'transient_failure' }
  ];
  assert.equal(countAccountSwitches(candidates), 2);
  assert.equal(countAccountSwitches([]), 0);
  assert.equal(countAccountSwitches(), 0);
});

test('换号次数：换到下一个账号后客户端取消也算 1 次', () => {
  const candidates = [
    { account_id: 1, runtime_outcome: 'transient_failure' },
    { account_id: 2, runtime_outcome: 'cancelled' },
    { account_id: 3 }
  ];
  assert.equal(countAccountSwitches(candidates), 1);
});

const timedRequest = {
  duration: 10000,
  upstreamDispatchMs: 1000,
  upstreamConnMs: 1500,
  upstreamWriteMs: 2500,
  firstTokenMs: 3000,
  completionMs: 4500,
  isStreaming: true,
  status: 'completed'
};

test('新数据：本地处理、建连、发送、首字、流式输出首尾相接', () => {
  const segments = buildLifecycleSegments(timedRequest);
  assert.deepEqual(segments.map(({ key, label, ms }) => [key, label, ms]), [
    ['queue', '本地处理', 1000], ['connect', '建连', 500], ['send', '发送', 1000],
    ['first', '首字', 3000], ['stream', '流式输出', 4500]
  ]);
  assert.match(segments[0].detail, /选路.*出站限速.*此前失败/);
  assert.doesNotMatch(segments[0].detail, /压缩|鉴权/);
  assert.match(segments[1].detail, /DNS、TCP、TLS/);
  assert.doesNotMatch(segments[1].detail, /本地处理/);
  assert.match(segments[2].detail, /请求体越大/);
});

test('账号池本地处理：说明含鉴权与按账号压缩，不含端点出站限速', () => {
  const [local] = buildLifecycleSegments({ ...timedRequest, upstreamType: 'account', requestFamily: 'codex' });
  assert.match(local.detail, /选路.*鉴权准备.*压缩.*此前失败/);
  assert.doesNotMatch(local.detail, /出站限速/);
});

test('新数据直通：订阅直通与图像接口同样显示本地处理，说明不含选路与重试', () => {
  for (const request of [
    { ...timedRequest, upstreamType: 'subscription', requestFamily: 'claude' },
    { ...timedRequest, upstreamType: 'subscription', requestFamily: 'codex' },
    { ...timedRequest, requestFamily: 'image' }
  ]) {
    const [local] = buildLifecycleSegments(request);
    assert.deepEqual([local.key, local.label, local.ms], ['queue', '本地处理', 1000]);
    assert.match(local.detail, /隐私扫描/);
    assert.doesNotMatch(local.detail, /选路|失败/);
    assert.equal(/重新压缩.*脱敏改写/.test(local.detail), request.requestFamily === 'codex', 'Codex 订阅只在正文被改写时重新压缩请求体');
  }
});

test('图像详情：snake_case 权威行经规范化并合并计时后选用直通说明', () => {
  // 与 RequestDetailPane 的合并方式一致：规范化后的权威行 + 详情接口单独返回的计时偏移。
  const request = {
    ...normalizeRequest({ request_id: 'req-image', request_family: 'image', upstream_type: 'endpoint', status: 'completed', is_streaming: false }),
    upstreamDispatchMs: 100, upstreamConnMs: 300, upstreamWriteMs: 600
  };
  const [local] = buildLifecycleSegments(request);
  assert.deepEqual([local.key, local.label, local.ms], ['queue', '本地处理', 100]);
  assert.equal(local.detail, buildLifecycleSegments({ ...timedRequest, upstreamType: 'subscription' })[0].detail);
});

test('早期直通记录：无 dispatch 时忽略 routeDecisionAt，本地处理并入建连', () => {
  const segments = buildLifecycleSegments({ ...timedRequest, ...base, upstreamDispatchMs: null, upstreamConnMs: 1500, upstreamWriteMs: 2500, duration: 11500 });
  assert.equal(segments.some(({ key }) => key === 'queue'), false);
  assert.deepEqual(segments.slice(0, 2).map(({ key, ms }) => [key, ms]), [['connect', 1500], ['send', 1000]]);
  assert.match(segments[0].detail, /未单独计本地处理/);
});

test('本地处理真实 0ms：不显示该段，建连不误标为并入本地处理', () => {
  const segments = buildLifecycleSegments({ ...timedRequest, upstreamDispatchMs: 0, upstreamConnMs: 500, upstreamWriteMs: 1500, duration: 9000 });
  assert.equal(segments.some(({ key }) => key === 'queue'), false);
  const connect = segments.find(({ key }) => key === 'connect');
  assert.equal(connect.ms, 500);
  assert.doesNotMatch(connect.detail, /未单独计本地处理/);
});

test('复用连接：conn 等于 dispatch 时不显示建连，0ms 本地处理与发送同样不显示', () => {
  const segments = buildLifecycleSegments({ ...timedRequest, upstreamConnMs: 1000 });
  assert.equal(segments.some(({ key }) => key === 'connect'), false);
  assert.equal(segments.find(({ key }) => key === 'send').ms, 1500);
  const zero = buildLifecycleSegments({ ...timedRequest, upstreamDispatchMs: 0, upstreamConnMs: 0, upstreamWriteMs: 0, duration: 7500 });
  assert.deepEqual(zero.map(({ key }) => key), ['first', 'stream']);
});

test('新数据非单调输入：向写完时刻钳制，段无负差、无重叠', () => {
  for (const [dispatch, conn, write, expected] of [
    [3000, 2000, 1000, [['queue', 1000]]],
    [2000, 500, 1000, [['queue', 500], ['send', 500]]],
    [-100, -50, 1000, [['send', 1000]]]
  ]) {
    const segments = buildLifecycleSegments({ ...timedRequest, upstreamDispatchMs: dispatch, upstreamConnMs: conn, upstreamWriteMs: write, duration: write + 7500 });
    assert.deepEqual(segments.filter(({ key }) => ['queue', 'connect', 'send'].includes(key)).map(({ key, ms }) => [key, ms]), expected);
    assert.equal(segments.reduce((sum, { ms }) => sum + ms, 0), write + 7500);
    assert.ok(segments.every(({ ms }) => ms >= 0));
  }
});

test('缺 conn 或 write：完整保留旧数据降级规则，不使用新 dispatch', () => {
  for (const request of [
    { ...base, upstreamWriteMs: 2500 },
    { ...base },
    { duration: 8000, status: 'completed' },
    { ...base, status: 'failed', failureReason: 'server_error' }
  ]) {
    const expected = buildLifecycleSegments(request);
    assert.deepEqual(buildLifecycleSegments({ ...request, upstreamConnMs: null, upstreamDispatchMs: 9000 }), expected);
    if (request.upstreamWriteMs === undefined) {
      assert.deepEqual(buildLifecycleSegments({ ...request, upstreamConnMs: 9000, upstreamDispatchMs: 9000 }), expected);
    }
  }
});

test('订阅无 conn（旧直通记录与 WS 提交）：显示准备与发送，其他旧标签保持', () => {
  const request = { ...timedRequest, upstreamConnMs: null, upstreamDispatchMs: null, upstreamType: 'subscription' };
  assert.deepEqual(buildLifecycleSegments(request)[0], { key: 'connect', label: '准备与发送', ms: 2500 });
  assert.equal(buildLifecycleSegments({ ...request, upstreamType: 'endpoint' })[0].label, '准备（本地处理+连接）');
  assert.equal(buildLifecycleSegments({ ...request, ...base, upstreamWriteMs: 2500 })[1].label, '连接');
});

test('新数据落定：成功、小残差、大收尾、失败、取消各段之和等于总耗时', () => {
  for (const request of [
    timedRequest,
    { ...timedRequest, duration: 10050 },
    { ...timedRequest, duration: 10500 },
    { ...timedRequest, duration: 10500, status: 'failed', failureReason: 'server_error' },
    { ...timedRequest, duration: 10500, status: 'cancelled', cancelReason: 'client disconnected' },
    { ...timedRequest, firstTokenMs: null, completionMs: null, duration: 5000, status: 'failed', failureReason: 'server_error' }
  ]) {
    const segments = buildLifecycleSegments(request);
    assert.equal(segments.reduce((sum, { ms }) => sum + ms, 0), request.duration);
  }
  assert.equal(buildLifecycleSegments({ ...timedRequest, duration: 10050 }).at(-1).ms, 4550);
  assert.deepEqual(buildLifecycleSegments({ ...timedRequest, duration: 10500 }).at(-1), { key: 'tail', label: '收尾', ms: 500 });
});
