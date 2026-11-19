import test from 'node:test';
import assert from 'node:assert/strict';
import { createRequire } from 'node:module';
import { fileURLToPath, pathToFileURL } from 'node:url';
import { build } from 'esbuild';
import { createElement } from 'react';
import { renderToStaticMarkup } from 'react-dom/server';

const require = createRequire(import.meta.url);
const fromSource = (path) => fileURLToPath(new URL(`../../../${path}`, import.meta.url));
const compiled = await build({
  entryPoints: [fileURLToPath(new URL('./RequestBilling.jsx', import.meta.url))],
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
    name: 'shared-react',
    setup(builder) {
      builder.onResolve({ filter: /^(react(?:\/.*)?|lucide-react)$/ }, ({ path }) => ({
        path: pathToFileURL(require.resolve(path)).href,
        external: true
      }));
    }
  }]
});
const { default: RequestBilling } = await import(
  `data:text/javascript;base64,${Buffer.from(compiled.outputFiles[0].text).toString('base64')}`
);
const render = (props) => renderToStaticMarkup(createElement(RequestBilling, { id: 'r1', usageKnown: true, ...props }));
const decision = { speed: 'standard', channel_tier: 0, full_input: 500, model_multiplier: 1 };

test('渠道缺价和官方复算结果相互独立', () => {
  const html = render({
    version: 7,
    decision: { ...decision, missing: '缺少渠道费率行' },
    officialRecalculation: { available: true }
  });
  assert.match(html, /缺少渠道费率行/);
  assert.match(html, /<button\b[^>]*aria-expanded="false"[^>]*>按当时官方价复算<\/button>/);
  assert.doesNotMatch(html, /role="status"|收起复算结果/);
  assert.doesNotMatch(html, /官方参考费用不可复算/);

  const unavailable = render({
    version: 7,
    decision,
    officialRecalculation: { available: false, missing: '缺少历史官方费率行' }
  });
  assert.match(unavailable, /官方参考费用不可复算 · 缺少历史官方费率行/);
  assert.doesNotMatch(unavailable, /<button\b/);
});

test('历史基础价可没有选行依据，版本号本身不能决定可复算性', () => {
  assert.match(render({ version: 1, officialRecalculation: { available: true } }), /按当时官方价复算/);
  assert.doesNotMatch(render({ version: 1, decision }), /复算|渠道倍率/);
  assert.equal(render({}), '');
  assert.match(render({ pending: true, officialRecalculation: { available: true } }), /等待请求结算/);
});

test('展示当次统一或适用的分项倍率，模型倍率仍有单独的字段', () => {
  const unified = render({
    decision: { ...decision, model_multiplier: 1.5 },
    billingMultipliers: { unified: 0.8 }
  });
  assert.match(unified, /渠道倍率.*统一 ×0\.8/);
  assert.match(unified, /模型规则.*×1\.5/);

  const split = render({
    billingMultipliers: { input: 1, output: 0.5, cache_read: 0.25, cache_creation_30m: 0.8, cache_creation_12h: 0.8 }
  });
  for (const text of ['输入 ×1', '输出 ×0.5', '缓存读取 ×0.25', '写入 30m ×0.8', '写入 12h ×0.8']) {
    assert.ok(split.includes(text), text);
  }
  assert.doesNotMatch(split, /统一|写入 5m|写入 1h|undefined/);
});

test('常规无 speed 结算不显未确定，未选档带缺失原因不误显基础档且防御缺失 full_input', () => {
  const plain = render({
    version: 3,
    decision: { channel_row: 1, channel_tier: 0, full_input: 500, model_multiplier: 1 }
  });
  assert.match(plain, /全天 · 基础档/);
  assert.doesNotMatch(plain, /未确定/);

  const missingRow = render({
    version: 3,
    decision: { channel_row: 0, channel_tier: 0, missing: '缺少上游写出时间', model_multiplier: 1 }
  });
  assert.match(missingRow, /未判定档位/);
  assert.doesNotMatch(missingRow, /全天 · 基础档/);
  assert.doesNotMatch(missingRow, /NaN/);
});
