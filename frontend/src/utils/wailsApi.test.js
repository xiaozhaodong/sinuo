import test from 'node:test';
import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

import {
  buildEndpointRecordPayload,
  buildUpstreamAccountPayload,
  isWailsEnvironment,
  mapEndpointRecord,
  mapUsageStats,
  normalizeUpstreamAccount,
  wrapWailsBindings
} from './wailsApi.js';

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const sourcePath = path.resolve(__dirname, './wailsApi.js');

test('Wails 环境识别覆盖 v3 的自定义协议与 Windows host', () => {
  const originalWindow = globalThis.window;
  try {
    globalThis.window = { location: { protocol: 'wails:', hostname: 'wails' } };
    assert.equal(isWailsEnvironment(), true);

    globalThis.window = { location: { protocol: 'http:', hostname: 'wails.localhost' } };
    assert.equal(isWailsEnvironment(), true);

    globalThis.window = { location: { protocol: 'http:', hostname: 'localhost' } };
    assert.equal(isWailsEnvironment(), false);
  } finally {
    if (originalWindow === undefined) {
      delete globalThis.window;
    } else {
      globalThis.window = originalWindow;
    }
  }
});

test('请求统计保留独立中断计数，不从总请求数猜测缺失字段', () => {
  const mapped = mapUsageStats({
    total_requests: 5, failed_requests: 2, interrupted_requests: 1, success_rate: 20
  }, '1d');
  assert.equal(mapped.period, '1d');
  assert.equal(mapped.total_requests, 5);
  assert.equal(mapped.failed_requests, 2);
  assert.equal(mapped.interrupted_requests, 1);
  assert.equal(mapped.success_rate, 20);
  assert.equal(mapUsageStats({ interrupted_requests: 0 }).interrupted_requests, 0);
  for (const value of [undefined, null, '1', NaN]) {
    assert.equal(mapUsageStats({ total_requests: 100, interrupted_requests: value }).interrupted_requests, null);
  }
});

test('Codex metadata payload preserves optional booleans across aliases and repeated normalization', () => {
  const aliases = ['strip_codex_internal_metadata', 'stripCodexInternalMetadata', 'StripCodexInternalMetadata'];
  for (const alias of aliases) {
    for (const value of [true, false]) {
      const payload = buildUpstreamAccountPayload({ provider_type: 'chatgpt_refresh_token', [alias]: value });
      assert.equal(payload.strip_codex_internal_metadata, value);
      assert.equal(buildUpstreamAccountPayload(payload).strip_codex_internal_metadata, value);
      assert.equal('stripCodexInternalMetadata' in payload, false);
      assert.equal('StripCodexInternalMetadata' in payload, false);
    }
    for (const value of [undefined, null]) {
      const payload = buildUpstreamAccountPayload({ [alias]: value });
      assert.equal('strip_codex_internal_metadata' in payload, false);
      assert.equal('strip_codex_internal_metadata' in buildUpstreamAccountPayload(payload), false);
    }
    assert.throws(() => buildUpstreamAccountPayload({ [alias]: 'false' }), TypeError);
  }
  assert.equal('strip_codex_internal_metadata' in buildUpstreamAccountPayload({}), false);
});

test('Codex metadata account normalization mirrors aliases and defaults to false', () => {
  for (const alias of ['strip_codex_internal_metadata', 'stripCodexInternalMetadata', 'StripCodexInternalMetadata']) {
    for (const value of [true, false]) {
      const account = normalizeUpstreamAccount({ [alias]: value });
      assert.equal(account.strip_codex_internal_metadata, value);
      assert.equal(account.stripCodexInternalMetadata, value);
    }
  }
  assert.equal(normalizeUpstreamAccount({}).stripCodexInternalMetadata, false);
  assert.equal(normalizeUpstreamAccount({ strip_codex_internal_metadata: 'false' }).stripCodexInternalMetadata, false);
});

test('mapEndpointRecord preserves cacheCreationCostMultiplier1h from detail payload', () => {
  const result = mapEndpointRecord({
    id: 1,
    name: 'ep-cache-1h',
    cache_creation_cost_multiplier_1h: 1.75
  });

  assert.equal(result.cacheCreationCostMultiplier1h, 1.75);
});

