import test from 'node:test';
import assert from 'node:assert/strict';

import { buildAccountUpdatePayload, buildManualFailoverPriorityPlan } from './manualFailover.js';
import { buildUpstreamAccountPayload } from '../../../utils/wailsApi.js';

const summarizePlan = (plan = []) => plan
  .map(({ account, priority }) => [account.id, priority])
  .sort((left, right) => left[0] - right[0]);

test('manual priority updates preserve the optional Codex metadata setting', () => {
  for (const alias of ['strip_codex_internal_metadata', 'stripCodexInternalMetadata', 'StripCodexInternalMetadata']) {
    for (const value of [true, false]) {
      const update = buildAccountUpdatePayload({ provider_type: 'api_key', [alias]: value }, 20);
      const payload = buildUpstreamAccountPayload(update);
      assert.equal(payload.strip_codex_internal_metadata, value);
      assert.equal(payload.priority, 20);
      assert.equal(buildUpstreamAccountPayload(payload).strip_codex_internal_metadata, value);
    }
  }
  const omitted = buildUpstreamAccountPayload(buildAccountUpdatePayload({ provider_type: 'api_key' }, 20));
  assert.equal('strip_codex_internal_metadata' in omitted, false);
});

test('buildManualFailoverPriorityPlan promotes the whole tier to primary', () => {
  const accounts = [
    { id: 1, priority: 10, account_name: 'primary-a' },
    { id: 2, priority: 10, account_name: 'primary-b' },
    { id: 3, priority: 20, account_name: 'backup-a' },
    { id: 4, priority: 20, account_name: 'backup-b' }
  ];

  const plan = buildManualFailoverPriorityPlan({
    accounts,
    targetAccountId: 4,
    targetTierIndex: 0
  });

  assert.deepEqual(summarizePlan(plan), [
    [1, 20],
    [2, 20],
    [3, 10],
    [4, 10]
  ]);
});

test('buildManualFailoverPriorityPlan moves the whole tier into backup position', () => {
  const accounts = [
    { id: 1, priority: 10, account_name: 'primary' },
    { id: 2, priority: 20, account_name: 'backup-old' },
    { id: 3, priority: 30, account_name: 'fallback-a' },
    { id: 4, priority: 30, account_name: 'fallback-b' }
  ];

  const plan = buildManualFailoverPriorityPlan({
    accounts,
    targetAccountId: 3,
    targetTierIndex: 1
  });

  assert.deepEqual(summarizePlan(plan), [
    [2, 30],
    [3, 20],
    [4, 20]
  ]);
});

test('buildManualFailoverPriorityPlan keeps an existing primary tier intact', () => {
  const accounts = [
    { id: 1, priority: 10, account_name: 'primary-a' },
    { id: 2, priority: 10, account_name: 'primary-b' },
    { id: 3, priority: 20, account_name: 'backup' }
  ];

  const plan = buildManualFailoverPriorityPlan({
    accounts,
    targetAccountId: 2,
    targetTierIndex: 0
  });

  assert.deepEqual(plan, []);
});

test('buildAccountUpdatePayload preserves model rewrite rules during manual tier updates', () => {
  const payload = buildAccountUpdatePayload({
    id: 9,
    provider_type: 'api_key',
    account_name: 'anyrouter',
    credential_raw: 'sk-anyrouter',
    base_url: 'https://api.anyrouter.example',
    model_rewrite_rules: '[{"from":"gpt-5.4","to":"gpt-5.5"}]',
    enabled: true
  }, 20);

  assert.equal(payload.model_rewrite_rules, '[{"from":"gpt-5.4","to":"gpt-5.5"}]');
  assert.equal(payload.priority, 20);
});
