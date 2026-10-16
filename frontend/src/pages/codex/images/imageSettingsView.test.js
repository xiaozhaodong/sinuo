import test from 'node:test';
import assert from 'node:assert/strict';
import {
  applySettingChange, buildImageSettingUpdates, groupImageSettings, IMAGE_GENERATION_CATEGORY, IMAGE_SETTING_GROUPS
} from './imageSettingsView.js';

const setting = (key, extra = {}) => ({ category: IMAGE_GENERATION_CATEGORY, key, value: '', ...extra });

test('图像设置按四组及组内 key 顺序展示，不依赖后端返回顺序', () => {
  const keys = ['enabled', 'endpoint_url', 'api_key', 'model', 'timeout', 'fixed_price_usd', 'direct_connect', 'direct_source_port_range'];
  const settings = keys.map((key, index) => setting(key, { label: `后端名称 ${key}`, description: '后端说明', display_order: index }));
  const input = settings.toReversed();
  const groups = groupImageSettings(input);

  assert.deepEqual(groups, IMAGE_SETTING_GROUPS.map(({ keys: groupKeys, ...group }) => ({
    ...group, items: groupKeys.map((key) => settings.find((item) => item.key === key))
  })));
  assert.deepEqual(groups.map((group) => group.id), ['takeover', 'upstream', 'billing', 'network']);
  assert.equal(groups[1].items[0], settings[1]);
  assert.deepEqual(input, settings.toReversed());
});

test('图像设置跳过缺失项和空分组，没有未知项时不输出其他组', () => {
  const endpoint = setting('endpoint_url');
  const timeout = setting('timeout');
  const groups = groupImageSettings([timeout, endpoint]);

  assert.deepEqual(groups.map((group) => group.id), ['upstream']);
  assert.deepEqual(groups[0].items, [endpoint, timeout]);
  assert.deepEqual(groupImageSettings([]), []);
});

test('未知图像设置进入末尾其他组并按 display_order 排序，不修改输入', () => {
  const later = setting('future_later', { display_order: 20 });
  const earlier = setting('future_earlier', { display_order: 10 });
  const enabled = setting('enabled');
  const input = Object.freeze([later, enabled, earlier]);
  const groups = groupImageSettings(input);

  assert.deepEqual(groups.map((group) => group.id), ['takeover', 'other']);
  assert.deepEqual(groups[1], { id: 'other', title: '其他', description: '', items: [earlier, later] });
  assert.deepEqual(input, [later, enabled, earlier]);
});

test('图像设置忽略其他分类的已知或未知 key，null 返回空数组', () => {
  assert.deepEqual(groupImageSettings([
    setting('enabled', { category: 'server' }),
    setting('future_option', { category: 'request' })
  ]), []);
  assert.deepEqual(groupImageSettings(null), []);
});

test('图像草稿写入新值且不修改原对象，保留其他草稿项', () => {
  const changes = Object.freeze({ timeout: '60s' });
  const next = applySettingChange(changes, setting('enabled', { value: 'false' }), true);

  assert.deepEqual(next, { timeout: '60s', enabled: true });
  assert.deepEqual(changes, { timeout: '60s' });
  assert.notEqual(next, changes);
});

test('图像草稿改回原值时删除 key，按字符串比较布尔及数字', () => {
  const changes = Object.freeze({ enabled: 'false', fixed_price_usd: '1', timeout: '60s' });
  const next = applySettingChange(changes, setting('enabled', { value: 'true' }), true);

  assert.deepEqual(next, { fixed_price_usd: '1', timeout: '60s' });
  assert.deepEqual(applySettingChange(next, setting('fixed_price_usd', { value: '0.04' }), 0.04), { timeout: '60s' });
  assert.deepEqual(changes, { enabled: 'false', fixed_price_usd: '1', timeout: '60s' });
});

test('API Key 输入后清空视为未修改，留空不生成清除请求', () => {
  const secret = setting('api_key', { secret_configured: true });
  const changed = applySettingChange({}, secret, 'test-only-key');
  const cleared = applySettingChange(changed, secret, '');

  assert.deepEqual(changed, { api_key: 'test-only-key' });
  assert.deepEqual(cleared, {});
  assert.deepEqual(buildImageSettingUpdates(cleared), []);
});

test('图像设置更新固定分类并将所有值转为字符串', () => {
  const changes = Object.freeze({ enabled: true, fixed_price_usd: 0.04, timeout: '300s' });

  assert.deepEqual(buildImageSettingUpdates(changes), [
    { category: IMAGE_GENERATION_CATEGORY, key: 'enabled', value: 'true' },
    { category: IMAGE_GENERATION_CATEGORY, key: 'fixed_price_usd', value: '0.04' },
    { category: IMAGE_GENERATION_CATEGORY, key: 'timeout', value: '300s' }
  ]);
  assert.deepEqual(buildImageSettingUpdates({}), []);
});