test('mapEndpointRecord and endpoint payload preserve model rewrite rules', () => {
  const modelRewriteRules = '[{"paths":["/v1/messages"],"match":"exact","from":"source","to":"target"}]';
  const mapped = mapEndpointRecord({
    id: 2,
    name: 'cc-model-rewrite',
    model_rewrite_rules: modelRewriteRules
  });
  const payload = buildEndpointRecordPayload({
    name: 'cc-model-rewrite',
    modelRewriteRules: `  ${modelRewriteRules}  `
  });

  assert.equal(mapped.modelRewriteRules, modelRewriteRules);
  assert.equal(payload.model_rewrite_rules, modelRewriteRules);
  assert.equal(payload.name, 'cc-model-rewrite');
  assert.equal(buildEndpointRecordPayload({}, 'fallback-endpoint').name, 'fallback-endpoint');
});

test('endpoint availability defaults to true and preserves an explicit false', () => {
  assert.equal(mapEndpointRecord({}).availabilityEnabled, true);
  assert.equal(mapEndpointRecord({ availability_enabled: false }).availabilityEnabled, false);
  assert.equal(buildEndpointRecordPayload({}).availability_enabled, true);
  assert.equal(buildEndpointRecordPayload({ availabilityEnabled: false }).availability_enabled, false);
});

test('endpoint payload excludes legacy channel fields and serializes explicit secret clears', () => {
  const payload = buildEndpointRecordPayload({
    name: 'claude-primary',
    priority: 0,
    clearToken: true,
    clearApiKey: true
  });

  assert.equal(payload.priority, 0);
  assert.equal(payload.clear_token, true);
  assert.equal(payload.clear_api_key, true);
  assert.equal('channel' in payload, false);
  assert.equal('enabled' in payload, false);
});

test('normalizeUpstreamAccount keeps mirrored aliases in sync for mixed-case upstream payloads', () => {
  const result = normalizeUpstreamAccount({
    ID: '12',
    ProviderType: 'api_key',
    accountName: 'primary-key',
    isGroupPreferred: true,
    credentialRaw: 'sk-live',
    baseURL: 'https://example.com',
    modelRewriteRules: '[{"from":"gpt-5.4","to":"gpt-5.5"}]',
    EnableRequestCompression: true,
    costMultiplier: '1.5',
    input_cost_multiplier: '1.2',
    outputCostMultiplier: '2.4',
    cache_creation_cost_multiplier: '0',
    cacheCreationCostMultiplier1h: '3.5',
    cache_read_cost_multiplier: '0.8',
    groupKey: 'primary',
    Priority: '10',
    Enabled: true,
    State: 'active',
    cooldownUntil: '2026-03-25T00:00:00Z',
    failCount: 2,
    lastSuccessAt: '2026-03-24T12:00:00Z',
    lastError: 'none',
    planType: 'plus',
    ChatGPTAccountID: 'acct-1',
    chatgptUserId: 'user-2',
    OrganizationID: 'org-3',
    quota5hUsedPercent: '12.5',
    quota_weekly_used_percent: '22.5',
    quota5hResetAt: '2026-03-25T05:00:00Z',
    quota_weekly_reset_at: '2026-03-27T05:00:00Z',
    quotaStatus: 'ok',
    quotaRefreshedAt: '2026-03-24T13:00:00Z',
    Fingerprint: 'fp-1',
    createdAt: '2026-03-20T00:00:00Z',
    UpdatedAt: '2026-03-24T13:05:00Z'
  });

  assert.equal(result.id, 12);
  assert.equal(result.provider_type, 'api_key');
  assert.equal(result.providerType, 'api_key');
  assert.equal(result.account_name, 'primary-key');
  assert.equal(result.accountName, 'primary-key');
  assert.equal(result.is_group_preferred, true);
  assert.equal(result.isGroupPreferred, true);
  assert.equal(result.cost_multiplier, 1.5);
  assert.equal(result.costMultiplier, 1.5);
  assert.equal(result.input_cost_multiplier, 1.2);
  assert.equal(result.inputCostMultiplier, 1.2);
  assert.equal(result.output_cost_multiplier, 2.4);
  assert.equal(result.outputCostMultiplier, 2.4);
  assert.equal(result.cache_creation_cost_multiplier, 1);
  assert.equal(result.cacheCreationCostMultiplier, 1);
  assert.equal(result.cache_creation_cost_multiplier_1h, 3.5);
  assert.equal(result.cacheCreationCostMultiplier1h, 3.5);
  assert.equal(result.cache_read_cost_multiplier, 0.8);
  assert.equal(result.enable_request_compression, true);
  assert.equal(result.enableRequestCompression, true);
  assert.equal(result.cacheReadCostMultiplier, 0.8);
  assert.equal(result.model_rewrite_rules, '[{"from":"gpt-5.4","to":"gpt-5.5"}]');
  assert.equal(result.modelRewriteRules, '[{"from":"gpt-5.4","to":"gpt-5.5"}]');
  assert.equal(result.group_key, 'primary');
  assert.equal(result.groupKey, 'primary');
  assert.equal(result.priority, 10);
  assert.equal(result.enabled, true);
  assert.equal(result.state, 'active');
  assert.equal(result.cooldown_until, '2026-03-25T00:00:00Z');
  assert.equal(result.cooldownUntil, '2026-03-25T00:00:00Z');
  assert.equal(result.fail_count, 2);
  assert.equal(result.failCount, 2);
  assert.equal(result.plan_type, 'plus');
  assert.equal(result.planType, 'plus');
  assert.equal(result.chatgpt_account_id, 'acct-1');
  assert.equal(result.chatgptAccountId, 'acct-1');
  assert.equal(result.chatgpt_user_id, 'user-2');
  assert.equal(result.chatgptUserId, 'user-2');
  assert.equal(result.organization_id, 'org-3');
  assert.equal(result.organizationId, 'org-3');
  assert.equal(result.quota_5h_used_percent, 12.5);
  assert.equal(result.quota5hUsedPercent, 12.5);
  assert.equal(result.quota_weekly_used_percent, 22.5);
  assert.equal(result.quotaWeeklyUsedPercent, 22.5);
  assert.equal(result.quota_5h_reset_at, '2026-03-25T05:00:00Z');
  assert.equal(result.quota5hResetAt, '2026-03-25T05:00:00Z');
  assert.equal(result.quota_weekly_reset_at, '2026-03-27T05:00:00Z');
  assert.equal(result.quotaWeeklyResetAt, '2026-03-27T05:00:00Z');
  assert.equal(result.quota_status, 'ok');
  assert.equal(result.quotaStatus, 'ok');
  assert.equal(result.quota_refreshed_at, '2026-03-24T13:00:00Z');
  assert.equal(result.quotaRefreshedAt, '2026-03-24T13:00:00Z');
  assert.equal(result.fingerprint, 'fp-1');
  assert.equal(result.created_at, '2026-03-20T00:00:00Z');
  assert.equal(result.createdAt, '2026-03-20T00:00:00Z');
  assert.equal(result.updated_at, '2026-03-24T13:05:00Z');
  assert.equal(result.updatedAt, '2026-03-24T13:05:00Z');
});

