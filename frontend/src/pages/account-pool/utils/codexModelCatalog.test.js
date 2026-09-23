import test from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';

import {
  createModelDraft,
  hasValidCapabilities,
  mergeImportedModels,
  parseCatalogImport,
  toggleReasoningLevel,
  tryParseCapabilities,
  validateModelDraft
} from './codexModelCatalog.js';

// 与后端 Go 测试共用同一份用例，防止前后端校验规则漂移。
const fixture = JSON.parse(readFileSync(new URL('../../../../../internal/service/testdata/codex_capabilities_cases.json', import.meta.url), 'utf8'));

const buildCapabilities = ({ set = {}, remove = [] } = {}) => {
  const capabilities = { ...fixture.base };
  remove.forEach(key => delete capabilities[key]);
  return { ...capabilities, ...set };
};

const fixtureModel = (capabilities = buildCapabilities()) => ({
  ...createModelDraft(),
  id: 'fixture-model',
  display_name: 'Fixture',
  capabilities_json: JSON.stringify(capabilities)
});

for (const testCase of fixture.cases) {
  test(`共用能力用例：${testCase.name}`, () => {
    const model = fixtureModel(buildCapabilities(testCase));
    if (testCase.valid) {
      assert.doesNotThrow(() => validateModelDraft(model));
    } else {
      assert.throws(() => validateModelDraft(model));
    }
  });
}

test('validateModelDraft rejects whitespace and control characters in IDs', () => {
  for (const id of ['my model', 'nbsp\u00a0model', 'c1\u0085model', 'm'.repeat(257)]) {
    assert.throws(() => validateModelDraft({ ...fixtureModel(), id }), /模型 ID/);
  }
});

test('validateModelDraft clears stale backend capability errors', () => {
  const model = validateModelDraft({ ...fixtureModel(), id: ' gpt-x ', capabilities_error: 'visibility 的值无效' });
  assert.equal(model.id, 'gpt-x');
  assert.equal(model.capabilities_error, '');
});

test('parseCatalogImport treats null description like a missing one', () => {
  const catalog = { models: [{ slug: 'gpt-null-desc', display_name: 'Null Desc', description: null, ...fixture.base }] };
  const { models } = parseCatalogImport(JSON.stringify(catalog));
  assert.equal(models[0].description, '');
  assert.equal(JSON.parse(models[0].capabilities_json).description, undefined);
});

test('parseCatalogImport keeps existing state and reports conflicts', () => {
  const current = [{ ...fixtureModel(), id: 'gpt-existing', source: 'official', enabled: false, deprecated: true }];
  const catalog = { models: [
    { slug: ' gpt-existing ', display_name: 'Existing', ...fixture.base },
    { slug: 'gpt-new', display_name: 'New', ...fixture.base }
  ] };
  const { models, conflicts } = parseCatalogImport(JSON.stringify(catalog), current);
  assert.deepEqual(conflicts, ['gpt-existing']);
  assert.equal(models[0].source, 'official');
  assert.equal(models[0].enabled, false);
  assert.equal(models[0].deprecated, true);
  assert.equal(models[1].source, 'custom');
  assert.equal(mergeImportedModels(current, models).length, 2);
});

test('parseCatalogImport names the failing model', () => {
  const catalog = { models: [{ slug: 'gpt-bad', display_name: 'Bad', ...fixture.base, apply_patch_tool_type: 'function' }] };
  assert.throws(() => parseCatalogImport(JSON.stringify(catalog)), /模型 gpt-bad：apply_patch_tool_type/);
});

test('parseCatalogImport rejects duplicated slugs', () => {
  const catalog = { models: [
    { slug: 'gpt-dup', display_name: 'A', ...fixture.base },
    { slug: 'gpt-dup', display_name: 'B', ...fixture.base }
  ] };
  assert.throws(() => parseCatalogImport(JSON.stringify(catalog)), /重复/);
});

test('tryParseCapabilities reports broken JSON instead of throwing', () => {
  const result = tryParseCapabilities({ capabilities_json: '{"visibility":' });
  assert.equal(result.metadata, null);
  assert.ok(result.error);
  assert.deepEqual(tryParseCapabilities({ capabilities_json: '' }), { metadata: null, error: '' });
});

test('hasValidCapabilities excludes empty and backend-flagged capabilities', () => {
  assert.equal(hasValidCapabilities(fixtureModel()), true);
  assert.equal(hasValidCapabilities({ ...fixtureModel(), capabilities_error: 'broken' }), false);
  assert.equal(hasValidCapabilities({ ...fixtureModel(), capabilities_json: '  ' }), false);
});

test('toggleReasoningLevel keeps canonical order and reuses catalog descriptions', () => {
  const levels = [{ effort: 'medium', description: 'm' }, { effort: 'custom-deep', description: 'c' }];
  const template = fixtureModel(buildCapabilities({ set: { supported_reasoning_levels: [{ effort: 'low', description: '目录里的 low 说明' }] } }));

  const added = toggleReasoningLevel(levels, 'low', [template]);
  assert.deepEqual(added.map(level => level.effort), ['low', 'medium', 'custom-deep']);
  assert.equal(added[0].description, '目录里的 low 说明');

  const fallback = toggleReasoningLevel(levels, 'xhigh');
  assert.equal(fallback.find(level => level.effort === 'xhigh').description, 'Extra high reasoning depth for complex problems');

  assert.deepEqual(toggleReasoningLevel(added, 'medium').map(level => level.effort), ['low', 'custom-deep']);
});
