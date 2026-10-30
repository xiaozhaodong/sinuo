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
  for (const [label, value] of [['本地处理', '100ms'], ['建连', '200ms'], ['发送', '300ms'], ['首字', '400ms'], ['流式输出', '4.00s']]) {
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
  assert.doesNotMatch(details, />首字<\/span>|>流式输出<\/span>|总耗时<\/span><span class="font-mono">/);
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
