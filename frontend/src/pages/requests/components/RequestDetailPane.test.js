import test from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { createRequire } from 'node:module';
import { fileURLToPath, pathToFileURL } from 'node:url';
import { build } from 'esbuild';
import { createElement } from 'react';
import { renderToStaticMarkup } from 'react-dom/server';
import { normalizeRequest } from '../utils/normalizeRequest.js';

const require = createRequire(import.meta.url);
const fromSource = (path) => fileURLToPath(new URL(`../../../${path}`, import.meta.url));

const compiled = await build({
  entryPoints: [fileURLToPath(new URL('./RequestDetailPane.jsx', import.meta.url))],
  bundle: true,
  write: false,
  platform: 'node',
  format: 'esm',
  jsx: 'automatic',
  alias: {
    '@components': fromSource('components'),
    '@utils': fromSource('utils'),
    '@pages': fromSource('pages'),
    '@wailsjs': fromSource('bindings')
  },
  plugins: [{
    name: 'detail-pane-test-env',
    setup(builder) {
      builder.onResolve({ filter: /^(react(?:\/.*)?|react-dom(?:\/.*)?|lucide-react)$/ }, ({ path }) => ({
        path: pathToFileURL(require.resolve(path)).href,
        external: true
      }));
      builder.onResolve({ filter: /^@contexts\/TimezoneContext\.jsx$/ }, () => ({ path: 'timezone', namespace: 'mock' }));
      builder.onResolve({ filter: /^\.\.\/hooks\/useRequestLifecycleDetail\.js$/ }, () => ({ path: 'lifecycle', namespace: 'mock' }));
      builder.onLoad({ filter: /.*/, namespace: 'mock' }, ({ path }) => {
        if (path === 'timezone') {
          return { contents: 'export const useTimezone = () => ({ formatTimestamp: (v) => String(v || "") });' };
        }
        return { contents: 'export default () => globalThis.__MOCK_LIFECYCLE__;' };
      });
    }
  }]
});

const { default: RequestDetailPane } = await import(
  `data:text/javascript;base64,${Buffer.from(compiled.outputFiles[0].text).toString('base64')}`
);

const renderPane = (request, lifecycleState = { lifecycle: null, lifecycleLoading: false }) => {
  globalThis.__MOCK_LIFECYCLE__ = lifecycleState;
  return renderToStaticMarkup(createElement(RequestDetailPane, {
    request,
    onClose: () => {},
    onFilterSession: () => {}
  }));
};

const textOf = (markup) => markup.replace(/<[^>]*>/g, '');

