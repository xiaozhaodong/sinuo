import test from 'node:test';
import assert from 'node:assert/strict';

import {
  ADVANCED_RULE_PRIORITY_BASE,
  buildReorderPayload,
  createEmptyExactSecretForm,
  duplicateRuleForm,
  exactSecretCategoryLabel,
  exactSecretMinLength,
  filterPrivacyExactSecrets,
  filterPrivacyRules,
  formatScanBytes,
  PRIVACY_PATH_OPTIONS,
  PRIVACY_UPSTREAM_TYPE_OPTIONS,
  moveRuleInList,
  sourceLabel,
  summarizeScope,
  validateExactSecretForm,
  validatePrivacyRuleForm
} from './privacyRules.js';

test('formatScanBytes renders human readable sizes', () => {
  assert.equal(formatScanBytes(4194304), '4 MB');
  assert.equal(formatScanBytes(1536), '1.5 KB');
  assert.equal(formatScanBytes(512), '512 B');
  assert.equal(formatScanBytes(0), '-');
  assert.equal(formatScanBytes('oops'), '-');
});

test('订阅代理可选路径与上游类型进入 scope 选项', () => {
  assert.ok(PRIVACY_PATH_OPTIONS.includes('/v1/alpha/search'));
  assert.deepEqual(
    PRIVACY_UPSTREAM_TYPE_OPTIONS.find((option) => option.value === 'subscription'),
    { value: 'subscription', label: 'Codex 订阅直通' }
  );
});

test('summarizeScope shows 全部请求 for empty scope and joins dimensions', () => {
  assert.equal(summarizeScope({}), '全部请求');
  assert.equal(summarizeScope({ paths: [], upstream_types: [] }), '全部请求');

  const summary = summarizeScope({
    paths: ['/v1/messages', '/v1/responses'],
    upstream_types: ['account'],
    account_ids: [7]
  });
  assert.match(summary, /路径 2/);
  assert.match(summary, /Codex 账号/);
  assert.match(summary, /账号 1/);
});

test('filterPrivacyRules applies keyword and facet filters', () => {
  const rules = [
    { id: 1, name: 'OpenAI Key', description: '', pattern: 'sk-.+', enabled: true, match_type: 'regex', action: 'redact', source: 'preset' },
    { id: 2, name: '项目代号', description: '内部', pattern: 'Phoenix', enabled: false, match_type: 'literal', action: 'detect', source: 'custom' }
  ];

  assert.equal(filterPrivacyRules(rules, { keyword: 'phoenix' }).length, 1);
  assert.equal(filterPrivacyRules(rules, { keyword: 'phoenix' })[0].id, 2);
  assert.equal(filterPrivacyRules(rules, { enabled: 'enabled' }).length, 1);
  assert.equal(filterPrivacyRules(rules, { enabled: 'disabled' })[0].id, 2);
  assert.equal(filterPrivacyRules(rules, { matchType: 'regex' })[0].id, 1);
  assert.equal(filterPrivacyRules(rules, { action: 'detect' })[0].id, 2);
  assert.equal(filterPrivacyRules(rules, { source: 'preset' })[0].id, 1);
  assert.equal(filterPrivacyRules(rules, {}).length, 2);
});

test('validatePrivacyRuleForm enforces required fields', () => {
  const valid = validatePrivacyRuleForm({
    name: 'A', pattern: 'x', action: 'redact', placeholder: '[x]', priority: 100
  });
  assert.deepEqual(valid, {});

  const errors = validatePrivacyRuleForm({
    name: ' ', pattern: '', action: 'redact', placeholder: '', priority: -1
  });
  assert.ok(errors.name);
  assert.ok(errors.pattern);
  assert.ok(errors.placeholder);
  assert.ok(errors.priority);

  // detect 动作不强制占位符
  const detect = validatePrivacyRuleForm({
    name: 'B', pattern: 'y', action: 'detect', placeholder: '', priority: 0
  });
  assert.equal(detect.placeholder, undefined);
});

test('duplicateRuleForm clears id and appends 副本', () => {
  const copy = duplicateRuleForm({
    id: 9, name: 'OpenAI Key', pattern: 'sk-.+', source: 'preset',
    scope: { paths: ['/v1/messages'] }
  });
  assert.equal(copy.id, 0);
  assert.equal(copy.name, 'OpenAI Key 副本');
  assert.equal(copy.source, 'custom');
  assert.deepEqual(copy.scope.paths, ['/v1/messages']);
  assert.deepEqual(copy.scope.endpoint_names, []);
});

