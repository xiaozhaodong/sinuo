import test from 'node:test';
import assert from 'node:assert/strict';
import { createRequire } from 'node:module';
import { fileURLToPath, pathToFileURL } from 'node:url';
import { build } from 'esbuild';
import { createElement } from 'react';
import { renderToStaticMarkup } from 'react-dom/server';

// 只在内存编译 JSX，复用同一个 React 实例；不启动开发服务器或写出产物。
const require = createRequire(import.meta.url);
const compiled = await build({
  entryPoints: [fileURLToPath(new URL('./LifecyclePanel.jsx', import.meta.url))],
  bundle: true,
  write: false,
  platform: 'node',
  format: 'esm',
  jsx: 'automatic',
  alias: { '@components': fileURLToPath(new URL('../../../components', import.meta.url)) },
  plugins: [{
    name: 'shared-react',
    setup(builder) {
      builder.onResolve({ filter: /^(react(?:\/.*)?|lucide-react)$/ }, ({ path }) => ({
        path: pathToFileURL(require.resolve(path)).href,
        external: true
      }));
    }
  }]
});
const { default: LifecyclePanel } = await import(`data:text/javascript;base64,${Buffer.from(compiled.outputFiles[0].text).toString('base64')}`);
const render = (request) => renderToStaticMarkup(createElement(LifecyclePanel, { request }));

const renderFlow = (flow, status = 'completed') => renderToStaticMarkup(createElement(LifecyclePanel, {
  request: { requestId: 'test', status }, lifecycle: { flow }
}));
const textOf = (markup) => markup.replace(/<[^>]*>/g, '');

test('单次 WS 请求展示上游和交付节点，分别展开可见毫秒级时间点', () => {
  const html = renderFlow({
    transport: 'ws', legs: [{ leg_seq: 1, target_snapshot: { upstream_name: 'chatgpt-subscription' } }],
    contacts: [{
      leg_seq: 1, contact_seq: 1, start_offset_ms: 0, write_done_offset_ms: 0,
      first_upstream_byte_offset_ms: 642, end_offset_ms: 3280, outcome_code: 'completed'
    }],
    first_client_write_offset_ms: 646, upstream_terminal_offset_ms: 3280, downstream_delivery_done_offset_ms: 3283,
    downstream_delivery_outcome: 'delivered', downstream_bytes_written: 249549
  });
  assert.match(textOf(html), /链路轨迹.*WS.*chatgpt-subscription.*已完整交付.*243\.7 KB/);
  assert.doesNotMatch(textOf(html), /1 段选路|1 次联系|HTTP 200/);
  assert.doesNotMatch(html, /<details\b[^>]*\sopen(?:\s|=|>)/);
  const [contact, delivery] = html.match(/<details\b[\s\S]*?<\/details>/g).map(textOf);
  assert.match(contact, /响应完成.*本次耗时3\.280 s.*距请求开始.*写完请求\+0 ms.*上游首帧\+642 ms/);
  assert.match(delivery, /已完整交付.*首次下行\+646 ms.*上游终态\+3\.280 s.*交付结束\+3\.283 s/);
  assert.doesNotMatch(renderFlow(null), /链路轨迹|请求执行顺序/);
});

test('单次联系失败可展开查看原因代码或实际 HTTP 状态', () => {
  const renderContact = (contact) => renderFlow({
    transport: 'http', contacts: [{ leg_seq: 1, contact_seq: 1, ...contact }]
  });
  for (const [contact, label, reason] of [
    [{ outcome_code: 'write_error' }, '读写错误', 'write_error'],
    [{ http_status_code: 200, outcome_code: 'timeout' }, '超时', 'timeout'],
    [{ http_status_code: 429 }, '限流', 'HTTP 429'],
    [{ http_status_code: 503 }, 'HTTP 503', 'HTTP 503'],
    [{ http_status_code: 503, outcome_code: 'completed' }, '失败', 'HTTP 503']
  ]) {
    const html = renderContact(contact);
    const [detail] = html.match(/<details\b[\s\S]*?<\/details>/g);
    assert.ok(textOf(detail.split('</summary>')[0]).includes(label));
    assert.ok(textOf(detail).includes(`失败原因${reason}`));
    assert.doesNotMatch(detail, /响应完成/);
  }
  for (const outcome_code of ['completed', 'success', 'ok']) {
    assert.match(textOf(renderContact({ http_status_code: 200, outcome_code })), /响应完成HTTP 200/);
  }
});

