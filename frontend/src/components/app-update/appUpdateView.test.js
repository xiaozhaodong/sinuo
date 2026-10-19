import test from 'node:test';
import assert from 'node:assert/strict';
import { acceptAppUpdateStatus, canShowNotes, cleanReleaseNotes, emptyAppUpdateStatus, resolveUpdateBadge, resolveUpdateStateLabel } from './appUpdateView.js';

test('已知更新且未跳过时展示胶囊和说明，不依赖本次检查状态', () => {
  const available = { state: 'available', update_available: true, latest_version: '6.1.0', skipped: false };
  assert.equal(resolveUpdateBadge(available), '6.1.0 可更新');
  assert.equal(canShowNotes(available), true);
  assert.equal(resolveUpdateBadge({ ...available, skipped: true }), null);
  assert.equal(canShowNotes({ ...available, skipped: true }), false);
  for (const state of ['checking', 'error']) {
    assert.equal(resolveUpdateBadge({ ...available, state }), '6.1.0 可更新');
    assert.equal(canShowNotes({ ...available, state }), true);
    assert.equal(resolveUpdateBadge({ ...available, state, skipped: true }), null);
    assert.equal(canShowNotes({ ...available, state, skipped: true }), false);
  }
  assert.equal(resolveUpdateBadge({ ...available, update_available: false }), null);
  assert.equal(canShowNotes({ ...available, update_available: false }), false);
  assert.equal(resolveUpdateBadge(emptyAppUpdateStatus()), null);
  assert.equal(canShowNotes(emptyAppUpdateStatus()), false);
  assert.equal(resolveUpdateBadge(null), null);
  assert.equal(canShowNotes(null), false);
});

test('发现更新后，检查中及失败保留提示，成功检查为最新后才清除', () => {
  const available = { ...emptyAppUpdateStatus(), state: 'available', update_available: true, latest_version: '6.1.0', notes: '更新说明', release_url: 'https://github.com/xiaozhaodong/sinuo/releases/tag/v6.1.0', revision: 1 };
  let current = acceptAppUpdateStatus(emptyAppUpdateStatus(), available);
  for (const incoming of [
    { ...available, state: 'checking', revision: 2 },
    { ...available, state: 'error', error: '无法连接 GitHub', revision: 3 }
  ]) {
    current = acceptAppUpdateStatus(current, incoming);
    assert.equal(resolveUpdateBadge(current), '6.1.0 可更新');
    assert.equal(canShowNotes(current), true);
    assert.equal(current.notes, available.notes);
    assert.equal(current.release_url, available.release_url);
  }
  assert.equal(resolveUpdateStateLabel(current), '无法连接 GitHub');
  current = acceptAppUpdateStatus(current, { ...emptyAppUpdateStatus(), state: 'up_to_date', revision: 4 });
  assert.equal(resolveUpdateBadge(current), null);
  assert.equal(canShowNotes(current), false);
  assert.equal(current.release_url, '');
});

test('更新说明仅去掉 Markdown 标题和粗体标记，保留换行与普通文本', () => {
  assert.equal(cleanReleaseNotes('## 更新\n\n- **修复**连接\n### 注意\n<script>text</script>'), '更新\n\n- 修复连接\n注意\n<script>text</script>');
  assert.equal(cleanReleaseNotes(), '');
});

test('状态文案包含错误、跳过和检查中', () => {
  assert.equal(resolveUpdateStateLabel({ state: 'available', skipped: true, latest_version: '6.1.0' }), '已跳过 6.1.0');
  assert.equal(resolveUpdateStateLabel({ state: 'error', error: '无法连接 GitHub' }), '无法连接 GitHub');
  assert.equal(resolveUpdateStateLabel({ state: 'checking' }), '检查中…');
  assert.equal(resolveUpdateStateLabel({ state: 'up_to_date' }), '已是最新版本');
});

test('较晚到达的旧事件和读取结果不能覆盖新 revision', () => {
  const current = acceptAppUpdateStatus(emptyAppUpdateStatus(), { state: 'available', update_available: true, latest_version: '6.1.0', revision: 5 });
  assert.equal(acceptAppUpdateStatus(current, { state: 'checking', revision: 4 }), current);
  assert.equal(acceptAppUpdateStatus(current, undefined), current);
  assert.equal(acceptAppUpdateStatus(current, { state: 'idle' }), current);
  assert.equal(acceptAppUpdateStatus(current, { state: 'up_to_date', revision: 6 }).state, 'up_to_date');
});
