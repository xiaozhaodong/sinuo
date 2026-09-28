import test from 'node:test';
import assert from 'node:assert/strict';

import { getModelColorClasses } from './modelTag.js';

const tone = (key) => `grad-tone model-tone-${key}`;

test('getModelColorClasses supports new model families used in request tracking', () => {
  assert.equal(getModelColorClasses('glm-5.2'), tone('glm'));
  assert.equal(getModelColorClasses('claude-fable-5'), tone('fable'));
  assert.equal(getModelColorClasses('claude-sonnet-5'), tone('sonnet-5'));
  assert.equal(getModelColorClasses(' Claude-Sonnet-5 '), tone('sonnet-5'));
});

test('getModelColorClasses distinguishes GPT-5.6 model tiers', () => {
  assert.equal(getModelColorClasses('gpt-5.6'), tone('sol'));
  assert.equal(getModelColorClasses('gpt-5.6-sol'), tone('sol'));
  assert.equal(getModelColorClasses('gpt-5.6-sol-2026-07-09'), tone('sol'));
  assert.equal(getModelColorClasses('gpt-5.6-terra'), tone('terra'));
  assert.equal(getModelColorClasses('gpt-5.6-luna'), tone('luna'));
});

test('getModelColorClasses gives GPT-6 Astra its own tone, distinct from generic GPT, Opus and Haiku', () => {
  assert.equal(getModelColorClasses('gpt-6-astra'), tone('astra'));
  assert.equal(getModelColorClasses('gpt-6-astra-2026-09-01'), tone('astra'));
  assert.equal(getModelColorClasses('GPT-6-Astra'), tone('astra'));
  assert.notEqual(getModelColorClasses('gpt-6-astra'), getModelColorClasses('claude-opus-5-5'));
  assert.notEqual(getModelColorClasses('gpt-6-astra'), getModelColorClasses('claude-haiku-4-5'));
});

test('getModelColorClasses reuses GPT-5.6 Sol / Luna colors for GPT-6 Sol / Luna', () => {
  assert.equal(getModelColorClasses('gpt-6-sol'), tone('sol'));
  assert.equal(getModelColorClasses('gpt-6-sol-2026-09-22'), tone('sol'));
  assert.equal(getModelColorClasses('GPT-6-Sol'), tone('sol'));
  assert.equal(getModelColorClasses('gpt-6-luna'), tone('luna'));
  assert.equal(getModelColorClasses('gpt-6-luna-2026-09-22'), tone('luna'));
  assert.equal(getModelColorClasses('gpt-6'), tone('gpt'));
});

test('getModelColorClasses keeps existing model family mappings', () => {
  assert.equal(getModelColorClasses('claude-sonnet-4'), tone('sonnet-4'));
  assert.equal(getModelColorClasses('claude-3-5-haiku'), tone('haiku'));
  assert.equal(getModelColorClasses('claude-3-5-sonnet'), tone('sonnet-3-5'));
  assert.equal(getModelColorClasses('claude-opus-4'), tone('opus'));
  assert.equal(getModelColorClasses('gpt-5.4-mini'), tone('gpt'));
});

test('getModelColorClasses supports deepseek / kimi / moonshot model families', () => {
  assert.equal(getModelColorClasses('deepseek-v4-flash'), tone('deepseek'));
  assert.equal(getModelColorClasses('DeepSeek-R1'), tone('deepseek'));
  assert.equal(getModelColorClasses('kimi-k3'), tone('kimi'));
  assert.equal(getModelColorClasses('moonshot-v1-8k'), tone('kimi'));
  assert.equal(getModelColorClasses('glm-5.2'), tone('glm'));
  assert.equal(getModelColorClasses('glm'), tone('glm'));
});

test('getModelColorClasses uses neutral color for missing or unknown models', () => {
  assert.equal(getModelColorClasses('unknown'), 'tone-slate');
  assert.equal(getModelColorClasses('-'), 'tone-slate');
  assert.equal(getModelColorClasses(''), 'tone-slate');
  assert.equal(getModelColorClasses(null), 'tone-slate');
  assert.equal(getModelColorClasses('codex-auto-review'), 'tone-slate');
});