test('仅收到响应头的请求保持联系中，已写出的下行可单独查看', () => {
  const flow = {
    transport: 'http',
    contacts: [{ leg_seq: 1, contact_seq: 1, start_offset_ms: 0, response_headers_offset_ms: 17, end_offset_ms: null, http_status_code: 200 }],
    first_client_write_offset_ms: 20,
    downstream_bytes_written: 92
  };
  const active = textOf(renderFlow(flow, 'processing'));
  assert.match(active, /联系中HTTP 200.*收到响应头\+17 ms.*交付中.*下行 92 B.*首次下行\+20 ms/);
  assert.doesNotMatch(active, /响应完成|本次尝试耗时|已完整交付/);
  assert.match(textOf(renderFlow(flow)), /结果未记录HTTP 200.*交付结果未记录/);
});

test('同账号重试及换号按实际联系顺序展开，裁剪后仍显示真实总数', () => {
  const html = renderFlow({
    transport: 'http', total_legs: 6, total_contacts: 9, truncated: true,
    legs: [
      { leg_seq: 1, target_snapshot: { upstream_name: '候选' } },
      { leg_seq: 2, target_snapshot: { upstream_type: 'account', upstream_id: 21, upstream_name: '账号甲' } },
      { leg_seq: 6, target_snapshot: { upstream_type: 'account', upstream_id: 42, upstream_name: '账号乙' } }
    ],
    contacts: [
      { leg_seq: 2, contact_seq: 1, http_status_code: 429, outcome_code: 'rate_limited', start_offset_ms: 0, end_offset_ms: 100 },
      { leg_seq: 2, contact_seq: 2, http_status_code: 503, transport_resend: true },
      { leg_seq: 6, contact_seq: 9, http_status_code: 200, outcome_code: 'completed' }
    ]
  });
  const text = textOf(html);
  assert.match(text, /6 段选路 · 9 次联系.*账号甲限流HTTP 429.*100 ms.*账号甲失败HTTP 503传输重发.*6 次联系未展示.*账号乙响应完成HTTP 200/);
  assert.doesNotMatch(text, /候选|切换账号/);
});

test('按实际联系的账号 ID 统计换号，最后一段尚未发出时保留真实事件', () => {
  const legs = [17, 45, 86].map((id, index) => ({
    leg_seq: index + 1, selected_offset_ms: index * 100,
    target_snapshot: { upstream_id: id, upstream_type: 'account', upstream_name: '同名账号' }
  }));
  const contacts = [1, 1, 2].map((leg_seq, i) => ({
    contact_seq: i + 1, leg_seq, first_upstream_byte_offset_ms: 20 + i * 50
  }));
  const renderTrace = (ls, cs) => renderFlow({ transport: 'http', legs: ls, contacts: cs });
  const repeated = textOf(renderTrace(legs.slice(0, 1), contacts.slice(0, 2)));
  assert.match(repeated, /同一账号 · 联系 2 次.*同账号重试/);
  const html = renderTrace(legs, contacts);
  assert.match(textOf(html), /换号 1 次 · 联系 3 次.*切换账号.*上游首字节\+120 ms/);
  const selection = textOf(html.match(/<details\b[\s\S]*?<\/details>/g).find((node) => node.includes('已选定上游')));
  assert.match(selection, /同名账号已选定上游.*选定上游\+200 ms/);
  assert.doesNotMatch(selection, /上游首字节|本次耗时|undefined/);
});

