import test from 'node:test';
import assert from 'node:assert/strict';

import {
  buildManualSwitchSuccessMessage,
  buildCodexModelRewriteRules,
  createDefaultModelRewriteRules,
  isValidAccountId,
  parseCodexModelRewriteSettings,
  shouldAutoExtractCallback
} from './accountPool.js';

test('buildManualSwitchSuccessMessage avoids promising immediate effect', () => {
  const message = buildManualSwitchSuccessMessage('primary-a', 'primary');

  assert.ok(message.includes('当前可调度状态优先使用'));
  assert.ok(message.includes('恢复后会自动回切'));
  assert.ok(!message.includes('立即生效'));
});

test('buildManualSwitchSuccessMessage can include requests path hint', () => {
  const message = buildManualSwitchSuccessMessage('primary-a', 'primary', { includeRequestPath: true });

  assert.ok(message.includes('/v1/responses'));
  assert.ok(message.includes('当前可调度状态优先使用'));
});

test('isValidAccountId accepts normalized account ids and rejects empty values', () => {
  assert.equal(isValidAccountId(1), true);
  assert.equal(isValidAccountId('42'), true);
  assert.equal(isValidAccountId('acct-primary'), true);
  assert.equal(isValidAccountId('  '), false);
  assert.equal(isValidAccountId(''), false);
  assert.equal(isValidAccountId(null), false);
  assert.equal(isValidAccountId(undefined), false);
});

test('buildCodexModelRewriteRules serializes multiple exact rewrite rules', () => {
  const raw = buildCodexModelRewriteRules({
    rules: [
      { source: 'gpt-5.4', target: 'gpt-5.5' },
      { source: 'gpt-5.6', target: 'gpt-5.5' }
    ]
  });

  const rules = JSON.parse(raw);

  assert.equal(rules.length, 2);
  assert.deepEqual(
    rules.map((rule) => [rule.match, rule.from, rule.to]),
    [
      ['exact', 'gpt-5.4', 'gpt-5.5'],
      ['exact', 'gpt-5.6', 'gpt-5.5']
    ]
  );
});

test('createDefaultModelRewriteRules includes base and mini mappings', () => {
  assert.deepEqual(createDefaultModelRewriteRules(), [
    { source: 'gpt-5.4', target: 'gpt-5.5' },
    { source: 'gpt-5.4-mini', target: 'gpt-5.5' }
  ]);
});

test('parseCodexModelRewriteSettings restores multiple editable rules', () => {
  const raw = JSON.stringify([
    { paths: ['/v1/responses'], match: 'exact', from: 'gpt-5.4', to: 'gpt-5.5' },
    { paths: ['/v1/responses'], match: 'exact', from: 'gpt-5.6', to: 'gpt-5.5' }
  ]);

  const settings = parseCodexModelRewriteSettings(raw);

  assert.equal(settings.enabled, true);
  assert.deepEqual(settings.rules, [
    { source: 'gpt-5.4', target: 'gpt-5.5' },
    { source: 'gpt-5.6', target: 'gpt-5.5' }
  ]);
});

test('shouldAutoExtractCallback accepts callback URLs with code', () => {
  assert.equal(shouldAutoExtractCallback('http://localhost:1455/auth/callback?code=test-code&state=test-state'), true);
});

test('shouldAutoExtractCallback accepts callback URLs with rt', () => {
  assert.equal(shouldAutoExtractCallback('http://localhost:1455/auth/callback?rt=test-rt'), true);
  assert.equal(shouldAutoExtractCallback('http://localhost:1455/auth/callback?refresh_token=test-rt'), true);
});

test('shouldAutoExtractCallback accepts OAuth errors for immediate feedback', () => {
  assert.equal(shouldAutoExtractCallback('http://localhost:1455/auth/callback?error=access_denied'), true);
  assert.equal(shouldAutoExtractCallback('http://localhost:1455/auth/callback#error=access_denied'), true);
});

test('shouldAutoExtractCallback rejects ordinary URLs without callback parameters', () => {
  assert.equal(shouldAutoExtractCallback('https://example.com/login?state=test-state'), false);
});

test('shouldAutoExtractCallback rejects empty input', () => {
  assert.equal(shouldAutoExtractCallback(''), false);
});

test('shouldAutoExtractCallback rejects invalid text even with callback parameter fragments', () => {
  assert.equal(shouldAutoExtractCallback('not-a-url?code=test-code&rt=test-rt&error=access_denied'), false);
});
