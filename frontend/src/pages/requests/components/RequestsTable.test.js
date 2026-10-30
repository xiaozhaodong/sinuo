import test from 'node:test';
import assert from 'node:assert/strict';
import { createRequire } from 'node:module';
import { fileURLToPath, pathToFileURL } from 'node:url';
import { build } from 'esbuild';
import { createElement } from 'react';
import { renderToStaticMarkup } from 'react-dom/server';

// 编译真实表格与计时组件；仅替换时区配置和费用API，避免启动桌面运行时。
const require = createRequire(import.meta.url);
const compiled = await build({
  entryPoints: [fileURLToPath(new URL('./RequestsTable.jsx', import.meta.url))],
  bundle: true, write: false, platform: 'node', format: 'esm', jsx: 'automatic',
  alias: {
    '@components': fileURLToPath(new URL('../../../components', import.meta.url)),
    '@utils': fileURLToPath(new URL('../../../utils', import.meta.url)),
    '@contexts': fileURLToPath(new URL('../../../contexts', import.meta.url))
  },
  plugins: [{
    name: 'table-test-environment',
    setup(builder) {
      builder.onResolve({ filter: /^(react(?:\/.*)?|react-dom(?:\/.*)?|lucide-react)$/ }, ({ path }) => ({
        path: pathToFileURL(require.resolve(path)).href, external: true
      }));
      builder.onResolve({ filter: /^@contexts\/TimezoneContext\.jsx$/ }, () => ({ path: 'timezone', namespace: 'test-env' }));
      builder.onResolve({ filter: /^@utils\/api\.js$/ }, () => ({ path: 'api', namespace: 'test-env' }));
      builder.onLoad({ filter: /.*/, namespace: 'test-env' }, ({ path }) => ({
        contents: path === 'timezone'
          ? 'export const useTimezone = () => ({formatListTimestamp: value => value});'
          : 'export const formatCost = value => String(value);'
      }));
    }
  }]
});
const { default: RequestsTable } = await import(`data:text/javascript;base64,${Buffer.from(compiled.outputFiles[0].text).toString('base64')}`);
const render = (fields) => renderToStaticMarkup(createElement(RequestsTable, {
  requests: [{ requestId: 'retry-row', duration: 1000, status: 'completed', firstTokenMs: 100, completionMs: 900, ...fields }],
  visibleColumns: ['duration'], columnConfigs: [{ id: 'duration', label: '耗时' }]
}));

