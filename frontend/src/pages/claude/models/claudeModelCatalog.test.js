import test from 'node:test';
import assert from 'node:assert/strict';
import {
  filterClaudeModels,
  normalizeClaudeCatalogForSave,
  parseClaudeCatalog,
  removeClaudeModel,
  setClaudeModelEnabled,
  upsertClaudeModel,
  validateClaudeModelDraft
} from './claudeModelCatalog.js';

test('Claude catalog sorts by UTF-8 bytes after enabled state', () => {
  const supplementary = String.fromCodePoint(0x10000);
  const privateUse = String.fromCodePoint(0xe000);
  const result = normalizeClaudeCatalogForSave({
    enabled: true,
    models: [
      { id: privateUse, display_name: '', enabled: false },
      { id: supplementary, display_name: '', enabled: false },
      { id: 'z-model', display_name: '', enabled: true },
      { id: 'a-model', display_name: '', enabled: true }
    ]
  });

  assert.deepEqual(result.models.map((model) => model.id), ['a-model', 'z-model', privateUse, supplementary]);
  assert.equal(result.models[0].display_name, 'a-model');
});

test('parseClaudeCatalog handles empty, malformed and tolerant fields', () => {
  assert.deepEqual(parseClaudeCatalog(''), { enabled: true, models: [] });
  assert.throws(() => parseClaudeCatalog('{bad json'), SyntaxError);
  assert.deepEqual(parseClaudeCatalog(JSON.stringify({ enabled: false, models: [{ id: 42, display_name: 7, enabled: false }] })), {
    enabled: false,
    models: [{ id: '42', display_name: '7', enabled: false }]
  });
});

test('validateClaudeModelDraft covers required, length, characters, duplicate and name errors', () => {
  assert.equal(validateClaudeModelDraft({ id: '' }, []).id, '请输入模型 ID');
  assert.match(validateClaudeModelDraft({ id: 'x'.repeat(257) }, []).id, /超过 256/);
  assert.match(validateClaudeModelDraft({ id: 'bad id' }, []).id, /不能包含空白/);
  assert.match(validateClaudeModelDraft({ id: 'same' }, [{ id: 'same' }]).id, /重复/);
  assert.match(validateClaudeModelDraft({ id: 'ok', display_name: 'x'.repeat(257) }, []).name, /超过 256/);
  assert.match(validateClaudeModelDraft({ id: 'ok', display_name: 'name\u0000' }, []).name, /控制字符/);
});

test('validateClaudeModelDraft excludes the original row when editing its ID', () => {
  assert.deepEqual(validateClaudeModelDraft({ id: 'same', display_name: '' }, [{ id: 'same' }], 'same'), {});
  assert.match(validateClaudeModelDraft({ id: 'other' }, [{ id: 'other' }, { id: 'same' }], 'same').id, /重复/);
});

test('filterClaudeModels matches ID and display name and sorts by ID', () => {
  const models = [
    { id: 'zeta', display_name: 'Alpha', enabled: true },
    { id: 'beta', display_name: 'Beta', enabled: false },
    { id: 'alpha', display_name: 'Gamma', enabled: true }
  ];
  assert.deepEqual(filterClaudeModels(models, { query: 'ALP' }).map((model) => model.id), ['alpha', 'zeta']);
  assert.deepEqual(filterClaudeModels(models, { filter: 'enabled' }).map((model) => model.id), ['alpha', 'zeta']);
  assert.deepEqual(filterClaudeModels(models, { filter: 'disabled' }).map((model) => model.id), ['beta']);
});

test('Claude catalog list operations preserve immutable model arrays', () => {
  const models = [{ id: 'a', display_name: 'A', enabled: true }];
  assert.deepEqual(upsertClaudeModel(models, { id: 'b', display_name: 'B', enabled: true }), [...models, { id: 'b', display_name: 'B', enabled: true }]);
  assert.deepEqual(upsertClaudeModel(models, { id: 'c', display_name: 'C', enabled: true }, 'a'), [{ id: 'c', display_name: 'C', enabled: true }]);
  assert.deepEqual(removeClaudeModel(models, 'a'), []);
  assert.deepEqual(setClaudeModelEnabled(models, 'a', false), [{ id: 'a', display_name: 'A', enabled: false }]);
  assert.equal(models[0].enabled, true);
});