test('本地拒绝和不完整交付只列已记录的时刻，按实际发生顺序排列', () => {
  const html = renderFlow({
    transport: 'ws', origin: 'server_successor', upstream_terminal_offset_ms: 900,
    downstream_delivery_done_offset_ms: 852, downstream_delivery_outcome: 'partial_or_failed',
    downstream_flush_error: true
  });
  const text = textOf(html);
  assert.match(text, /服务端续推.*交付不完整.*Flush 异常/);
  assert.match(html, /text-warn[^>]*>Flush 异常<\/span>/);
  assert.match(text, /交付结束\+852 ms.*上游终态\+900 ms/);
  assert.doesNotMatch(text, /0 段选路|0 次联系|上游首帧|上游首字节|首次下行/);
  const missing = renderFlow({ transport: 'http', origin: 'client_request' });
  assert.match(textOf(missing), /交付结果未记录/);
  assert.doesNotMatch(textOf(missing), /0 段选路|0 次联系/);
  assert.doesNotMatch(missing, /<details\b/);
  // 单段选路尚未联系上游（如本地鉴权失败）同样不展示「1 段选路 · 0 次联系」
  const singleSelected = renderFlow({
    transport: 'http',
    legs: [{ leg_seq: 1, selected_offset_ms: 2, target_snapshot: { upstream_name: 'claude-subscription' } }],
    contacts: [],
    downstream_delivery_outcome: 'delivered'
  }, 'failed');
  assert.doesNotMatch(textOf(singleSelected), /1 段选路|0 次联系/);
  // 失败请求写回错误响应时不给绿色成功圆标
  assert.doesNotMatch(singleSelected, /border-success-line/);
});

