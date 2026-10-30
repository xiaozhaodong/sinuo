import test from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { createRequire } from 'node:module';
import { fileURLToPath, pathToFileURL } from 'node:url';
import { build } from 'esbuild';

// 在内存编译真实组件，并导出内部数据组装函数供测试，不扩大生产接口。
const require = createRequire(import.meta.url);
const source = readFileSync(new URL('./RequestTimingCell.jsx', import.meta.url), 'utf8');
const compiled = await build({
  stdin: { contents: `${source}\nexport { timingCellData };`, loader: 'jsx',
    resolveDir: fileURLToPath(new URL('.', import.meta.url)) },
  bundle: true, write: false, platform: 'node', format: 'esm', jsx: 'automatic',
  plugins: [{
    name: 'shared-react',
    setup(builder) {
      builder.onResolve({ filter: /^(react(?:\/.*)?|react-dom(?:\/.*)?|lucide-react)$/ }, ({ path }) => ({
        path: pathToFileURL(require.resolve(path)).href, external: true
      }));
    }
  }]
});
const { timingCellData } = await import(`data:text/javascript;base64,${Buffer.from(compiled.outputFiles[0].text).toString('base64')}`);
const row = (extra = {}) => ({
  status: 'completed', isStreaming: true, duration: 20000, upstreamWriteMs: 25 + 958,
  firstTokenMs: 3680, firstContentMs: 25 + 958 + 3680 + 3420, completionMs: 14720, ...extra
});
const items = (request) => timingCellData(request, false, request.firstTokenMs).items;

test('新数据悬停首项为首字，两个胶囊恢复首响与生成口径', () => {
  const tooltip = items(row());
  assert.deepEqual(tooltip.slice(0, 2), [
    { label: '首字', value: '7.10s' }, { label: '总耗时', value: '20.0s' }
  ]);
  assert.equal(timingCellData(row(), false, 3680).first, 3680);
  assert.equal(timingCellData(row(), false, 3680).completion, 14720);
  assert.deepEqual(items(row({ firstContentMs: 983 }))[0], { label: '首字', value: '0ms' });
  assert.deepEqual(items(row({ firstContentMs: 1069 }))[0], { label: '首字', value: '86ms' });
  const warning = row({ retryCount: 1, upstreamFirstDispatchMs: 0, stopReason: 'max_tokens' });
  assert.deepEqual(items(warning).map((item) => item.label), ['首字', '总耗时', '速度', '重试', '结束']);
});

test('旧数据、图像和非流式悬停提示没有首字，保留TPS', () => {
  for (const fields of [{ firstContentMs: null }, { requestFamily: 'image' }, { isStreaming: false }]) {
    const tooltip = items(row(fields));
    assert.deepEqual(tooltip.map((item) => item.label), ['总耗时', 'TPS']);
  }
});

test('缺失首响不影响已知首字，首内容实时事件先到时也能计算', () => {
  for (const firstTokenMs of [null, undefined, NaN]) {
    const tooltip = items(row({ firstTokenMs }));
    assert.deepEqual(tooltip[0], { label: '首字', value: '7.10s' });
    assert.ok(!tooltip.some((item) => item.label === '首响'));
  }
  const live = row({ firstContentMs: null, upstreamWriteMs: null,
    firstTokenStartAt: '2026-10-30T00:00:00.983Z', firstContentAt: '2026-10-30T00:00:08.083Z' });
  assert.deepEqual(items(live)[0], { label: '首字', value: '7.10s' });
});
