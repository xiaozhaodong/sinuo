import test from 'node:test';
import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import { runInNewContext } from 'node:vm';
import { setImmediate } from 'node:timers/promises';

const source = (await readFile(new URL('./useLatestScheduleSnapshot.js', import.meta.url), 'utf8'))
  .replace(/^import .*;\n/gm, '')
  .replace('export default useLatestScheduleSnapshot;', 'globalThis.hook = useLatestScheduleSnapshot;');

// 只替换 React hooks 与快照 API，执行真实 hook 的 effect、清理函数和异步加载。
const createHarness = () => {
  const slots = [];
  const effects = [];
  const timers = new Map();
  const listeners = new Map();
  const notices = [];
  let cursor = 0;
  let calls = 0;
  let props = {};
  let response = { hasSnapshot: true, selectedAccountId: 1 };
  const sameDeps = (left, right) => left && right && left.length === right.length && left.every((item, index) => Object.is(item, right[index]));
  const context = {
    useState(initial) {
      const index = cursor++;
      slots[index] ??= { value: initial };
      return [slots[index].value, value => { slots[index].value = value; }];
    },
    useRef(initial) {
      const index = cursor++;
      slots[index] ??= { current: initial };
      return slots[index];
    },
    useCallback(callback, deps) {
      const index = cursor++;
      if (!sameDeps(slots[index]?.deps, deps)) slots[index] = { value: callback, deps };
      return slots[index].value;
    },
    useEffect(effect, deps) {
      const index = cursor++;
      if (sameDeps(slots[index]?.deps, deps)) return;
      effects.push(() => {
        slots[index]?.cleanup?.();
        slots[index] = { deps, cleanup: effect() };
      });
    },
    async fetchLatestAccountScheduleSnapshot() {
      calls += 1;
      if (response instanceof Error) throw response;
      return response;
    },
    setInterval(callback, ms) { const token = {}; timers.set(token, { callback, ms }); return token; },
    clearInterval(token) { timers.delete(token); },
    document: {
      visibilityState: 'visible',
      addEventListener(name, callback) { listeners.set(name, callback); },
      removeEventListener(name, callback) { if (listeners.get(name) === callback) listeners.delete(name); }
    }
  };
  runInNewContext(source, context);
  const showNotice = (...args) => notices.push(args);
  const render = (nextProps = props) => {
    props = nextProps;
    cursor = 0;
    const result = context.hook({ showNotice, ...props });
    effects.splice(0).forEach(effect => effect());
    return result;
  };
  return {
    render, timers, listeners, notices, document: context.document,
    get calls() { return calls; },
    setResponse(value) { response = value; },
    async settle() { await setImmediate(); return render(); },
    tick() { [...timers.values()].forEach(timer => timer.callback()); },
    visibility(state) { context.document.visibilityState = state; listeners.get('visibilitychange')?.(); },
    unmount() { slots.forEach(slot => slot.cleanup?.()); }
  };
};

test('默认 enabled 保持首次加载、5s 静默轮询与卸载清理', async () => {
  const harness = createHarness();
  harness.render();
  assert.equal(harness.calls, 1);
  assert.equal([...harness.timers.values()][0].ms, 5000);
  harness.tick();
  await harness.settle();
  assert.equal(harness.calls, 2);
  assert.deepEqual(harness.notices, []);
  harness.unmount();
  assert.equal(harness.timers.size, 0);
  assert.equal(harness.listeners.size, 0);
});

test('禁用仍首次加载；启用立即静默刷新；再次禁用停止轮询与前台刷新', async () => {
  const harness = createHarness();
  harness.render({ enabled: false });
  await harness.settle();
  assert.equal(harness.calls, 1);
  assert.equal(harness.timers.size, 0);
  harness.visibility('visible');
  assert.equal(harness.calls, 1);
  harness.render({ enabled: true });
  await harness.settle();
  assert.equal(harness.calls, 2);
  assert.equal(harness.timers.size, 1);
  harness.render({ enabled: false });
  harness.tick();
  harness.visibility('visible');
  assert.equal(harness.calls, 2);
  assert.equal(harness.timers.size, 0);
  assert.equal(harness.listeners.size, 0);
  assert.deepEqual(harness.notices, []);
});

test('页面后台不轮询，回到前台立即刷新', async () => {
  const harness = createHarness();
  harness.render();
  await harness.settle();
  harness.visibility('hidden');
  harness.tick();
  assert.equal(harness.calls, 1);
  harness.visibility('visible');
  await harness.settle();
  assert.equal(harness.calls, 2);
});

test('不支持快照时停止轮询；重新启用允许立即探测一次', async () => {
  const harness = createHarness();
  harness.setResponse({ unsupported: true, message: '不支持' });
  harness.render();
  const result = await harness.settle();
  assert.equal(result.snapshotUnsupported, true);
  assert.equal(harness.timers.size, 0);
  assert.equal(harness.listeners.size, 0);
  harness.render({ enabled: false });
  harness.render({ enabled: true });
  await harness.settle();
  assert.equal(harness.calls, 2);
});

test('轮询失败保留上一份数据且无提示，手动非静默失败显示原错误', async () => {
  const harness = createHarness();
  harness.render();
  const initial = await harness.settle();
  harness.setResponse(new Error('模拟失败'));
  harness.tick();
  const failed = await harness.settle();
  assert.equal(failed.latestScheduleSnapshot, initial.latestScheduleSnapshot);
  assert.deepEqual(harness.notices, []);
  await failed.loadLatestScheduleSnapshot();
  assert.deepEqual(harness.notices, [['error', '模拟失败']]);
});