test('客户端取消或断开走中性态和结束原因，无429限流与Flush异常保持警告色', () => {
  const disconnected = renderFlow({
    transport: 'http',
    contacts: [{ leg_seq: 1, contact_seq: 1, outcome_code: 'subscription_client_disconnected' }],
    downstream_delivery_outcome: 'aborted'
  }, 'cancelled');
  assert.match(textOf(disconnected), /已中断.*结束原因subscription_client_disconnected/);
  assert.doesNotMatch(textOf(disconnected), /失败原因/);
  assert.doesNotMatch(disconnected, /border-danger-line/);

  const cancelled = renderFlow({
    transport: 'http',
    contacts: [{ leg_seq: 1, contact_seq: 1, outcome_code: 'cancelled' }]
  }, 'cancelled');
  assert.match(textOf(cancelled), /已取消.*结束原因cancelled/);
  assert.doesNotMatch(cancelled, /border-danger-line/);

  // 无 HTTP 429 时 rate_limited 仍用警告色；兜底码显示中文标签；delivered + flush 异常降级警告色
  const warnFlow = renderFlow({
    transport: 'http',
    contacts: [
      { leg_seq: 1, contact_seq: 1, outcome_code: 'rate_limited', start_offset_ms: 0, end_offset_ms: 50 },
      { leg_seq: 1, contact_seq: 2, outcome_code: 'attempt_failed' },
      { leg_seq: 1, contact_seq: 3, outcome_code: 'leg_switched' }
    ],
    downstream_delivery_outcome: 'delivered',
    downstream_bytes_written: 128,
    downstream_flush_error: true
  });
  assert.match(warnFlow, /border-warn-line[\s\S]*?限流/);
  assert.match(textOf(warnFlow), /尝试失败.*切换上游/);
  assert.doesNotMatch(warnFlow, /border-success-line/);
  assert.match(warnFlow, /text-warn[^>]*>Flush 异常<\/span>/);
  // sr-only 不能嵌在 .font-mono 内避免复制时间时混入读屏前缀
  assert.doesNotMatch(warnFlow, /font-mono[^>]*><span class="sr-only/);
});

test('详情明细：按条上的顺序显示段名、耗时、说明，总耗时置顶在标题行右侧', () => {
  const html = render({
    status: 'completed', duration: 5000, isStreaming: true,
    upstreamDispatchMs: 100, upstreamConnMs: 300, upstreamWriteMs: 600,
    firstTokenMs: 400, completionMs: 4000
  });
  const legendStart = html.indexOf('data-lifecycle-legend');
  assert.ok(legendStart > -1, '应渲染图例区');
  const legend = html.slice(legendStart, html.indexOf('data-timing-summary'));
  let previous = -1;
  for (const [label, value] of [['本地处理', '100ms'], ['建连', '200ms'], ['发送', '300ms'], ['首响', '400ms'], ['流式输出', '4.00s']]) {
    const at = legend.indexOf(`>${label}</span><span class="font-mono tabular-nums text-fg-body">${value}</span>`);
    assert.ok(at > previous, `${label} 应依次显示准确耗时`);
    previous = at;
  }
  assert.match(legend, /title="写出请求头与请求体/);
  assert.doesNotMatch(legend, /总耗时/, '总耗时已置顶到区段右上角，图例区不再重复');
  assert.match(html, /data-lifecycle-total[^>]*>.*?总耗时.*?font-mono tabular-nums">5\.00s<\/span>/);
});

test('进行中明细只列已知阶段，总耗时显示进行中而非数字', () => {
  const html = render({ status: 'processing', duration: 0, upstreamConnMs: 200, upstreamWriteMs: 500, isStreaming: true });
  const legend = html.slice(html.indexOf('data-lifecycle-legend'), html.indexOf('data-timing-summary'));
  assert.match(legend, />建连<\/span><span class="font-mono tabular-nums text-fg-body">200ms/);
  assert.match(legend, />发送<\/span><span class="font-mono tabular-nums text-fg-body">300ms/);
  assert.match(html, /data-lifecycle-total[^>]*text-fg-subtle[^>]*>.*?总耗时.*?<span>进行中<\/span>/);
  assert.doesNotMatch(legend, />首响<\/span>|>流式输出<\/span>/);
  assert.doesNotMatch(html, /data-lifecycle-total[^>]*>.*?font-mono/);
});

test('尚无计时的进行中请求不显示伪造的总耗时分段', () => {
  const html = render({ status: 'pending', duration: 0, isStreaming: true });
  assert.doesNotMatch(html, /data-lifecycle-legend|>0ms<\/span>/);
  assert.match(html, /data-lifecycle-total[^>]*>.*?总耗时.*?<span>进行中<\/span>/);
});

test('重试属性：新数据显示次数，旧数据不显示不可信的1', () => {
  const legacy = render({ status: 'completed', duration: 1000, retryCount: 1 });
  assert.doesNotMatch(legacy, />重试</);
  const current = render({ status: 'completed', duration: 1000, retryCount: 1, upstreamFirstDispatchMs: 0 });
  assert.match(current, />重试</);
  assert.match(current, /font-mono tabular-nums">1<\/span> 次/);
});

test('旧数据保留换号与故障转移标记，无标记时隐藏整行', () => {
  const account = renderToStaticMarkup(createElement(LifecyclePanel, {
    request: { status: 'completed', duration: 1000, upstreamType: 'account', retryCount: 99 },
    lifecycle: { scheduleSnapshot: { candidates: [{ runtime_outcome: 'failed' }, { runtime_outcome: 'success' }] } }
  }));
  assert.match(account, /换号 1 次/);
  assert.doesNotMatch(account, />99</);
  assert.match(account, /候选决策（2）/);
  assert.doesNotMatch(account, /ml-\[94px\]/, '候选决策应内联到调度行，不再依赖固定左边距');
  const failover = render({ status: 'completed', duration: 1000, retryCount: 99, requestedEndpoint: 'a', effectiveEndpoint: 'b' });
  assert.match(failover, /故障转移/);
  assert.doesNotMatch(failover, />99</);
});

test('S2/C2/R1：思考品红、速度与首内容口径、结束原因文字、请求ID复制', () => {
  const html = render({ status: 'completed', isStreaming: true, duration: 2200,
    upstreamWriteMs: 100, firstTokenMs: 100, completionMs: 2000,
    firstContentMs: 1000, firstTextMs: 1000, reasoningHidden: true, streamFlowMs: 1500,
    outputTokens: 300, stopReason: 'max_tokens', upstreamRequestId: 'synthetic-upstream-request-id' });
  const text = textOf(html);
  assert.match(html, /bg-fuchsia-300 dark:bg-fuchsia-400/);
  assert.match(html, /data-timing-metric="first"[^>]*title="写完请求到上游第一个事件（受理、排队）"><span>首响<\/span><span[^>]*>100ms<\/span>/);
  assert.match(html, /data-timing-metric="content"[^>]*title="写完请求到首个内容（含思考或等待）"><span>首字<\/span><span[^>]*>900ms<\/span>/);
  assert.match(html, /data-timing-metric="completion"[^>]*title="上游第一个事件到流完成"><span>生成<\/span><span[^>]*>2\.00s<\/span>/);
  assert.match(text, /流式输出速度250 tok\/s/);
  assert.match(text, /生成吞吐150 tok\/s/);
  assert.match(html, /text-amber-700 dark:text-amber-300/);
  assert.match(html, /title="max_tokens">达到输出上限/);
  assert.match(html, /title="synthetic-upstream-request-id"/);
  assert.match(html, /复制上游请求 ID/);
  assert.match(html, /width="13" height="13"/);
  assert.doesNotMatch(render({ status: 'completed' }), /上游请求 ID/);
});

test('一次性下发给琥珀警示，不给虚假速度', () => {
  const html = render({ status: 'completed', isStreaming: true, duration: 2200,
    upstreamWriteMs: 100, firstTokenMs: 100, completionMs: 2000,
    firstContentMs: 1000, firstTextMs: 1000, streamFlowMs: 1, outputTokens: 300 });
  const text = textOf(html);
  assert.match(html, /data-timing-metric="流式输出速度"[^>]*text-warn[^>]*title="内容集中到达/);
  assert.match(text, /流式输出速度—（集中下发）/);
  assert.match(text, /生成吞吐150 tok\/s/);
  assert.doesNotMatch(text, /速度250|TPS /);
});

test('Codex无正文计时仍显示生成吞吐，缺少生成耗时不回退到请求总耗时', () => {
  const request = { status: 'completed', requestFamily: 'codex', isStreaming: true,
    duration: 9615, upstreamWriteMs: 1672, firstTokenMs: 2502, completionMs: 5440,
    firstContentMs: 6504, firstTextMs: null, streamFlowMs: 1747, reasoningHidden: true,
    outputTokens: 380, responseReasoning: 'reasoning_tokens:73' };
  const html = render(request);
  assert.match(textOf(html), /生成吞吐69\.9 tok\/s/);
  assert.match(textOf(html), /流式输出速度—（缺少计时）/);
  assert.match(html, /title="上游报告了推理 token，但未采集到正文增量时间/);
  assert.doesNotMatch(textOf(html), /39\.5 tok\/s|平均吞吐|TPS /);
  const legacy = textOf(render({ ...request, firstContentMs: null }));
  assert.match(legacy, /生成吞吐69\.9 tok\/s/);
  assert.match(legacy, /流式输出速度—（缺少计时）/);
  const regular = textOf(render({ ...request, isStreaming: false, completionMs: 0 }));
  assert.doesNotMatch(regular, /生成吞吐|流式输出速度/);
  assert.match(textOf(render({ ...request, completionMs: null })), /生成吞吐—（缺少计时）/);
  assert.doesNotMatch(textOf(render({ ...request, status: 'processing' })), /生成吞吐|流式输出速度/);
});

test('生成吞吐拒绝1ms整段返回，在详情标签悬停解释原因', () => {
  const html = render({ status: 'completed', requestFamily: 'claude', isStreaming: true,
    duration: 10000, upstreamWriteMs: 1000, firstTokenMs: 8999, completionMs: 1,
    outputTokens: 628, usageKnown: true });
  assert.match(html, /title="输出时间窗口不足 100ms，无法可靠测速。"><span>生成吞吐<\/span><span[^>]*>—（时间过短）<\/span>/);
  assert.doesNotMatch(textOf(html), /628000|生成吞吐[\d.]+ tok\/s/);
});

test('详情仅成功流式请求显示速度，其余状态与非流式保持隐藏', () => {
  const request = { status: 'completed', requestFamily: 'claude', isStreaming: true,
    duration: 2200, upstreamWriteMs: 100, firstTokenMs: 100, completionMs: 2000,
    firstContentMs: 1000, firstTextMs: 1000, streamFlowMs: 1500, outputTokens: 300, usageKnown: true };
  assert.match(textOf(render(request)), /生成吞吐150 tok\/s/);
  assert.match(textOf(render(request)), /流式输出速度250 tok\/s/);
  for (const fields of [
    ...['pending', 'forwarding', 'processing', 'retry', 'suspended', 'failed', 'cancelled', 'interrupted'].map((status) => ({ status })),
    { isStreaming: false }, { requestFamily: 'image' }
  ]) {
    assert.doesNotMatch(textOf(render({ ...request, ...fields })), /生成吞吐|流式输出速度/);
  }
  const missing = textOf(render({ ...request, usageKnown: false }));
  assert.match(missing, /生成吞吐—（缺少用量）/);
  assert.match(missing, /流式输出速度—（缺少用量）/);
});

test('复审顶部标签：图像首响用旧口径，无内容完成事件立即显示完整计时', () => {
  const image = render({ status: 'processing', requestFamily: 'image', isStreaming: true,
    duration: 0, firstTokenMs: 100, completionMs: null });
  assert.match(textOf(image), /首响100ms/);
  assert.doesNotMatch(image, /data-timing-metric="content"/);
  assert.doesNotMatch(textOf(image), /速度/);
  for (const requestFamily of ['image', 'claude', 'codex']) {
    const completed = render({ status: 'processing', requestFamily, isStreaming: true,
      duration: 0, firstTokenMs: 100, completionMs: 900, firstContentMs: null, firstContentAt: '' });
    assert.match(textOf(completed), /首响100ms/);
    assert.doesNotMatch(completed, /data-timing-metric="content"/);
    assert.match(textOf(completed), /生成900ms/);
    assert.doesNotMatch(textOf(completed), /速度/);
  }
});

test('详情首响与生成和列表口径一致，新旧数据只差首字与速度标签', () => {
  const request = { status: 'completed', isStreaming: true, duration: 20000,
    upstreamWriteMs: 983, firstTokenMs: 3680, firstContentMs: 8083, completionMs: 14720 };
  const html = render(request);
  const text = textOf(html);
  assert.match(text, /首响3\.68s/);
  assert.match(text, /首字7\.10s/);
  assert.match(text, /生成14\.7s/);
  assert.ok(text.indexOf('首响3.68s') < text.indexOf('首字7.10s'));
  assert.ok(text.indexOf('首字7.10s') < text.indexOf('生成14.7s'));
  const legacy = render({ ...request, firstContentMs: null });
  assert.match(textOf(legacy), /首响3\.68s/);
  assert.match(textOf(legacy), /生成14\.7s/);
  assert.doesNotMatch(legacy, /data-timing-metric="content"/);
});

test('进行中首内容实时事件先到时立即显示首字，首响只认后端真实值', () => {
  const request = { status: 'processing', isStreaming: true, duration: 0, firstTokenMs: null,
    completionMs: null, firstContentMs: null, firstTokenStartAt: '2026-10-30T00:00:00.983Z',
    firstContentAt: '2026-10-30T00:00:08.083Z' };
  const html = render(request);
  assert.match(textOf(html), /首字7\.10s/);
  assert.doesNotMatch(html, /data-timing-metric="first"|data-timing-metric="completion"/);
  const completedTiming = render({ ...request, firstTokenMs: 3680, completionMs: 14720 });
  assert.match(textOf(completedTiming), /首响3\.68s/);
  assert.match(textOf(completedTiming), /首字7\.10s/);
  assert.match(textOf(completedTiming), /生成14\.7s/);
  assert.match(completedTiming, /进行中，完成后显示完整耗时/);
});

test('选路前的本地响应也展示实际字节与交付结果', () => {
  for (const [outcome, label] of [['delivered', '已完整交付'], ['partial_or_failed', '交付不完整'], ['aborted', '交付已中止']]) {
    const html = renderFlow({ transport: 'http', downstream_bytes_written: outcome === 'aborted' ? 0 : 92, downstream_delivery_outcome: outcome });
    const text = html.replace(/<[^>]*>/g, '');
    assert.ok(text.includes('收到请求'));
    assert.doesNotMatch(text, /0 段选路|0 次联系/);
    assert.ok(text.includes(label));
    if (outcome !== 'aborted') assert.ok(text.includes('下行 92 B'));
  }
});
