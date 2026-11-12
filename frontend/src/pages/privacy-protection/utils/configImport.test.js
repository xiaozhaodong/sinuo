import test from 'node:test';
import assert from 'node:assert/strict';
import { CONFIG_IMPORT_MAX_FILE_BYTES, createConfigImportSession, defaultConfigSelection, readConfigImportFiles, selectedConfigIDs } from './configImport.js';

const file = (name, content) => {
  const bytes = new TextEncoder().encode(content);
  return { name, size: bytes.length, arrayBuffer: async () => bytes.buffer };
};

test('读取配置保留空格、换行和 UTF-8；超限在读取前拒绝', async () => {
  const content = 'PASSWORD="  合成密码  "\n';
  assert.deepEqual(await readConfigImportFiles([file('.env', content)]), [{ name: '.env', content }]);
  let reads = 0;
  await assert.rejects(readConfigImportFiles([{ size: CONFIG_IMPORT_MAX_FILE_BYTES + 1, arrayBuffer: () => reads++ }]), /2 MiB/);
  assert.equal(reads, 0);
  await assert.rejects(readConfigImportFiles([]), /1–32/);
  await assert.rejects(readConfigImportFiles(Array.from({ length: 33 }, () => file('.env', 'x'))), /1–32/);
  await assert.rejects(readConfigImportFiles([{ name: '.env', size: 1, arrayBuffer: async () => new Uint8Array([0xff]).buffer }]), /UTF-8/);
});

test('仅建议且允许选择的候选预选，提交不会夹带已存在或排除项', () => {
  const candidates = [
    { id: 'a', selectable: true, selected: true },
    { id: 'b', selectable: true, selected: false },
    { id: 'c', selectable: false, selected: true }
  ];
  assert.deepEqual([...defaultConfigSelection(candidates)], ['a']);
  assert.deepEqual(selectedConfigIDs(candidates, new Set(['a', 'b', 'c', 'invalid'])), ['a', 'b']);
});

test('预览取消、替换、提交分别释放对应会话；提交只发送候选 ID', async () => {
  const calls = [];
  let number = 0;
  const session = createConfigImportSession({
    preview: async () => ({ id: `preview-${++number}`, candidates: [] }),
    cancel: async (id) => { calls.push(['cancel', id]); },
    commit: async (id, selected) => { calls.push(['commit', id, selected]); return { added: 1, skipped: 0 }; }
  });
  await session.preview([]);
  await session.preview([]);
  const result = await session.commit(['candidate-1']);
  await session.dispose();
  assert.equal(result.added, 1);
  assert.deepEqual(calls, [['cancel', 'preview-1'], ['commit', 'preview-2', ['candidate-1']]]);
  await assert.rejects(session.commit(['candidate-1']), /失效/);
});

test('窗口先卸载、预览后返回时立即取消；失效窗口不能提交', async () => {
  let resolve;
  const cancelled = [];
  const session = createConfigImportSession({
    preview: () => new Promise((done) => { resolve = done; }),
    cancel: async (id) => cancelled.push(id),
    commit: async () => assert.fail('不能提交')
  });
  const pending = session.preview([]);
  await session.dispose();
  resolve({ id: 'late-preview' });
  assert.equal(await pending, null);
  assert.deepEqual(cancelled, ['late-preview']);
  assert.equal(await session.preview([]), null);
});

test('后端失败明确抛出，并保留会话用于重试或取消', async () => {
  const cancelled = [];
  const session = createConfigImportSession({
    preview: async () => ({ id: 'retry-preview' }),
    commit: async () => { throw new Error('synthetic transaction failure'); },
    cancel: async (id) => cancelled.push(id)
  });
  await session.preview([]);
  await assert.rejects(session.commit(['a']), /transaction failure/);
  await session.dispose();
  assert.deepEqual(cancelled, ['retry-preview']);
});
