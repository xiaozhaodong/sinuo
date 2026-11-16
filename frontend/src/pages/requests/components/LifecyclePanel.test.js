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

const renderFlow = (flow) => renderToStaticMarkup(createElement(LifecyclePanel, {
  request: { requestId: 'test', status: 'completed' }, lifecycle: { flow }
}));

test('单次 WS 请求默认收起，成功也能展开看真实毫秒时间线', () => {
  const html = renderFlow({
    transport: 'ws', legs: [{ leg_seq: 1, target_snapshot: { upstream_name: 'chatgpt-subscription' } }],
    contacts: [{ leg_seq: 1, contact_seq: 1, write_done_offset_ms: 0, first_upstream_byte_offset_ms: 642 }],
    first_client_write_offset_ms: 646, upstream_terminal_offset_ms: 3280, downstream_delivery_done_offset_ms: 3283,
    downstream_delivery_outcome: 'delivered', downstream_bytes_written: 249549
  });
  const [summary, detail] = html.split('<details');
  assert.match(summary, /链路轨迹[\s\S]*WS[\s\S]*chatgpt-subscription[\s\S]*已完整交付[\s\S]*243\.7 KB/);
  assert.doesNotMatch(summary, /1 段选路|1 次联系/);
  assert.doesNotMatch(detail.split('>')[0], /\bopen\b/);
  const text = detail.replace(/<[^>]*>/g, '');
  assert.match(text, /查看轨迹.*距请求开始.*发往上游0 ms.*收到上游首帧642 ms.*首次写给客户端646 ms.*收到上游终态3\.280 s.*下行交付结束3\.283 s/);
  assert.doesNotMatch(text, /上游尝试|HTTP 200/);
  assert.doesNotMatch(renderFlow(null), /链路轨迹|查看轨迹/);
});

test('单次联系失败可展开查看原因或 HTTP 错误，正常联系保持紧凑', () => {
  const renderContact = (contact) => renderFlow({
    transport: 'http', contacts: [{ leg_seq: 1, contact_seq: 1, ...contact }]
  });
  for (const [contact, expected] of [
    [{ outcome_code: 'write_error' }, '读写错误'],
    [{ http_status_code: 200, outcome_code: 'timeout' }, '超时'],
    [{ http_status_code: 503 }, 'HTTP 503']
  ]) {
    const html = renderContact(contact);
    const details = html.match(/<details\b[\s\S]*?<\/details>/)?.[0] || '';
    assert.ok(details.includes('查看轨迹') && details.includes(expected), expected);
  }
  for (const outcome_code of [undefined, 'completed', 'success', 'ok']) {
    const [summary, detail] = renderContact({ http_status_code: 200, outcome_code }).split('<details');
    assert.doesNotMatch(summary, /HTTP 200/);
    assert.match(detail, /HTTP 200/);
  }
});

