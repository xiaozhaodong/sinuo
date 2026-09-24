import test from 'node:test';
import assert from 'node:assert/strict';

import { buildSubscriptionEndpointNames, isSubscriptionRequest } from './subscriptionUpstream.js';

test('只收集订阅直通端点名', () => {
  const names = buildSubscriptionEndpointNames([
    { name: 'claude-subscription', authMode: 'client_oauth' },
    { name: 'vendor', authMode: 'configured' },
    { name: 'legacy' },
    null
  ]);
  assert.deepEqual([...names], ['claude-subscription']);
  assert.equal(buildSubscriptionEndpointNames(undefined).size, 0);
});

test('只把 Claude 端点请求识别为订阅请求', () => {
  const names = new Set(['claude-subscription']);
  const base = { requestFamily: 'claude', upstreamType: 'endpoint', upstreamName: 'claude-subscription' };
  assert.equal(isSubscriptionRequest(base, names), true);
  assert.equal(isSubscriptionRequest({ ...base, upstreamName: 'vendor' }, names), false);
  assert.equal(isSubscriptionRequest({ ...base, requestFamily: 'codex' }, names), false);
  assert.equal(isSubscriptionRequest({ ...base, upstreamType: 'account' }, names), false);
  assert.equal(isSubscriptionRequest(base, undefined), false);
  assert.equal(isSubscriptionRequest(null, names), false);
});