test('buildUpstreamAccountPayload serializes editable model rewrite rule arrays', () => {
  const payload = buildUpstreamAccountPayload({
    provider_type: 'api_key',
    account_name: 'anyrouter',
    credential_raw: 'sk-anyrouter',
    modelRewriteRules: [
      { source: 'gpt-5.4', target: 'gpt-5.5' },
      { source: 'gpt-5.4-mini', target: 'gpt-5.5' }
    ]
  });

  assert.deepEqual(
    JSON.parse(payload.model_rewrite_rules).map((rule) => [rule.match, rule.from, rule.to]),
    [
      ['exact', 'gpt-5.4', 'gpt-5.5'],
      ['exact', 'gpt-5.4-mini', 'gpt-5.5']
    ]
  );
});

test('buildUpstreamAccountPayload keeps zstd compression opt-in for API Key and ChatGPT OAuth', () => {
  const apiKeyPayload = buildUpstreamAccountPayload({
    provider_type: 'api_key',
    enableRequestCompression: true
  });
  const oauthPayload = buildUpstreamAccountPayload({
    provider_type: 'chatgpt_refresh_token',
    enableRequestCompression: true
  });
  const sessionPayload = buildUpstreamAccountPayload({
    provider_type: 'session_cookie',
    enableRequestCompression: true
  });

  assert.equal(apiKeyPayload.enable_request_compression, true);
  assert.equal(oauthPayload.enable_request_compression, true);
  assert.equal(sessionPayload.enable_request_compression, false);
});

test('normalizeUpstreamAccount uses alias helpers instead of repeating inline field chains', async () => {
  const source = await readFile(sourcePath, 'utf8');

  assert.match(source, /const pickAccountValue = \(account,\s*keys,\s*fallback = ''\)/);
  assert.match(source, /const mirrorAliasedField = \(target,\s*snakeKey,\s*camelKey,\s*value\)/);
  assert.doesNotMatch(source, /provider_type:\s*account\.provider_type \|\| account\.providerType \|\| account\.ProviderType/);
});