test('moveRuleInList swaps neighbors and keeps bounds', () => {
  const rules = [{ id: 1 }, { id: 2 }, { id: 3 }];
  assert.deepEqual(moveRuleInList(rules, 2, -1).map((r) => r.id), [2, 1, 3]);
  assert.deepEqual(moveRuleInList(rules, 3, 1).map((r) => r.id), [1, 2, 3]);
  assert.deepEqual(moveRuleInList(rules, 1, -1).map((r) => r.id), [1, 2, 3]);
  assert.deepEqual(moveRuleInList(rules, 99, 1).map((r) => r.id), [1, 2, 3]);
});

test('buildReorderPayload keeps advanced rules after builtin PII by default', () => {
  const payload = buildReorderPayload([{ id: 5 }, { id: 3 }, { id: 8 }]);
  assert.deepEqual(payload, [
    { id: 5, priority: ADVANCED_RULE_PRIORITY_BASE },
    { id: 3, priority: ADVANCED_RULE_PRIORITY_BASE + 10 },
    { id: 8, priority: ADVANCED_RULE_PRIORITY_BASE + 20 }
  ]);
  assert.deepEqual(buildReorderPayload([{ id: 1 }, { id: 2 }], { start: 200, step: 5 }), [
    { id: 1, priority: 200 },
    { id: 2, priority: 205 }
  ]);
});

test('exact secret helpers enforce category labels and minimum lengths', () => {
  assert.equal(exactSecretCategoryLabel('api_key'), 'API Key');
  assert.equal(exactSecretCategoryLabel('missing'), 'missing');
  assert.equal(exactSecretMinLength('token'), 12);
  assert.equal(exactSecretMinLength('password'), 8);
  assert.equal(exactSecretMinLength('custom'), 4);

  const empty = createEmptyExactSecretForm();
  assert.equal(empty.enabled, true);
  assert.equal(empty.category, 'custom');
  assert.equal(empty.source_type, 'manual');
});

test('validateExactSecretForm allows metadata-only edits but validates creates', () => {
  const createErrors = validateExactSecretForm({
    name: ' ',
    secret_value: 'abc',
    category: 'token',
    placeholder: ''
  });
  assert.ok(createErrors.name);
  assert.ok(createErrors.secret_value);
  assert.ok(createErrors.placeholder);

  const editErrors = validateExactSecretForm({
    id: 1,
    name: '生产 Token',
    secret_value: '',
    category: 'token',
    placeholder: '[Token]'
  }, { requireSecretValue: false });
  assert.deepEqual(editErrors, {});
});

test('filterPrivacyExactSecrets applies keyword, category and status filters', () => {
  const secrets = [
    {
      id: 1,
      enabled: true,
      name: '生产 OpenAI Key',
      description: '主账号',
      category: 'api_key',
      placeholder: '[API密钥]',
      masked_value: 'sk-pro…abcd',
      value_hash_short: 'aaaabbbb',
      source_type: 'endpoint_token',
      source_ref: '3'
    },
    {
      id: 2,
      enabled: false,
      name: '内部 Token',
      description: '',
      category: 'token',
      placeholder: '[Token]',
      masked_value: 'to…en',
      value_hash_short: 'ccccdddd',
      source_type: 'manual',
      source_ref: ''
    }
  ];

  assert.deepEqual(filterPrivacyExactSecrets(secrets, { keyword: 'openai' }).map((item) => item.id), [1]);
  assert.deepEqual(filterPrivacyExactSecrets(secrets, { category: 'token' }).map((item) => item.id), [2]);
  assert.deepEqual(filterPrivacyExactSecrets(secrets, { enabled: 'enabled' }).map((item) => item.id), [1]);
  assert.deepEqual(filterPrivacyExactSecrets(secrets, { enabled: 'disabled' }).map((item) => item.id), [2]);
  assert.equal(filterPrivacyExactSecrets(secrets, {}).length, 2);
});

test('sourceLabel explains hit sources', () => {
  assert.equal(sourceLabel('exact'), '本地敏感值');
  assert.equal(sourceLabel('builtin'), '内置规则');
  assert.equal(sourceLabel('preset'), '高级预设');
  assert.equal(sourceLabel('custom'), '自定义');
});
