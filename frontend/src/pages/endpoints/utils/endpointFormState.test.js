import test from 'node:test';
import assert from 'node:assert/strict';

import {
  buildEndpointFormPayload,
  createEndpointFormState,
  summarizeEndpointAuthentication,
  validateEndpointFormState
} from './endpointFormState.js';

test('editing endpoint never places stored secrets in form state', () => {
  const state = createEndpointFormState({
    name: 'claude-primary',
    url: 'https://api.example.com',
    tokenMasked: 'sk-••••',
    apiKeyMasked: 'key-••••',
    headers: { 'X-Tenant': 'tenant-a' }
  });

  assert.equal(state.token, '');
  assert.equal(state.apiKey, '');
  assert.equal(state.hasStoredToken, true);
  assert.equal(state.hasStoredApiKey, true);
  assert.deepEqual(state.headerRows, [{ name: 'X-Tenant', value: 'tenant-a' }]);
  assert.equal(summarizeEndpointAuthentication({ tokenMasked: 'x', apiKeyMasked: 'y', headers: { A: 'b' } }), 'Token + API Key + 1 Header');
});

test('secret removal is explicit and headers are serialized structurally', () => {
  const payload = buildEndpointFormPayload({
    ...createEndpointFormState(),
    name: 'claude-primary',
    url: 'https://api.example.com/v1',
    clearToken: true,
    headerRows: [{ name: ' X-Tenant ', value: ' tenant-a ' }, { name: '', value: '' }]
  });

  assert.equal(payload.clearToken, true);
  assert.equal(payload.token, '');
  assert.deepEqual(payload.headers, { 'X-Tenant': 'tenant-a' });
  assert.equal('channel' in payload, false);
  assert.equal('group' in payload, false);
});

test('URL and header validation reject malformed structured input', () => {
  assert.deepEqual(validateEndpointFormState({ name: '', url: 'javascript:alert(1)', headerRows: [] }), {
    name: '请输入端点名称',
    url: '请输入不含账号密码的 HTTP(S) URL'
  });
  assert.equal(validateEndpointFormState({
    name: 'ep',
    url: 'https://user:pass@example.com',
    headerRows: [{ name: 'X-Test', value: '1' }, { name: 'x-test', value: '2' }]
  }).headers, 'Header 名称不能重复');
});

test('端点表单保存独立失败策略并支持恢复全局默认', () => {
  const original = createEndpointFormState({ name: 'policy', url: 'https://example.test', softFailureWindowMinutes: 10, softFailureThreshold: 5 });
  assert.equal(original.softFailureCustom, true);
  assert.equal(original.softFailureWindowMinutes, '10');
  assert.equal(original.softFailureThreshold, '5');
  const payload = buildEndpointFormPayload(original);
  assert.equal(payload.soft_failure_window_minutes, 10);
  assert.equal(payload.soft_failure_threshold, 5);
  const reset = buildEndpointFormPayload({ ...original, softFailureCustom: false });
  assert.equal(reset.soft_failure_window_minutes, null);
  assert.equal(reset.soft_failure_threshold, null);
  assert.ok(validateEndpointFormState({ ...original, softFailureWindowMinutes: '1.5' }).softFailurePolicy);
});

test('订阅直通端点：表单状态、校验与提交数据', () => {
  const state = createEndpointFormState({
    name: 'subscription',
    url: 'https://api.anthropic.com',
    authMode: 'client_oauth'
  });
  assert.equal(state.authMode, 'client_oauth');
  assert.equal(summarizeEndpointAuthentication({ authMode: 'client_oauth', tokenMasked: 'x' }), 'Claude 订阅直通');

  // URL 由表单固定为官方地址，用户残留的输入不参与校验与提交
  const payload = buildEndpointFormPayload({
    ...state,
    url: 'https://relay.example.com',
    token: 'sk-should-not-send',
    apiKey: 'key-should-not-send',
    clearToken: true
  });
  assert.equal(payload.authMode, 'client_oauth');
  assert.equal(payload.url, 'https://api.anthropic.com');
  assert.equal(payload.token, '');
  assert.equal(payload.apiKey, '');
  assert.equal(payload.clearToken, false);
  assert.equal(validateEndpointFormState({ ...state, url: '' }).url, undefined);

  assert.equal(validateEndpointFormState({
    ...state,
    headerRows: [{ name: 'Authorization', value: 'Bearer x' }]
  }).headers, '订阅直通端点的自定义 Header 不能包含认证类请求头');
  assert.equal(validateEndpointFormState({
    ...state,
    headerRows: [{ name: 'X-Trace', value: '1' }]
  }).headers, undefined);
});

test('旧端点默认按端点凭据处理', () => {
  const state = createEndpointFormState({ name: 'legacy', url: 'https://relay.example.com' });
  assert.equal(state.authMode, 'configured');
  const payload = buildEndpointFormPayload({ ...state, token: 'sk-new' });
  assert.equal(payload.authMode, 'configured');
  assert.equal(payload.url, 'https://relay.example.com');
  assert.equal(payload.token, 'sk-new');
  assert.equal(createEndpointFormState().authMode, 'configured');
});
