import test from 'node:test';
import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const __dirname = path.dirname(fileURLToPath(import.meta.url));

const readSource = async (relativePath) => {
  const sourcePath = path.resolve(__dirname, relativePath);

  try {
    return await readFile(sourcePath, 'utf8');
  } catch (error) {
    assert.fail(`expected ${relativePath} to exist for the account pool surface redesign, but it could not be read: ${error.message}`);
  }
};

test('inventory area and scheduler tab expose redesigned workspace landmarks', async () => {
  const [filtersSource, schedulerSource, schedulerDrawerSource, groupCardSource, tableSource, drawerSource, snapshotSource, candidateSource, actionsHookSource] = await Promise.all([
    readSource('./AccountInventoryFilters.jsx'),
    readSource('../../codex/schedule/CodexSchedulePage.jsx'),
    readSource('../../codex/schedule/SchedulerInspector.jsx'),
    readSource('../../codex/schedule/SchedulerGroupsTable.jsx'),
    readSource('./AccountInventoryTable.jsx'),
    readSource('./AccountDetailsDrawer.jsx'),
    readSource('../../codex/schedule/ScheduleCandidatesTable.jsx'),
    readSource('../../codex/schedule/ScheduleCandidatesTable.jsx'),
    readSource('../hooks/useAccountPoolActions.js')
  ]);

  assert.match(filtersSource, /搜索账号/);
  assert.match(filtersSource, /搜索账号/);
  assert.match(tableSource, /最近成功/);
  assert.match(tableSource, /最近刷新/);
  assert.match(drawerSource, /账号详情/);
  assert.match(drawerSource, /路由组别/);
  assert.match(drawerSource, /组内顺序/);
  assert.match(drawerSource, /5h 重置/);
  assert.match(drawerSource, /d7 重置/);
  assert.match(drawerSource, /label: '主组'/);
  assert.match(drawerSource, /label: '备组'/);
  assert.match(drawerSource, /label: '冷备'/);
  assert.match(drawerSource, /已在\$\{action\.label\}/);
  assert.match(drawerSource, /移到\$\{action\.label\}/);
  assert.match(drawerSource, /busyKey/);
  assert.match(drawerSource, /disabled=\{/);
  assert.match(actionsHookSource, /account-switch-/, 'expected move busy tokens to be generated from the actions hook');
  assert.doesNotMatch(drawerSource, /DetailRow label="priority"/);
  assert.doesNotMatch(drawerSource, /当前版本暂未支持独立冷备编排/);

  assert.match(schedulerDrawerSource, /调度编排/);
  assert.match(schedulerDrawerSource, /LatestSection/);
  assert.match(schedulerDrawerSource, /busyKey/);
  assert.match(schedulerSource, /useSchedulerActions/);
  assert.match(schedulerDrawerSource, /启用编排|按编排/);
  assert.match(schedulerDrawerSource, /手动模式|Auto 模式|当前模式/);
  assert.match(schedulerSource, /主组/);
  assert.match(schedulerSource, /备组/);
  assert.match(schedulerSource, /冷备/);
  assert.match(schedulerDrawerSource, /最新调度摘要|最近一次调度/);
  assert.match(schedulerSource, /onViewInInventory/);
  assert.match(groupCardSource, /swapGroup/);

  assert.match(groupCardSource, /当前组动作|整组交换/);
  assert.match(groupCardSource, /整组交换，不是单账号加入|与相邻组整组交换/);
  assert.match(groupCardSource, /group-swap-/);
  assert.match(groupCardSource, /swap-up/);
  assert.match(groupCardSource, /swap-down/);
  assert.doesNotMatch(groupCardSource, /promote-primary/);
  assert.doesNotMatch(groupCardSource, /move-backup/);
  assert.match(groupCardSource, /group\.accounts\.map/);
  assert.match(groupCardSource, /在账号池中查看本组/);
  assert.match(groupCardSource, /设为本组首选|本组首选/);
  assert.match(groupCardSource, /全局固定|设为全局固定/);
  assert.doesNotMatch(groupCardSource, /visibleAccounts|DEFAULT_VISIBLE|showAll/);

  assert.match(snapshotSource, /命中账号、命中组别、组内顺序/);
  assert.doesNotMatch(snapshotSource, /命中层级/);
  assert.match(candidateSource, /组内/);
  assert.doesNotMatch(candidateSource, /未分层/);
  assert.match(actionsHookSource, /targetTier === 'cold'/);
  assert.doesNotMatch(actionsHookSource, /手动切换层级|手动切换顺序/);
});

test('overview and anomaly center files are removed from the account pool workspace', async () => {
  await assert.rejects(() => readFile(path.resolve(__dirname, './AccountPoolOverviewSection.jsx'), 'utf8'));
  await assert.rejects(() => readFile(path.resolve(__dirname, './AccountPoolAnomalySection.jsx'), 'utf8'));
  await assert.rejects(() => readFile(path.resolve(__dirname, './AnomalyQueueCard.jsx'), 'utf8'));
});

test('旧调度抽屉及卡片文件已删除', async () => {
  for (const file of ['SchedulerDrawer', 'AccountPoolSchedulerSection', 'GroupBoardCard', 'LatestScheduleSnapshotCard', 'ScheduleCandidateCard']) {
    await assert.rejects(() => readFile(path.resolve(__dirname, `${file}.jsx`), 'utf8'), { code: 'ENOENT' });
  }
});
