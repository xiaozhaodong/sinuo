import test from 'node:test';
import assert from 'node:assert/strict';

import { getModelColorClasses } from './modelTag.js';

test('getModelColorClasses supports new model families used in request tracking', () => {
  assert.match(getModelColorClasses('glm-5.2'), /teal/);
  assert.match(getModelColorClasses('claude-fable-5'), /rose/);
  assert.match(getModelColorClasses('claude-sonnet-5'), /amber/);
  assert.match(getModelColorClasses(' Claude-Sonnet-5 '), /amber/);
});

test('getModelColorClasses distinguishes GPT-5.6 model tiers', () => {
  assert.match(getModelColorClasses('gpt-5.6'), /yellow/);
  assert.match(getModelColorClasses('gpt-5.6-sol'), /yellow/);
  assert.match(getModelColorClasses('gpt-5.6-sol-2026-07-09'), /yellow/);
  assert.match(getModelColorClasses('gpt-5.6-terra'), /lime/);
  assert.match(getModelColorClasses('gpt-5.6-luna'), /sky/);
});

test('getModelColorClasses distinguishes GPT-6 Astra from generic GPT', () => {
  assert.match(getModelColorClasses('gpt-6-astra'), /fuchsia/);
  assert.match(getModelColorClasses('gpt-6-astra-2026-09-01'), /fuchsia/);
  assert.match(getModelColorClasses('GPT-6-Astra'), /fuchsia/);
});

test('getModelColorClasses keeps existing model color mappings', () => {
  assert.match(getModelColorClasses('claude-sonnet-4'), /orange/);
  assert.match(getModelColorClasses('claude-3-5-haiku'), /green/);
  assert.match(getModelColorClasses('claude-3-5-sonnet'), /blue/);
  assert.match(getModelColorClasses('claude-opus-4'), /purple/);
  assert.match(getModelColorClasses('gpt-5.4-mini'), /indigo/);
});

test('getModelColorClasses supports deepseek / kimi / moonshot model families', () => {
  assert.match(getModelColorClasses('deepseek-v4-flash'), /cyan/);
  assert.match(getModelColorClasses('DeepSeek-R1'), /cyan/);
  assert.match(getModelColorClasses('kimi-k3'), /pink/);
  assert.match(getModelColorClasses('moonshot-v1-8k'), /pink/);
  assert.match(getModelColorClasses('glm-5.2'), /teal/);
  assert.match(getModelColorClasses('glm'), /teal/);
});

test('getModelColorClasses uses neutral color for missing or unknown models', () => {
  assert.match(getModelColorClasses('unknown'), /slate/);
  assert.match(getModelColorClasses('-'), /slate/);
  assert.match(getModelColorClasses(''), /slate/);
  assert.match(getModelColorClasses(null), /slate/);
});
