import test from 'node:test';
import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

const here = dirname(fileURLToPath(import.meta.url));
const pageSourcePath = join(here, 'index.jsx');

test('Codex / Claude Code 接入配置移出设置页并保留明确跳转入口', async () => {
  const source = await readFile(pageSourcePath, 'utf8');

  assert.doesNotMatch(source, /<CodexSubscriptionProxyCard\s*\/>/);
  assert.match(source, /onNavigate=\{onNavigateToCodexAccess\}/);
  assert.match(source, /onNavigate=\{onNavigateToClaudeAccess\}/);
  assert.doesNotMatch(source, /<ClaudeCodeEntryCard\s*\/>/);
});

test('专用配置不进入通用设置表单', async () => {
  const source = await readFile(pageSourcePath, 'utf8');

  assert.match(source, /SUBSCRIPTION_PROXY_CONFIG_CATEGORY/);
  assert.match(source, /filter\(\(item\) => item\.category !== SUBSCRIPTION_PROXY_CONFIG_CATEGORY\)/);
});

test('图像生成分类和设置项移出设置页，不再保留图像分类图标', async () => {
  const source = await readFile(pageSourcePath, 'utf8');

  assert.match(source, /import \{ IMAGE_GENERATION_CATEGORY \} from '@pages\/codex\/images\/imageSettingsView\.js'/);
  assert.match(source, /setCategories\(\(categoriesData \|\| \[\]\)\.filter\(\(category\) => category\.name !== IMAGE_GENERATION_CATEGORY\)\)/);
  assert.match(source, /filter\(\(item\) => item\.category !== SUBSCRIPTION_PROXY_CONFIG_CATEGORY\)\s*\.filter\(\(item\) => item\.category !== IMAGE_GENERATION_CATEGORY\)/);
  assert.doesNotMatch(source, /image_generation: ImageIcon|\bImageIcon\b/);
});
