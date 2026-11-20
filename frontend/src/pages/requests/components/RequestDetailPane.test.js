import test from 'node:test';
import assert from 'node:assert/strict';
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
    assert.ok(text.includes(`合计${total}`));
  }
});
