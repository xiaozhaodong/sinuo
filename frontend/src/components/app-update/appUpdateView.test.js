import test from 'node:test';
import assert from 'node:assert/strict';
import { acceptAppUpdateStatus, canShowNotes, cleanReleaseNotes, emptyAppUpdateStatus, resolveUpdateDialogSections, resolveUpdateActions, resolveUpdateBadge, resolveUpdatePrimaryAction, resolveUpdateStateLabel, resolveUpdateTrigger, updateProgressPercent } from './appUpdateView.js';

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

test('更新说明清理标题、粗体和列表标记，保留换行与普通文本', () => {
  assert.equal(cleanReleaseNotes('## 更新\n\n- **修复**连接\n### 注意\n<script>text</script>'), '更新\n\n• 修复连接\n注意\n<script>text</script>');
  assert.equal(cleanReleaseNotes(), '');
});

test('仅去掉与当前更新版本一致的首行版本标题', () => {
  const notes = '## [6.2.0] - 2026-10-19\n\n### 新功能\n- **更新**内容';
  assert.equal(cleanReleaseNotes(notes, '6.2.0'), '新功能\n• 更新内容');
  assert.equal(cleanReleaseNotes(notes, '6.3.0'), '[6.2.0] - 2026-10-19\n\n新功能\n• 更新内容');
  assert.equal(cleanReleaseNotes(notes), '[6.2.0] - 2026-10-19\n\n新功能\n• 更新内容');
  assert.equal(cleanReleaseNotes('说明\n## [6.2.0] - 2026-10-19', '6.2.0'), '说明\n[6.2.0] - 2026-10-19');
  assert.equal(cleanReleaseNotes('\n\n## [6.2.0]\r\n\r\n* 修复\r\n', '6.2.0'), '• 修复');
});