test('parsePrivacyScope returns empty arrays for invalid or empty scope json', async () => {
  const { parsePrivacyScope } = await import('./wailsApi.js');

  const empty = parsePrivacyScope('');
  assert.deepEqual(empty.paths, []);
  assert.deepEqual(empty.account_ids, []);

  const invalid = parsePrivacyScope('{not-json');
  assert.deepEqual(invalid.upstream_types, []);

  const parsed = parsePrivacyScope('{"paths":["/v1/messages"],"account_ids":[7]}');
  assert.deepEqual(parsed.paths, ['/v1/messages']);
  assert.deepEqual(parsed.account_ids, [7]);
  assert.deepEqual(parsed.provider_types, []);
});

test('encodePrivacyScope omits empty dimensions and coerces account ids to numbers', async () => {
  const { encodePrivacyScope } = await import('./wailsApi.js');

  const json = encodePrivacyScope({
    paths: ['/v1/messages', '  '],
    upstream_types: [],
    account_ids: ['7', 'oops', 8]
  });
  const parsed = JSON.parse(json);
  assert.deepEqual(parsed.paths, ['/v1/messages']);
  assert.deepEqual(parsed.account_ids, [7, 8]);
  assert.equal('upstream_types' in parsed, false);
  assert.equal('provider_types' in parsed, false);
});

test('normalizePrivacyRule mirrors backend fields and parses scope', async () => {
  const { normalizePrivacyRule } = await import('./wailsApi.js');

  const rule = normalizePrivacyRule({
    id: '12',
    enabled: true,
    name: 'OpenAI Key',
    priority: '100',
    match_type: 'regex',
    pattern: 'sk-.+',
    placeholder: '[密钥]',
    action: 'redact',
    scope_json: '{"paths":["/v1/responses"]}',
    source: 'preset',
    compile_error: ''
  });

  assert.equal(rule.id, 12);
  assert.equal(rule.priority, 100);
  assert.equal(rule.action, 'redact');
  assert.deepEqual(rule.scope.paths, ['/v1/responses']);
  assert.deepEqual(rule.scope.endpoint_names, []);
});

test('buildPrivacyRulePayload prefers explicit scope_json and falls back to scope object', async () => {
  const { buildPrivacyRulePayload } = await import('./wailsApi.js');

  const fromScope = buildPrivacyRulePayload({
    enabled: true,
    name: '  规则A  ',
    priority: '50',
    matchType: 'LITERAL',
    pattern: 'secret',
    placeholder: '[x]',
    action: 'DETECT',
    scope: { paths: ['/v1/messages'] }
  });
  assert.equal(fromScope.name, '规则A');
  assert.equal(fromScope.priority, 50);
  assert.equal(fromScope.match_type, 'literal');
  assert.equal(fromScope.action, 'detect');
  assert.deepEqual(JSON.parse(fromScope.scope_json).paths, ['/v1/messages']);

  const fromJson = buildPrivacyRulePayload({
    name: 'B',
    pattern: 'x',
    scope_json: '{"upstream_types":["account"]}',
    scope: { paths: ['/ignored'] }
  });
  assert.deepEqual(JSON.parse(fromJson.scope_json).upstream_types, ['account']);
  assert.equal('paths' in JSON.parse(fromJson.scope_json), false);
});

test('normalizePrivacySettings applies safe defaults', async () => {
  const { normalizePrivacySettings } = await import('./wailsApi.js');

  const defaults = normalizePrivacySettings({});
  assert.equal(defaults.mode, 'disabled');
  assert.equal(defaults.scan_max_bytes, 4194304);
  assert.equal(defaults.over_limit_action, 'scan_prefix');
  assert.equal(defaults.on_error, 'fail_open');

  const fromBackend = normalizePrivacySettings({
    mode: 'redact',
    scan_max_bytes: 1024,
    over_limit_action: 'fail_closed',
    on_error: 'fail_closed',
    version: 3,
    status: 'degraded',
    enabled_rules: 5
  });
  assert.equal(fromBackend.mode, 'redact');
  assert.equal(fromBackend.scan_max_bytes, 1024);
  assert.equal(fromBackend.status, 'degraded');
  assert.equal(fromBackend.enabled_rules, 5);
});