test('三栏配平：中栏含概要与模型与思考，右栏顶置成本与合计并展示缓存命中率', () => {
  const html = renderPane(normalizeRequest({
    request_id: 'req-demo-1',
    start_time: '2026-11-20 08:58:14',
    status: 'completed',
    is_streaming: true,
    duration_ms: 7420,
    request_family: 'claude',
    request_kind: 'generation',
    upstream_name: 'relay-tokyo-pro',
    session_id: 'sess-1234567890',
    route_mode: 'auto',
    method: 'POST',
    path: '/v1/messages',
    http_status_code: 200,
    request_model: 'claude-opus-4-6',
    upstream_model: 'claude-opus-4-6-20260401',
    response_model: 'claude-opus-4-6-20260401',
    input_tokens: 4000,
    output_tokens: 1000,
    cache_read_tokens: 30000,
    cache_creation_tokens: 6000,
    cache_creation_5m_tokens: 4000,
    cache_creation_1h_tokens: 2000,
    total_cost_usd: 0.0482,
    usage_known: true,
    cost_known: true
  }));

  // 顶栏常驻摘要锚点
  assert.match(html, /data-header-anchor[\s\S]*?7\.42s[\s\S]*?>\$0\.0482<\/span>/);
  // 三栏分隔使用明确 bg-line，三栏独立纵向滚动且挂 data-detail-scroll
  assert.match(html, /gap-px bg-line @xl:grid-cols-2 @4xl:h-full @4xl:grid-cols-\[1\.35fr_1fr_1\.05fr\]/);
  assert.equal((html.match(/data-detail-scroll/g) || []).length, 3);
  // 顺序：生命周期 -> 概要 -> 模型与思考 -> Token 与成本
  const order = ['生命周期', '概要', '模型与思考', 'Token 与成本'].map((heading) => html.indexOf(`>${heading}<`));
  assert.ok(order.every((idx) => idx > -1), '四个核心分区标题都应存在');
  assert.ok(order[0] < order[1] && order[1] < order[2] && order[2] < order[3], '分区顺序应与三栏重组一致');
  // HTTP 200 使用等宽成功色
  assert.match(html, /font-mono text-xs font-medium tabular-nums text-success">200<\/span>/);
  // 缓存命中率：30000 / (4000 + 6000 + 30000) = 75.0%
  assert.match(html, /text-success[^>]*>缓存命中 75\.0%<\/span>/);
  assert.match(textOf(html), /本次成本\$0\.0482合计 Token41,000/);
  assert.match(textOf(html), /缓存创建6,000/);
});

test('缓存命中率与列表唯一口径一致：缺字段留空不伪造，确定零命中显示 0.0%', () => {
  // Claude 缓存创建量缺失：normalizeInputUsage 产出 cacheRatio: null，详情不能把 null 当 0 算出 25.3%
  const missingCreation = renderPane(normalizeRequest({
    request_id: 'req-missing-creation',
    status: 'completed',
    request_family: 'claude',
    path: '/v1/messages',
    input_tokens: 2840,
    output_tokens: 200,
    cache_read_tokens: 960,
    cache_creation_tokens: null,
    usage_known: true
  }));
  assert.doesNotMatch(textOf(missingCreation), /缓存命中|25\.3%/);
  assert.match(textOf(missingCreation), /缓存创建—/);
  assert.match(textOf(missingCreation), /合计 Token—/);

  // 确定零命中：展示中性态 0.0%，与列表一致，不用成功绿
  const zeroHit = renderPane(normalizeRequest({
    request_id: 'req-zero-hit',
    status: 'completed',
    request_family: 'claude',
    path: '/v1/messages',
    input_tokens: 2840,
    output_tokens: 200,
    cache_read_tokens: 0,
    cache_creation_tokens: 0,
    usage_known: true
  }));
  assert.match(zeroHit, /text-fg-muted[^>]*>缓存命中 0\.0%<\/span>/);
  assert.doesNotMatch(zeroHit, /text-success[^>]*>缓存命中/);
  assert.match(textOf(zeroHit), /缓存创建0/);
  assert.match(textOf(zeroHit), /合计 Token3,040/);

  // 在途或用量未知时即使字段有值也留空
  const pendingWithRatio = renderPane({
    requestId: 'req-pending-ratio',
    status: 'processing',
    cacheRatio: 0.5
  });
  assert.doesNotMatch(textOf(pendingWithRatio), /缓存命中/);
});

test('缓存创建只在两项分档齐全时可还原；Responses 的合计直接使用输入和输出', () => {
  const base = { request_id: 'req-cache-split', status: 'completed', request_family: 'claude',
    input_tokens: 0, output_tokens: 0, cache_read_tokens: 0 };
  for (const [fields, creation, total] of [
    [{ cache_creation_5m_tokens: 20 }, '—', '—'],
    [{ cache_creation_5m_tokens: 20, cache_creation_1h_tokens: 5 }, '25', '25'],
    [{ cache_creation_5m_tokens: 0, cache_creation_1h_tokens: 0 }, '0', '0'],
    [{ request_family: 'codex', input_tokens: 80, output_tokens: 20 }, '—', '100']
  ]) {
    const text = textOf(renderPane(normalizeRequest({ ...base, ...fields })));
    assert.ok(text.includes(`缓存创建${creation}`));
    assert.ok(text.includes(`合计 Token${total}`));
  }
});

test('详情顶栏与本次成本均显示查询返回的原始小额费用', () => {
  const row = normalizeRequest({ request_id: 'tiny-cost', status: 'completed', cost: 0 });
  const detail = normalizeRequest({ ...row, total_cost_usd: 0.00023 });
  const html = renderPane(row, { lifecycle: { request: detail }, lifecycleLoading: false });
  assert.match(html, /data-header-anchor[\s\S]*?>\$0\.00023<\/span>/);
  assert.match(textOf(html), /本次成本\$0\.00023合计 Token/);
});

test('详情顶栏与本次成本最多显示六位小数', () => {
  const row = normalizeRequest({ status: 'completed', total_cost_usd: 0.019312199999999998 });
  const html = renderPane(row);
  assert.match(html, /data-header-anchor[\s\S]*?>\$0\.019312<\/span>/);
  assert.match(textOf(html), /本次成本\$0\.019312合计 Token/);
  assert.doesNotMatch(html, /01931219999/);
});

test('详情两处成本使用一致的阈值，悬停精度不会把阈值下方的费用进位到阈值', () => {
  for (const [cost, display, title] of [
    [2.3e-9, '&lt;$0.000001', '$0.0000000023'],
    [9.999999e-7, '&lt;$0.000001', '$0.0000009999999'],
    [9.999999999999997e-7, '&lt;$0.000001', '$0.0000009999999999999997'],
    [0.000001, '$0.000001', '$0.000001'],
    [0.0009999, '&lt;$0.001', '$0.0009999'],
    [0.001, '$0.001', '$0.001']
  ]) {
    const html = renderPane(normalizeRequest({ status: 'completed', total_cost_usd: cost }));
    assert.equal(html.split(`title="${title}">${display}<`).length - 1, 2, String(cost));
  }
});

test('滚动复位仅跟随切换后的请求 ID，不因同请求实时刷新跳回顶部', () => {
  const source = readFileSync(new URL('./RequestDetailPane.jsx', import.meta.url), 'utf8');
  const effect = source.match(/useEffect\(\(\) => \{[\s\S]*?scrollTop = 0;[\s\S]*?\n  \}, \[([^\]]*)\]\);/);
  assert.ok(effect, '应存在复位三栏与外层滚动位置的 effect');
  assert.match(effect[0], /querySelectorAll\('\[data-detail-scroll\]'\)/);
  assert.equal(effect[1].trim(), 'selectedRequestId');
});

test('失败与在途状态：HTTP 5xx 警示着色，在途请求顶栏与汇总头保持占位符', () => {
  const failedHtml = renderPane({
    requestId: 'req-fail',
    timestamp: '2026-11-20 08:49:21',
    status: 'failed',
    isStreaming: true,
    duration: 4180,
    httpStatusCode: 502,
    usageKnown: false,
    costKnown: false
  });
  assert.match(failedHtml, /text-danger">502<\/span>/);
  assert.doesNotMatch(textOf(failedHtml), /缓存命中/);

  const liveHtml = renderPane({
    requestId: 'req-live',
    timestamp: '2026-11-20 09:08:52',
    status: 'processing',
    isStreaming: true,
    duration: 0,
    inputTokens: 0,
    outputTokens: 0,
    cost: 0
  });
  assert.match(liveHtml, /data-header-anchor[\s\S]*?进行中[\s\S]*?—/);
  assert.match(textOf(liveHtml), /本次成本—.*合计 Token—/);
});
