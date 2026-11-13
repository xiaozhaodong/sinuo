import test from 'node:test';
import assert from 'node:assert/strict';
import { createRequire } from 'node:module';
import { fileURLToPath, pathToFileURL } from 'node:url';
import { build } from 'esbuild';
import { createElement } from 'react';
import { renderToStaticMarkup } from 'react-dom/server';
import { normalizePrivacyWarnings, privacyWarningGroups } from './privacyWarnings.js';
import { normalizeRequest } from './normalizeRequest.js';

test('固定异常码去重和排序，阶段文案区分请求原文与响应占位符', () => {
  assert.deepEqual(normalizePrivacyWarnings(['response_bypassed', 'request_fail_open', 'SECRET', 'request_fail_open']), ['request_fail_open', 'response_bypassed']);
  const groups = privacyWarningGroups(['request_fail_open', 'response_bypassed']);
  assert.equal(groups.length, 2);
  assert.match(groups[0].message, /请求.*原文放行/);
  assert.match(groups[1].message, /响应.*占位符/);
  assert.doesNotMatch(groups[1].message, /请求.*发送|泄露/);
});

const require = createRequire(import.meta.url);
const loadComponent = async (name) => {
  const compiled = await build({
    entryPoints: [fileURLToPath(new URL(`../components/${name}.jsx`, import.meta.url))],
    bundle: true, write: false, platform: 'node', format: 'esm', jsx: 'automatic',
    alias: {
      '@components': fileURLToPath(new URL('../../../components', import.meta.url)),
      '@utils': fileURLToPath(new URL('../../../utils', import.meta.url))
    },
    plugins: [{ name: 'privacy-ui-test', setup(builder) {
      builder.onResolve({ filter: /^(react(?:\/.*)?|react-dom(?:\/.*)?|lucide-react)$/ }, ({ path }) => ({ path: pathToFileURL(require.resolve(path)).href, external: true }));
      builder.onResolve({ filter: /^@contexts\/TimezoneContext\.jsx$/ }, () => ({ path: 'timezone', namespace: 'test-env' }));
      builder.onResolve({ filter: /^@utils\/api\.js$/ }, () => ({ path: 'api', namespace: 'test-env' }));
      builder.onLoad({ filter: /.*/, namespace: 'test-env' }, ({ path }) => ({ contents: path === 'timezone'
        ? 'export const useTimezone = () => ({formatListTimestamp: value => value});'
        : 'export const formatCost = value => String(value);' }));
    } }]
  });
  return (await import(`data:text/javascript;base64,${Buffer.from(compiled.outputFiles[0].text).toString('base64')}`)).default;
};
const RequestsTable = await loadComponent('RequestsTable');
const LifecyclePanel = await loadComponent('LifecyclePanel');

test('列表只显示一个小图标，hover 包含两类异常，详情保留说明且正常行无标记', () => {
  const raw = { request_id: 'notice-row', status: 'completed', duration_ms: 1000,
    privacy_warnings: ['request_fail_open', 'response_bypassed'] };
  const request = normalizeRequest(raw);
  const table = (row) => renderToStaticMarkup(createElement(RequestsTable, {
    requests: [row], visibleColumns: ['status'], columnConfigs: [{ id: 'status', label: '状态' }]
  }));
  const html = table(request);
  assert.match(html, /已完成/);
  assert.match(html, /data-privacy-warning="request response"/);
  assert.equal((html.match(/data-privacy-warning=/g) || []).length, 1);
  assert.equal((html.match(/lucide-shield-alert/g) || []).length, 1);
  assert.match(html, /width="13" height="13"/);
  assert.match(html, /title="请求脱敏失败 · 原文放行/);
  assert.match(html, /响应回填未完成 · 原样放行/);
  assert.doesNotMatch(html, />脱敏异常<|>回填异常</);
  const detail = renderToStaticMarkup(createElement(LifecyclePanel, { request,
    lifecycle: { privacyScan: { action: 'redact', changed: true, restore: { status: 'completed' } } }
  }));
  assert.match(detail, /异常记录/);
  assert.match(detail, /请求隐私处理失败/);
  assert.match(detail, /可能包含占位符/);
  assert.doesNotMatch(table(normalizeRequest({ ...raw, privacy_warnings: [] })), /data-privacy-warning/);
});
