import test from 'node:test';
import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

const here = dirname(fileURLToPath(import.meta.url));
const pageSourcePath = join(here, 'index.jsx');

test('Codex 订阅代理配置移出设置页并保留明确跳转入口', async () => {
  const source = await readFile(pageSourcePath, 'utf8');

  assert.doesNotMatch(source, /<CodexSubscriptionProxyCard\s*\/>/);
  assert.match(source, /onNavigateToCodexAccess/);
  assert.match(source, /前往客户端接入/);
});

test('专用配置不进入通用设置表单', async () => {
  const source = await readFile(pageSourcePath, 'utf8');

  assert.match(source, /SUBSCRIPTION_PROXY_CONFIG_CATEGORY/);
  assert.match(source, /filter\(\(item\) => item\.category !== SUBSCRIPTION_PROXY_CONFIG_CATEGORY\)/);
});
