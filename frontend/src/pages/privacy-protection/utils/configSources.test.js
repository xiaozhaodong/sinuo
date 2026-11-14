import test from 'node:test';
import assert from 'node:assert/strict';
import { configSourcePayload, configSourceStatus, fetchConfigSources } from './configSources.js';

test('配置来源写入只提交可编辑字段，保留显式暂停状态', () => {
  assert.deepEqual(configSourcePayload({ id: 7, name: ' 工作项目 ', path: ' ~/.env ', kind: 'file', patterns: '*.json', enabled: false, last_error: 'stale', content_hash: 'private' }), {
    id: 7, name: '工作项目', path: '~/.env', kind: 'file', patterns: '', enabled: false
  });
  assert.equal(configSourcePayload({ kind: 'directory', path: '/tmp', patterns: ' .env,*.yaml ', enabled: true }).patterns, '.env,*.yaml');
});

test('状态区分未同步、失败和暂停，不把失败伪装成零命中；未收录数量不进状态', () => {
  assert.equal(configSourceStatus({ enabled: true }).label, '等待同步');
  assert.equal(configSourceStatus({ enabled: true, last_success_at: 'before', last_error: 'read failed' }).label, '同步失败');
  assert.equal(configSourceStatus({ enabled: false, last_error: 'read failed' }).label, '已暂停');
  assert.equal(configSourceStatus({ enabled: true, last_success_at: 'before', review_count: 2, excluded_count: 1 }).label, '已同步');
  assert.equal(configSourceStatus({ enabled: true, last_success_at: 'before' }).label, '已同步');
});

test('普通浏览器不把登记路径发给 HTTP 服务', async () => {
  await assert.rejects(fetchConfigSources(), /仅在 Sinuo 桌面端可用/);
});