test('已结束耗时格：新记录末尾显示11px图标和次数，旧记录隐藏', () => {
  const html = render({ retryCount: 1, upstreamFirstDispatchMs: 0 });
  assert.match(html, /title="重试 1 次"/);
  assert.match(html, /lucide-rotate-cw/);
  assert.match(html, /width="11" height="11"/);
  assert.match(html, /stroke-width="2.5"/);
  assert.ok(html.indexOf('900ms') < html.indexOf('title="重试 1 次"'), '标记放在生成位之后');
  assert.doesNotMatch(render({ retryCount: 1 }), /title="重试|lucide-rotate-cw/);
  assert.doesNotMatch(render({ retryCount: 0, upstreamFirstDispatchMs: 0 }), /title="重试|lucide-rotate-cw/);
});

test('进行中重试先回等待点，收到新写完后从新起点恢复跑秒，次数标记保留', () => {
  const request = {
    status: 'forwarding', duration: 0, firstTokenMs: null, completionMs: null,
    firstTokenStartAt: '2026-10-30T00:00:00.000000Z',
    retryStartedAt: '2026-10-30T00:00:02.000000Z', retryCount: 1
  };
  const waiting = render(request);
  assert.match(waiting, /pending-send-dots/);
  assert.match(waiting, /title="上一次尝试未成功，正在重试（尚未发到上游）"/);
  assert.match(waiting, /<i>·<\/i><i>·<\/i><i>·<\/i>/);
  assert.match(waiting, /title="重试 1 次"/);
  const sent = render({ ...request, firstTokenStartAt: '2026-10-30T00:00:03.000000Z' });
  assert.doesNotMatch(sent, /pending-send-dots/);
  assert.match(sent, /title="重试 1 次"/);
});

test('首次发送前沿用原等待点文案，首响已落定后不再进入重试等待', () => {
  const before = render({ status: 'forwarding', firstTokenMs: null, completionMs: null });
  assert.match(before, /尚未发到上游（本地处理 \/ 建连中）/);
  const settledFirst = render({
    status: 'processing', firstTokenMs: 100, completionMs: null,
    firstTokenStartAt: '2026-10-30T00:00:00.000000Z', retryStartedAt: '2026-10-30T00:00:02.000000Z', retryCount: 1
  });
  assert.doesNotMatch(settledFirst, /pending-send-dots/);
  assert.match(settledFirst, />100ms<\/span>/);
});

test('新数据仍按首响和生成定格，W1只给警示级且排在重试之后', () => {
  const fields = { isStreaming: true, upstreamWriteMs: 983, firstTokenMs: 3680,
    firstContentMs: 8083, firstTextMs: 8083, completionMs: 14720, duration: 20000,
    retryCount: 1, upstreamFirstDispatchMs: 0, stopReason: 'max_tokens' };
  const html = render(fields);
  assert.match(html, />3\.68s<\/span>/);
  assert.match(html, />14\.7s<\/span>/);
  assert.doesNotMatch(html, />7\.10s<\/span>/);
  assert.match(html, /lucide-triangle-alert/);
  assert.ok(html.indexOf('title="重试 1 次"') < html.indexOf('title="达到输出上限"'));
  assert.doesNotMatch(render({ ...fields, stopReason: 'completed' }), /lucide-triangle-alert/);
  assert.match(render({ ...fields, stopReason: 'refusal' }), /text-rose-700 dark:text-rose-300/);
});

test('进行中首个事件即定格首响，内容事件不会改胶囊或跑秒锚点', () => {
  const fields = { status: 'processing', isStreaming: true, firstTokenMs: 100, completionMs: null,
    timestamp: '2026-10-30T00:00:00.000000Z', firstTokenStartAt: '2026-10-30T00:00:00.100000Z' };
  assert.match(render(fields), />100ms<\/span>/);
  assert.match(render({ ...fields, firstContentAt: '2026-10-30T00:00:01.000000Z' }), />100ms<\/span>/);
  assert.doesNotMatch(render({ ...fields, firstContentMs: 1000 }), />900ms<\/span>/);
});

test('复审图像SSE：首个事件定格首响，生成位接过跑秒，完成后保留旧口径', () => {
  const image = { status: 'processing', requestFamily: 'image', isStreaming: true,
    duration: 0, firstTokenMs: 100, completionMs: null,
    timestamp: '2026-10-30T00:00:00.000000Z',
    firstTokenStartAt: '2026-10-30T00:00:00.100000Z',
    firstTokenAt: '2026-10-30T00:00:00.200000Z' };
  const running = render(image);
  assert.match(running, />100ms<\/span>/);
  assert.match(running, /tone-slate[^>]*>0ms<\/span>/);
  assert.doesNotMatch(running, /首字：写完到首个内容/);
  const completed = render({ ...image, completionMs: 900 });
  assert.match(completed, />100ms<\/span>/);
  assert.match(completed, />900ms<\/span>/);
  assert.doesNotMatch(completed, />-<\/span>/);
});

test('复审无内容流：完成事件先于状态查询，两个胶囊立即按旧口径定格', () => {
  for (const requestFamily of ['claude', 'codex']) {
    const completed = render({ status: 'processing', requestFamily, isStreaming: true,
      duration: 0, firstTokenMs: 100, completionMs: 900, firstContentMs: null, firstContentAt: '' });
    assert.match(completed, />100ms<\/span>/);
    assert.match(completed, />900ms<\/span>/);
    assert.doesNotMatch(completed, />-<\/span>/);
  }
});