test('列表符号替换为圆点并保留空格和制表符缩进', () => {
  assert.equal(cleanReleaseNotes('  - 第一项\n    * 子项\n\t- 制表符\n普通 - 文本'), '  • 第一项\n    • 子项\n\t• 制表符\n普通 - 文本');
  assert.equal(cleanReleaseNotes('## [6.2.0]\n\n  * 缩进项目', '6.2.0'), '  • 缩进项目');
  assert.equal(cleanReleaseNotes(' \n\t\n'), '');
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

test('P2 胶囊覆盖完整流程，已跳过时全部隐藏', () => {
  for (const [state, label] of Object.entries({ available: '6.1.0 可更新', downloading: '下载中 45%', installing: '正在安装', ready: '重启更新', waiting_idle: '等待 3 个请求', restarting: '正在重启' })) {
    const status = { state, update_available: true, latest_version: '6.1.0', progress_written: 45, progress_total: 100, active_requests: 3 };
    assert.equal(resolveUpdateBadge(status), label);
    assert.equal(resolveUpdateBadge({ ...status, skipped: true }), null);
  }
  assert.equal(resolveUpdateBadge({ state: 'downloading', update_available: true, latest_version: '6.1.0' }), '下载中');
  assert.equal(updateProgressPercent({ progress_total: 10, progress_written: 20 }), 100);
  assert.equal(updateProgressPercent({ progress_total: 0 }), null);
});

test('安装支持与所有状态的面板按钮矩阵', () => {
  for (const supported of [true, false]) {
    for (const state of ['idle', 'checking', 'available', 'up_to_date', 'error', 'downloading', 'installing', 'ready', 'waiting_idle', 'restarting']) {
      const status = { ...emptyAppUpdateStatus(), state, install_supported: supported, update_available: true, release_url: 'https://github.com/xiaozhaodong/sinuo/releases/latest' };
      const actions = resolveUpdateActions(status);
      const expected = { downloading: 'cancelDownload', installing: 'cancelDownload', ready: 'restart', waiting_idle: 'cancelRestart', restarting: null, checking: null };
      assert.equal(actions.primary, Object.hasOwn(expected, state) ? expected[state] : supported ? 'download' : 'external', `${state} supported=${supported}`);
      assert.equal(actions.checkDisabled, ['checking', 'downloading', 'installing', 'ready', 'waiting_idle', 'restarting'].includes(state));
    }
  }
  assert.equal(resolveUpdateActions(emptyAppUpdateStatus()).primary, null);
});

test('版本触发器在无胶囊时显示当前版本，所有更新状态复用胶囊，空版本不渲染', () => {
  const base = { ...emptyAppUpdateStatus(), current_version: '6.2.0' };
  for (const state of ['idle', 'up_to_date', 'checking', 'error']) {
    assert.deepEqual(resolveUpdateTrigger({ ...base, state }), {
      badge: false, label: 'v6.2.0', title: '当前版本 v6.2.0'
    });
  }
  for (const state of ['available', 'downloading', 'installing', 'ready', 'waiting_idle', 'restarting']) {
    const status = { ...base, state, update_available: true, latest_version: '6.3.0', progress_written: 45, progress_total: 100, active_requests: 2 };
    assert.deepEqual(resolveUpdateTrigger(status), {
      badge: true, label: resolveUpdateBadge(status), title: '有新版本 6.3.0，点击查看'
    });
    assert.equal(resolveUpdateTrigger({ ...status, skipped: true }).label, 'v6.2.0');
    assert.equal(resolveUpdateTrigger({ ...status, current_version: '' }), null);
  }
  assert.equal(resolveUpdateTrigger(emptyAppUpdateStatus()), null);
  assert.equal(resolveUpdateTrigger(null), null);
});

test('更新弹窗完整状态矩阵与设置页使用相同操作、说明及禁用规则', () => {
  const base = { ...emptyAppUpdateStatus(), current_version: '6.2.0', local_build: true };
  const release = {
    ...base, update_available: true, latest_version: '6.3.0', install_supported: true,
    release_url: 'https://github.com/xiaozhaodong/sinuo/releases/tag/v6.3.0',
    published_at: '2026-10-19T10:00:00Z', checked_at: '2026-10-19T10:01:00Z', notes: '更新说明'
  };
  const cases = [
    { state: 'idle', status: base, primary: 'check' },
    { state: 'up_to_date', status: base, primary: 'check' },
    { state: 'checking', status: base, primary: 'check', disabled: true },
    { state: 'error', status: { ...base, error: '无法连接 GitHub' }, primary: 'check' },
    { state: 'available', status: release, primary: 'download' },
    { state: 'available', status: { ...release, install_supported: false, install_blocked_reason: '此版本未提供签名，请前往下载' }, primary: 'external' },
    { state: 'available', status: { ...release, skipped: true }, primary: 'download' },
    { state: 'downloading', status: { ...release, progress_written: 45, progress_total: 100 }, primary: 'cancelDownload', progress: 'download' },
    { state: 'downloading', status: { ...release, progress_written: 45 }, primary: 'cancelDownload', progress: 'download' },
    { state: 'installing', status: release, primary: 'cancelDownload', progress: 'install' },
    { state: 'ready', status: { ...release, ready_version: '6.3.0' }, primary: 'restart' },
    { state: 'waiting_idle', status: { ...release, active_requests: 2 }, primary: 'cancelRestart' },
    { state: 'restarting', status: release, primary: null },
    { state: 'checking', status: release, primary: null },
    { state: 'error', status: { ...release, error: '下载更新失败' }, primary: 'download' }
  ];
  for (const row of cases) {
    const status = { ...row.status, state: row.state };
    const sections = resolveUpdateDialogSections(status);
    const panelActions = resolveUpdateActions(status);
    assert.equal(sections.primary, row.primary, row.state);
    assert.deepEqual(sections.primaryAction, resolveUpdatePrimaryAction(row.primary));
    assert.equal(sections.primaryDisabled, Boolean(row.disabled));
    assert.equal(sections.primaryLoading, row.primary === 'check' && row.state === 'checking');
    assert.equal(sections.progress, row.progress || null);
    assert.equal(sections.showNotes, canShowNotes(status));
    assert.equal(sections.stateLabel, resolveUpdateStateLabel(status));
    assert.equal(sections.stateError, row.state === 'error');
    assert.equal(sections.skipDisabled, panelActions.skipDisabled);
    assert.equal(sections.showSkip, Boolean(status.update_available));
    assert.equal(sections.skipLabel, status.skipped ? '取消跳过' : '跳过此版本');
    assert.equal(sections.skipVersion, status.skipped ? '' : status.latest_version);
    assert.equal(sections.showExternal, Boolean(status.release_url && row.primary !== 'external'));
    assert.equal(sections.title, status.update_available ? 'Sinuo 6.3.0' : 'Sinuo v6.2.0');
    assert.equal(sections.showLocalBuild, !status.update_available);
    assert.equal(sections.showPublishedAt, Boolean(status.published_at));
    assert.equal(sections.showCheckedAt, Boolean(status.checked_at));
    assert.equal(sections.blockedReason, status.update_available && !status.install_supported ? status.install_blocked_reason : '');
    if (panelActions.primary) assert.equal(sections.primary, panelActions.primary);
  }
});

test('无当前平台安装包时仍保留外部下载；操作错误与状态错误不重复显示', () => {
  const status = { ...emptyAppUpdateStatus(), current_version: '6.2.0', state: 'error', error: '此平台没有安装包', latest_version: '6.3.0', release_url: 'https://github.com/xiaozhaodong/sinuo/releases/latest' };
  assert.equal(resolveUpdateDialogSections(status).primary, 'external');
  assert.equal(resolveUpdateDialogSections(status).showSkip, false);
  assert.equal(resolveUpdateDialogSections(status, status.error).notice, '');
  assert.equal(resolveUpdateDialogSections(status, '打开下载页面失败').notice, '打开下载页面失败');
});
