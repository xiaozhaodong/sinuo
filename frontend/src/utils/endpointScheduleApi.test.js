import test from 'node:test';
import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';

import { toEndpointScheduleSnapshot } from './endpointScheduleApi.js';

test('端点调度快照兼容 snake_case 字段与决策列表', () => {
  const snapshot = toEndpointScheduleSnapshot({
    has_snapshot: true,
    request_id: 'req-1',
    selected_endpoint: 'duck',
    final_outcome: 'success',
    decisions: [{ name: 'duck', decision: 'candidate', runtime_outcome: 'success', available_at: '2026-10-10T00:00:00Z' }]
  });

  assert.equal(snapshot.hasSnapshot, true);
  assert.equal(snapshot.requestId, 'req-1');
  assert.equal(snapshot.selectedEndpoint, 'duck');
  assert.equal(snapshot.finalOutcome, 'success');
  assert.deepEqual(snapshot.decisions, [{
    name: 'duck', decision: 'candidate', reason: '', availableAt: '2026-10-10T00:00:00Z', runtimeOutcome: 'success', runtimeError: ''
  }]);
  assert.equal(snapshot.unsupported, undefined);
});

test('后端返回 null 时视为暂无快照而不是报错', () => {
  const snapshot = toEndpointScheduleSnapshot(null);
  assert.equal(snapshot.hasSnapshot, false);
  assert.deepEqual(snapshot.decisions, []);
  assert.equal(snapshot.unsupported, undefined);
});

test('后端未提供该方法时给出不支持说明', () => {
  const snapshot = toEndpointScheduleSnapshot({ unsupported: true });
  assert.equal(snapshot.unsupported, true);
  assert.equal(snapshot.hasSnapshot, false);
  assert.match(snapshot.message, /尚未提供端点调度快照/);
});

test('端点调度快照走 Wails v3 绑定，不再读取 v2 的 window.go 全局', async () => {
  const [api, wails] = await Promise.all([
    readFile(new URL('./endpointScheduleApi.js', import.meta.url), 'utf8'),
    readFile(new URL('./wailsApi.js', import.meta.url), 'utf8')
  ]);
  assert.doesNotMatch(api, /window\?*\.go/);
  assert.match(api, /await getLatestEndpointScheduleSnapshot\(\)/);
  assert.match(wails, /getWailsMethod\('GetLatestEndpointScheduleSnapshot', \{ optional: true \}\)/);
});
