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
  assert.match(html, /tone-amber[^>]*title="内容几乎同时到达/);
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
  assert.match(html, /流式输出速度 —（缺少正文计时）/);
  assert.match(html, /title="上游报告了推理 token，但未采集到正文增量时间/);
  assert.doesNotMatch(html, /39\.5 tok\/s|平均吞吐|TPS /);
  const legacy = render({ ...request, firstContentMs: null });
  assert.match(legacy, /生成吞吐 69\.9 tok\/s/);
  assert.match(legacy, /流式输出速度 —（缺少流式计时）/);
  const regular = render({ ...request, isStreaming: false, completionMs: 0 });
  assert.match(regular, /生成吞吐 —/);
  assert.doesNotMatch(regular, /流式输出速度/);
  assert.match(render({ ...request, completionMs: null }), /生成吞吐 —/);
  assert.doesNotMatch(render({ ...request, status: 'processing' }), /生成吞吐|流式输出速度/);
});

test('生成吞吐拒绝1ms整段返回，在详情标签悬停解释原因', () => {
  const html = render({ status: 'completed', requestFamily: 'claude', isStreaming: true,
    duration: 10000, upstreamWriteMs: 1000, firstTokenMs: 8999, completionMs: 1,
    outputTokens: 628, usageKnown: true });
  assert.match(html, /title="生成耗时过短（上游一次性返回），无法计算">生成吞吐 —/);
  assert.doesNotMatch(html, /628000|生成吞吐 [\d.]+ tok\/s/);
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
