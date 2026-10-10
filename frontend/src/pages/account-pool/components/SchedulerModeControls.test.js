import test from 'node:test';
import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { runInNewContext } from 'node:vm';
import { isValidAccountId, resolveAccountId } from '../utils/accountPool.js';

const __dirname = path.dirname(fileURLToPath(import.meta.url));

const readSource = async (relativePath) => {
  const sourcePath = path.resolve(__dirname, relativePath);
  return readFile(sourcePath, 'utf8');
};

test('scheduler inspector, group table and actions hook expose manual/auto mode controls', async () => {
  const [drawerSource, groupCardSource, indexSource] = await Promise.all([
    readSource('../../codex/schedule/SchedulerInspector.jsx'),
    readSource('../../codex/schedule/SchedulerGroupsTable.jsx'),
    readSource('../../codex/schedule/useSchedulerActions.js')
  ]);

  assert.match(drawerSource, /启用编排/);
  assert.match(drawerSource, /当前模式/);
  assert.match(drawerSource, /全局固定|最近一次调度/);
  assert.match(drawerSource, /enableAutoSelection/);
  assert.match(groupCardSource, /actions\.pinAccount/);

  assert.match(groupCardSource, /设为本组首选|本组首选/);
  assert.match(groupCardSource, /设为全局固定/);
  assert.match(groupCardSource, />固定<\/button>/);
  assert.match(groupCardSource, /actions\.pinAccount/);

  assert.match(indexSource, /handleSchedulerPinAccount/);
  assert.match(indexSource, /handleSchedulerEnableAutoSelection/);
  assert.match(indexSource, /pinAccount: handleSchedulerPinAccount/);
  assert.match(indexSource, /enableAutoSelection: handleSchedulerEnableAutoSelection/);
});

const actionsSource = (await readSource('../../codex/schedule/useSchedulerActions.js'))
  .replace(/^import [\s\S]*?;\n/gm, '')
  .replace('export default useSchedulerActions;', 'globalThis.hook = useSchedulerActions;');

const createActionsHarness = (apiName, result) => {
  const calls = [];
  const notices = [];
  const refreshes = [];
  let busyKey = '';
  const context = {
    isValidAccountId, resolveAccountId,
    useCallback: callback => callback,
    useState: initial => [initial, value => { busyKey = value; }],
    [apiName]: async (...args) => {
      calls.push(args);
      if (result instanceof Error) throw result;
      return result;
    }
  };
  runInNewContext(actionsSource, context);
  const actions = context.hook({
    showNotice: (...args) => notices.push(args),
    loadData: async options => refreshes.push(['accounts', options.silent]),
    loadLatestScheduleSnapshot: async options => refreshes.push(['snapshot', options.silent])
  });
  return { actions, calls, notices, refreshes, get busyKey() { return busyKey; } };
};

const actionCases = [
  { name: 'setActiveAccount', api: 'setGroupActiveAccount', args: [{ id: 12 }, { key: ' Primary ' }], call: ['primary', 12], busy: 'group-active-primary-12',
    success: '已更新当前组首选账号', unchanged: '当前组首选账号未发生变化', unsupported: '当前后端版本暂不支持设置组内首选账号', failure: '设置组内首选账号失败' },
  { name: 'pinAccount', api: 'pinUpstreamAccountSelection', args: [{ id: 12, account_name: '测试账号' }], call: [12], busy: 'scheduler-pin-12',
    success: '已将「测试账号」设为手动账号', unchanged: '「测试账号」已经是当前手动账号', unsupported: '当前后端版本暂不支持固定账号', failure: '设置手动账号失败' },
  { name: 'enableAutoSelection', api: 'enableAutomaticAccountSelection', args: [], call: [], busy: 'scheduler-enable-auto',
    success: '已启用编排自动调度', unchanged: '当前已按编排自动调度', unsupported: '当前后端版本暂不支持启用编排', failure: '启用编排失败' },
  { name: 'swapGroup', api: 'swapUpstreamAccountGroups', args: [{ key: ' Primary ' }, { key: 'swap-down', targetGroupKey: ' Backup ' }], call: ['primary', 'backup'], busy: 'group-swap-primary-swap-down',
    success: '已完成相邻组整组交换', unchanged: '当前整组交换未产生变化', unsupported: '当前后端版本暂不支持整组交换', failure: '整组交换失败' }
];

for (const action of actionCases) {
  for (const outcome of ['success', 'unchanged', 'unsupported', 'failure', 'serverMessage']) {
    test(`${action.name} ${outcome} 保持 API、busy、文案及成功后双数据刷新契约`, async () => {
      const result = outcome === 'failure' ? new Error('')
        : outcome === 'unsupported' ? { unsupported: true, changed: true }
          : outcome === 'unchanged' ? { changed: false }
            : { changed: true, ...(outcome === 'serverMessage' ? { message: '后端提示' } : {}) };
      const harness = createActionsHarness(action.api, result);
      const pending = harness.actions[action.name](...action.args);
      assert.equal(harness.busyKey, action.busy);
      await pending;
      assert.equal(harness.busyKey, '');
      assert.deepEqual(harness.calls, [action.call]);
      const success = outcome === 'success' || outcome === 'serverMessage';
      assert.deepEqual(harness.notices, [[success ? 'success' : outcome === 'failure' ? 'error' : 'info', outcome === 'serverMessage' ? '后端提示' : action[outcome]]]);
      assert.deepEqual(harness.refreshes, success ? [['accounts', true], ['snapshot', true]] : []);
    });
  }
}

test('调度写操作参数无效时保留原提示且不调用 API', async () => {
  const cases = [
    ['setActiveAccount', [{}, {}], '缺少组别或账号 ID，无法设置组内首选账号'],
    ['pinAccount', [{}], '缺少账号 ID，无法设为手动账号'],
    ['swapGroup', [{}, {}], '缺少整组交换目标，无法执行编排动作']
  ];
  for (const [name, args, message] of cases) {
    const harness = createActionsHarness('unused', {});
    await harness.actions[name](...args);
    assert.deepEqual(harness.calls, []);
    assert.deepEqual(harness.notices, [['error', message]]);
    assert.equal(harness.busyKey, '');
  }
});
