import test from 'node:test';
import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

const here = dirname(fileURLToPath(import.meta.url));
const pageSourcePath = join(here, 'index.jsx');
const cardSourcePath = join(here, 'components', 'CodexSubscriptionProxyCard.jsx');

test('Codex 订阅代理卡片只挂载一次，不随通用设置的加载/失败分支重建', async () => {
  const source = await readFile(pageSourcePath, 'utf8');

  // 曾把卡片同时写进「错误/加载」提前返回和正常返回两处：两个分支位置不同，
  // 通用设置一刷新就会重建卡片，未复制的接入地址与未保存输入随之丢失。
  const mounts = source.match(/<CodexSubscriptionProxyCard\s*\/>/g) || [];
  assert.equal(mounts.length, 1);

  // 通用设置的错误/加载只影响卡片之后的区块
  const cardIndex = source.indexOf('<CodexSubscriptionProxyCard />');
  assert.ok(cardIndex > -1);
  assert.ok(source.indexOf('title="设置加载失败"') > cardIndex);
  assert.ok(source.indexOf('text="加载设置..."') > cardIndex);
});

test('专用配置不进入通用设置表单', async () => {
  const source = await readFile(pageSourcePath, 'utf8');

  assert.match(source, /SUBSCRIPTION_PROXY_CONFIG_CATEGORY/);
  assert.match(source, /filter\(\(item\) => item\.category !== SUBSCRIPTION_PROXY_CONFIG_CATEGORY\)/);
});

test('接入地址随监听地址重建，不长期停留在旧端口', async () => {
  const source = await readFile(cardSourcePath, 'utf8');

  // 普通保存不回传明文，必须保留内存里的密钥才能重建地址
  assert.match(source, /buildSubscriptionProxyAccessURL/);
  assert.match(source, /secret:/);
  assert.match(source, /listenAddressKey\(config\)/);
});

test('隐私读取失败只降级隐私区块，不阻断代理控制', async () => {
  const source = await readFile(cardSourcePath, 'utf8');

  // 运行态与隐私各自独立读取，不能和配置读放在同一个 Promise.all 里
  assert.doesNotMatch(source, /Promise\.all\(\[\s*getCodexSubscriptionProxyConfig\(\)[\s\S]{0,200}getPrivacySettings\(\)/);
  assert.match(source, /const loadPrivacy = useCallback/);
  assert.match(source, /setPrivacyError/);
});