test('同账号重试及换号按实际联系顺序展开，裁剪后仍显示真实总数', () => {
  const html = renderFlow({
    transport: 'http', total_legs: 6, total_contacts: 9, truncated: true,
    legs: [
      { leg_seq: 1, target_snapshot: { upstream_name: '候选' } },
      { leg_seq: 2, target_snapshot: { upstream_name: '账号甲' } },
      { leg_seq: 6, target_snapshot: { upstream_name: '账号乙' } }
    ],
    contacts: [
      { leg_seq: 2, contact_seq: 1, http_status_code: 429, outcome_code: 'rate_limited', start_offset_ms: 0, end_offset_ms: 100 },
      { leg_seq: 2, contact_seq: 2, http_status_code: 503, transport_resend: true },
      { leg_seq: 6, contact_seq: 9, http_status_code: 200, outcome_code: 'completed' }
    ]
  });
  assert.match(html, /6 段选路 · 9 次联系/);
  const attempts = html.match(/<details\b[\s\S]*?<\/details>/)?.[0] || '';
  assert.match(attempts, /上游尝试/);
  assert.doesNotMatch(attempts, /候选/);
  assert.match(attempts, /#1[\s\S]*?账号甲[\s\S]*?429[\s\S]*?限流[\s\S]*?100 ms[\s\S]*?#2[\s\S]*?账号甲[\s\S]*?503[\s\S]*?传输重发[\s\S]*?#9[\s\S]*?账号乙[\s\S]*?200/);
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
  assert.match(renderTrace(legs.slice(0, 1), contacts.slice(0, 2)), /同一账号 · 联系 2 次/);
  assert.match(renderTrace(legs.slice(0, 2), contacts), /收到上游首字节[\s\S]*120 ms/);
  const html = renderTrace(legs, contacts);
  assert.match(html, /换号 1 次 · 联系 3 次/);
  assert.match(html, /选定上游[\s\S]*200 ms/);
  assert.doesNotMatch(html, /收到上游首字节|发往同名账号|undefined/);
});

test('本地拒绝和不完整交付只列已记录的时刻，按实际发生顺序排列', () => {
  const html = renderFlow({
    transport: 'ws', origin: 'server_successor', upstream_terminal_offset_ms: 900,
    downstream_delivery_done_offset_ms: 852, downstream_delivery_outcome: 'partial_or_failed',
    downstream_flush_error: true
  });
  assert.match(html, /服务端续推[\s\S]*交付不完整[\s\S]*Flush 异常/);
  assert.match(html, /下行交付结束[\s\S]*852 ms[\s\S]*收到上游终态[\s\S]*900 ms/);
  assert.doesNotMatch(html, /发往上游|收到上游首帧|首次写给客户端/);
  assert.match(renderFlow({ transport: 'http', origin: 'client_request' }), /查看轨迹[\s\S]*暂无已记录的时间点/);
});

test('详情明细：按条上的顺序显示段名、耗时、说明与总耗时', () => {
  const html = render({
    status: 'completed', duration: 5000, isStreaming: true,
    upstreamDispatchMs: 100, upstreamConnMs: 300, upstreamWriteMs: 600,
    firstTokenMs: 400, completionMs: 4000
  });
  const details = html.slice(html.indexOf('mt-1.5 flex flex-wrap'));
  let previous = -1;
  for (const [label, value] of [['本地处理', '100ms'], ['建连', '200ms'], ['发送', '300ms'], ['首响', '400ms'], ['流式输出', '4.00s']]) {
    const at = details.indexOf(`>${label}</span><span class="font-mono">${value}</span>`);
    assert.ok(at > previous, `${label} 应依次显示准确耗时`);
    previous = at;
  }
  assert.match(details, /title="写出请求头与请求体/);
  assert.match(details, /font-semibold[^>]*><span>总耗时<\/span><span class="font-mono">5\.00s<\/span>/);
});

test('进行中明细只列已知阶段，总耗时显示进行中而非数字', () => {
  const html = render({ status: 'processing', duration: 0, upstreamConnMs: 200, upstreamWriteMs: 500, isStreaming: true });
  const details = html.slice(html.indexOf('mt-1.5 flex flex-wrap'));
  assert.match(details, />建连<\/span><span class="font-mono">200ms/);
  assert.match(details, />发送<\/span><span class="font-mono">300ms/);
  assert.match(details, /text-fg-subtle[^>]*><span>总耗时<\/span><span>进行中<\/span>/);
  assert.doesNotMatch(details, />首响<\/span>|>流式输出<\/span>|总耗时<\/span><span class="font-mono">/);
});

test('尚无计时的进行中请求不显示伪造的总耗时分段', () => {
  const html = render({ status: 'pending', duration: 0, isStreaming: true });
  const details = html.slice(html.indexOf('mt-1.5 flex flex-wrap'));
  assert.doesNotMatch(details, /rounded-sm|>0ms<\/span>/);
  assert.match(details, /<span>总耗时<\/span><span>进行中<\/span>/);
});

test('重试属性：新数据显示次数，旧数据不显示不可信的1', () => {
  const legacy = render({ status: 'completed', duration: 1000, retryCount: 1 });
  assert.doesNotMatch(legacy, />重试</);
  const current = render({ status: 'completed', duration: 1000, retryCount: 1, upstreamFirstDispatchMs: 0 });
  assert.match(current, />重试</);
  assert.match(current, /font-mono">1<\/span> 次/);
});

test('旧数据保留换号与故障转移标记，无标记时隐藏整行', () => {
  const account = renderToStaticMarkup(createElement(LifecyclePanel, {
    request: { status: 'completed', duration: 1000, upstreamType: 'account', retryCount: 99 },
    lifecycle: { scheduleSnapshot: { candidates: [{ runtime_outcome: 'failed' }, { runtime_outcome: 'success' }] } }
  }));
  assert.match(account, /换号 1 次/);
  assert.doesNotMatch(account, />99</);
  const failover = render({ status: 'completed', duration: 1000, retryCount: 99, requestedEndpoint: 'a', effectiveEndpoint: 'b' });
  assert.match(failover, /故障转移/);
  assert.doesNotMatch(failover, />99</);
});

test('S2/C2/R1：思考品红、速度与首内容口径、结束原因文字、请求ID复制', () => {
  const html = render({ status: 'completed', isStreaming: true, duration: 2200,
    upstreamWriteMs: 100, firstTokenMs: 100, completionMs: 2000,
    firstContentMs: 1000, firstTextMs: 1000, reasoningHidden: true, streamFlowMs: 1500,
    outputTokens: 300, stopReason: 'max_tokens', upstreamRequestId: 'synthetic-upstream-request-id' });
  assert.match(html, /bg-fuchsia-200 dark:bg-fuchsia-800/);
  assert.match(html, /tone-violet[^>]*title="写完请求到上游第一个事件（受理、排队）">首响 100ms/);
  assert.match(html, /首字 900ms/);
  assert.match(html, /tone-fuchsia[^>]*title="写完请求到首个内容（含思考或等待）">首字 900ms/);
  assert.match(html, /tone-emerald[^>]*title="上游第一个事件到流完成">生成 2\.00s/);
  assert.match(html, /速度 250 tok\/s/);
  assert.match(html, /生成吞吐 150 tok\/s/);
  assert.match(html, /text-amber-700 dark:text-amber-300/);
  assert.match(html, /title="max_tokens">达到输出上限/);
  assert.match(html, /title="synthetic-upstream-request-id"/);
  assert.match(html, /复制上游请求 ID/);
  assert.match(html, /width="13" height="13"/);
  assert.doesNotMatch(render({ status: 'completed' }), /上游请求 ID/);
});

test('一次性下发给琥珀标签，不给虚假速度', () => {
  const html = render({ status: 'completed', isStreaming: true, duration: 2200,
    upstreamWriteMs: 100, firstTokenMs: 100, completionMs: 2000,
    firstContentMs: 1000, firstTextMs: 1000, streamFlowMs: 1, outputTokens: 300 });
  assert.match(html, /tone-amber[^>]*title="内容集中到达/);
  assert.match(html, /流式输出速度 —（集中下发）<\/span>/);
  assert.match(html, /生成吞吐 150 tok\/s/);
  assert.doesNotMatch(html, /速度 250|TPS /);
});

test('Codex无正文计时仍显示生成吞吐，缺少生成耗时不回退到请求总耗时', () => {
  const request = { status: 'completed', requestFamily: 'codex', isStreaming: true,
    duration: 9615, upstreamWriteMs: 1672, firstTokenMs: 2502, completionMs: 5440,
    firstContentMs: 6504, firstTextMs: null, streamFlowMs: 1747, reasoningHidden: true,
    outputTokens: 380, responseReasoning: 'reasoning_tokens:73' };
  const html = render(request);
  assert.match(html, /生成吞吐 69\.9 tok\/s/);
  assert.match(html, /流式输出速度 —（缺少计时）/);
  assert.match(html, /title="上游报告了推理 token，但未采集到正文增量时间/);
  assert.doesNotMatch(html, /39\.5 tok\/s|平均吞吐|TPS /);
  const legacy = render({ ...request, firstContentMs: null });
  assert.match(legacy, /生成吞吐 69\.9 tok\/s/);
  assert.match(legacy, /流式输出速度 —（缺少计时）/);
  const regular = render({ ...request, isStreaming: false, completionMs: 0 });
  assert.doesNotMatch(regular, /生成吞吐|流式输出速度/);
  assert.match(render({ ...request, completionMs: null }), /生成吞吐 —（缺少计时）/);
  assert.doesNotMatch(render({ ...request, status: 'processing' }), /生成吞吐|流式输出速度/);
});

test('生成吞吐拒绝1ms整段返回，在详情标签悬停解释原因', () => {
  const html = render({ status: 'completed', requestFamily: 'claude', isStreaming: true,
    duration: 10000, upstreamWriteMs: 1000, firstTokenMs: 8999, completionMs: 1,
    outputTokens: 628, usageKnown: true });
  assert.match(html, /title="输出时间窗口不足 100ms，无法可靠测速。">生成吞吐 —（时间过短）/);
  assert.doesNotMatch(html, /628000|生成吞吐 [\d.]+ tok\/s/);
});

test('详情仅成功流式请求显示速度，其余状态与非流式保持隐藏', () => {
  const request = { status: 'completed', requestFamily: 'claude', isStreaming: true,
    duration: 2200, upstreamWriteMs: 100, firstTokenMs: 100, completionMs: 2000,
    firstContentMs: 1000, firstTextMs: 1000, streamFlowMs: 1500, outputTokens: 300, usageKnown: true };
  assert.match(render(request), /生成吞吐 150 tok\/s/);
  assert.match(render(request), /流式输出速度 250 tok\/s/);
  for (const fields of [
    ...['pending', 'forwarding', 'processing', 'retry', 'suspended', 'failed', 'cancelled', 'interrupted'].map((status) => ({ status })),
    { isStreaming: false }, { requestFamily: 'image' }
  ]) {
    assert.doesNotMatch(render({ ...request, ...fields }), /生成吞吐|流式输出速度/);
  }
  const missing = render({ ...request, usageKnown: false });
  assert.match(missing, /生成吞吐 —（缺少用量）/);
  assert.match(missing, /流式输出速度 —（缺少用量）/);
});

test('复审顶部标签：图像首响用旧口径，无内容完成事件立即显示完整计时', () => {
  const image = render({ status: 'processing', requestFamily: 'image', isStreaming: true,
    duration: 0, firstTokenMs: 100, completionMs: null });
  assert.match(image, /首响 100ms/);
  assert.doesNotMatch(image, /tone-fuchsia[^>]*>首字 /);
  assert.doesNotMatch(image, /速度 /);
  for (const requestFamily of ['image', 'claude', 'codex']) {
    const completed = render({ status: 'processing', requestFamily, isStreaming: true,
      duration: 0, firstTokenMs: 100, completionMs: 900, firstContentMs: null, firstContentAt: '' });
    assert.match(completed, /首响 100ms/);
    assert.doesNotMatch(completed, /tone-fuchsia[^>]*>首字 /);
    assert.match(completed, /生成 900ms/);
    assert.doesNotMatch(completed, /速度 /);
  }
});

test('详情首响与生成和列表口径一致，新旧数据只差首字与速度标签', () => {
  const request = { status: 'completed', isStreaming: true, duration: 20000,
    upstreamWriteMs: 983, firstTokenMs: 3680, firstContentMs: 8083, completionMs: 14720 };
  const html = render(request);
  assert.match(html, /首响 3\.68s/);
  assert.match(html, /首字 7\.10s/);
  assert.match(html, /生成 14\.7s/);
  assert.ok(html.indexOf('首响 3.68s') < html.indexOf('首字 7.10s'));
  assert.ok(html.indexOf('首字 7.10s') < html.indexOf('生成 14.7s'));
  const legacy = render({ ...request, firstContentMs: null });
  assert.match(legacy, /首响 3\.68s/);
  assert.match(legacy, /生成 14\.7s/);
  assert.doesNotMatch(legacy, /tone-fuchsia[^>]*>首字 /);
});

test('进行中首内容实时事件先到时立即显示首字，首响只认后端真实值', () => {
  const request = { status: 'processing', isStreaming: true, duration: 0, firstTokenMs: null,
    completionMs: null, firstContentMs: null, firstTokenStartAt: '2026-10-30T00:00:00.983Z',
    firstContentAt: '2026-10-30T00:00:08.083Z' };
  const html = render(request);
  assert.match(html, /首字 7\.10s/);
  assert.doesNotMatch(html, />首响 |生成 /);
  const completedTiming = render({ ...request, firstTokenMs: 3680, completionMs: 14720 });
  assert.match(completedTiming, /首响 3\.68s/);
  assert.match(completedTiming, /首字 7\.10s/);
  assert.match(completedTiming, /生成 14\.7s/);
  assert.match(completedTiming, /进行中，完成后显示完整耗时/);
});

test('选路前的本地响应也展示实际字节与交付结果', () => {
  for (const [outcome, label] of [['delivered', '已完整交付'], ['partial_or_failed', '交付不完整'], ['aborted', '交付已中止']]) {
    const html = renderFlow({ transport: 'http', downstream_bytes_written: outcome === 'aborted' ? 0 : 92, downstream_delivery_outcome: outcome });
    const text = html.replace(/<[^>]*>/g, '');
    assert.ok(text.includes('0 段选路 · 0 次联系'));
    assert.ok(text.includes(label));
    if (outcome !== 'aborted') assert.ok(text.includes('下行 92 B'));
  }
});