test('normalizePrivacyExactSecret and import candidate do not require raw values', async () => {
  const {
    normalizePrivacyExactSecret,
    normalizePrivacyImportCandidate,
    buildPrivacyExactSecretPayload
  } = await import('./wailsApi.js');

  const secret = normalizePrivacyExactSecret({
    ID: '7',
    Enabled: true,
    Name: '生产 Key',
    Category: 'api_key',
    Placeholder: '[API密钥]',
    SourceType: 'endpoint_token',
    SourceRef: '3',
    MaskedValue: 'sk-pro…abcd',
    ValueLength: '42',
    ValueHashShort: 'abcdef12'
  });
  assert.equal(secret.id, 7);
  assert.equal(secret.enabled, true);
  assert.equal(secret.source_type, 'endpoint_token');
  assert.equal(secret.masked_value, 'sk-pro…abcd');
  assert.equal(secret.value_length, 42);
  assert.equal('secret_value' in secret, false);

  const candidate = normalizePrivacyImportCandidate({
    SourceType: 'upstream_account',
    SourceRef: '11',
    Name: '账号 Key',
    Category: 'api_key',
    AlreadyExists: true
  });
  assert.equal(candidate.source_type, 'upstream_account');
  assert.equal(candidate.already_exists, true);

  const payload = buildPrivacyExactSecretPayload({
    enabled: true,
    name: '  Token  ',
    secretValue: ' raw-token ',
    category: 'token',
    placeholder: '[Token]'
  });
  assert.equal(payload.name, 'Token');
  assert.equal(payload.secret_value, ' raw-token ');
  assert.equal(payload.source_type, 'manual');
});

test('独立失败策略贯通账号与端点映射并可恢复默认', () => {
  const policy = { soft_failure_window_minutes: 10, soft_failure_threshold: 5 };
  const account = normalizeUpstreamAccount(policy);
  const endpoint = mapEndpointRecord(policy);
  assert.equal(account.softFailureWindowMinutes, 10);
  assert.equal(account.softFailureThreshold, 5);
  assert.equal(endpoint.softFailureWindowMinutes, 10);
  assert.equal(endpoint.softFailureThreshold, 5);
  for (const build of [buildUpstreamAccountPayload, buildEndpointRecordPayload]) {
    const saved = build({ softFailureWindowMinutes: '10', softFailureThreshold: '5' });
    assert.equal(saved.soft_failure_window_minutes, 10);
    assert.equal(saved.soft_failure_threshold, 5);
    const restored = build({ ...saved, soft_failure_window_minutes: null, soft_failure_threshold: null, softFailureWindowMinutes: 10, softFailureThreshold: 5 });
    assert.equal(restored.soft_failure_window_minutes, null);
    assert.equal(restored.soft_failure_threshold, null);
    assert.equal(build(restored).soft_failure_window_minutes, null);
    assert.throws(() => build({ soft_failure_window_minutes: 5 }));
    assert.throws(() => build({ softFailureWindowMinutes: 5, softFailureThreshold: 1.5 }));
  }
});

test('账号策略归一化清除内部表单字段与旧别名', () => {
  const result = buildUpstreamAccountPayload({ softFailureCustom: true, SoftFailureWindowMinutes: 2, SoftFailureThreshold: 1 });
  assert.equal(result.soft_failure_window_minutes, 2);
  assert.equal(result.soft_failure_threshold, 1);
  for (const key of ['softFailureCustom', 'SoftFailureWindowMinutes', 'SoftFailureThreshold']) assert.equal(key in result, false);
});

test('绑定层把 Wails 三种 reject 形态统一归一化成 Error，同步抛错与非函数成员也照顾到', async () => {
  const wrapped = wrapWailsBindings({
    rejectString: async () => { throw 'error parsing arguments: invalid ID'; },
    rejectObject: async () => { throw { message: '账号仍被模型路由使用' }; },
    rejectError: async () => { throw new Error('网络超时'); },
    throwSync: () => { throw 'sync failure'; },
    ok: async (a, b) => a + b,
    version: '1.0'
  });

  await assert.rejects(wrapped.rejectString(), (err) => err instanceof Error && err.message === 'error parsing arguments: invalid ID');
  await assert.rejects(wrapped.rejectObject(), (err) => err instanceof Error && err.message === '账号仍被模型路由使用');
  const original = new Error('网络超时');
  const passthrough = wrapWailsBindings({ f: async () => { throw original; } });
  await assert.rejects(passthrough.f(), (err) => err === original);
  await assert.rejects(wrapped.throwSync(), (err) => err instanceof Error && err.message === 'sync failure');
  assert.equal(await wrapped.ok(1, 2), 3);
  assert.equal(wrapped.version, '1.0');
  assert.equal(wrapWailsBindings(null), null);
});
