import test from 'node:test';
import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import { compileFunction } from 'node:vm';
import { transform } from 'esbuild';
import { appendLogBatch, createInitialLogBuffer, replaceRecentLogs } from '../../hooks/useWailsLogs.helpers.js';

const source = await readFile(new URL('./index.jsx', import.meta.url), 'utf8');
const { code } = await transform(source, { loader: 'jsx', jsx: 'automatic', format: 'cjs' });
const loadPage = compileFunction(code, ['module', 'require', 'window', 'requestAnimationFrame', 'cancelAnimationFrame']);

// 执行实际日志页。仅替换 Hook 调度、Wails 数据源和 DOM，记录真实页面输出的徽标和滚动调用。
function renderLogPage(t) {
  let buffer = createInitialLogBuffer();
  let loading = true;
  let cursor = 0;
  let changed = false;
  let badge, followButton, controls, onScroll;
  const slots = [], effects = [], frames = new Map();
  const domRefs = new Set();
  const scroll = t.mock.fn();
  const container = { scrollHeight: 2000, scrollTop: 0, clientHeight: 400 };
  const end = { scrollIntoView: scroll };
  const slot = () => slots[cursor++] ??= {};
  const sameDeps = (a, b) => a && a.length === b.length && a.every((v, i) => Object.is(v, b[i]));
  const hooks = {
    useState(initial) {
      const state = slot();
      if (!('value' in state)) state.value = initial;
      return [state.value, (update) => {
        const next = typeof update === 'function' ? update(state.value) : update;
        if (!Object.is(next, state.value)) changed = true;
        state.value = next;
      }];
    },
    useRef(initial) {
      const ref = slot();
      if (!('current' in ref)) ref.current = initial;
      return ref;
    },
    useMemo(fn, deps) {
      const memo = slot();
      if (!sameDeps(memo.deps, deps)) {
        memo.value = fn();
        memo.deps = deps;
      }
      return memo.value;
    },
    useCallback(fn, deps) { return hooks.useMemo(() => fn, deps); },
    useEffect(fn, deps) {
      const effect = slot();
      if (!sameDeps(effect.deps, deps)) {
        effect.deps = deps;
        effects.push(() => { effect.cleanup?.(); effect.cleanup = fn(); });
      }
    }
  };

  function element(type, props) {
    if (type === 'div' && props.ref) {
      props.ref.current = props.onScroll ? container : end;
      domRefs.add(props.ref);
      if (props.onScroll) onScroll = props.onScroll;
    }
    if (type === 'ToolbarButton' && props.title?.startsWith('跟随最新')) followButton = props;
    if (type === 'LogControls') controls = props;
    if (type === 'span' && typeof props.children === 'string' && props.children.endsWith('回到底部')) {
      badge = props.children;
    }
  }

  const modules = {
    react: hooks,
    'react/jsx-runtime': { jsx: element, jsxs: element },
    '@/hooks/useWailsLogs': { useWailsLogs: () => ({
      ...buffer, loading, error: null,
      clear: () => { buffer = createInitialLogBuffer(); },
      refresh: () => { loading = true; }
    }) },
    './components': { LogControls: 'LogControls' },
    '@components/layout/ToolbarButton.jsx': { ToolbarButton: 'ToolbarButton' }
  };
  const module = { exports: {} };
  let nextFrame = 0;
  loadPage(module, (id) => modules[id] ?? {}, { setTimeout: (fn) => fn() },
    (fn) => { frames.set(++nextFrame, fn); return nextFrame; }, (id) => frames.delete(id));

  function render() {
    do {
      cursor = 0;
      changed = false;
      badge = null;
      for (const ref of domRefs) ref.current = null;
      domRefs.clear();
      module.exports.default();
      for (const effect of effects.splice(0)) effect();
    } while (changed);
    for (const fn of frames.values()) fn();
    frames.clear();
  }

  const logs = (count) => Array.from({ length: count }, () => ({ level: 'INFO', message: '示例日志' }));
  render();
  buffer = replaceRecentLogs(buffer, logs(1000), 1000);
  loading = false;
  render();
  assert.equal(scroll.mock.callCount(), 1, '首次载入历史应滚动一次，确认滚动观测有效');
  scroll.mock.resetCalls();

  return {
    scroll,
    get badge() { return badge; },
    toggleFollow() { followButton.onClick(); render(); },
    scrollUp() { onScroll(); render(); },
    receive(count) { buffer = appendLogBatch(buffer, logs(count), 1000); render(); },
    refresh() { controls.onRefresh(); render(); },
    finishRefresh() { buffer = replaceRecentLogs(buffer, buffer.logs, 1000); loading = false; render(); },
    clear() { controls.onClear(); render(); }
  };
}

test('暂停跟随时刷新：刷新期间的实时日志计入未读，历史快照不重复计数，也不滚动', (t) => {
  const page = renderLogPage(t);
  page.receive(4);
  assert.equal(page.scroll.mock.callCount(), 1);
  page.scroll.mock.resetCalls();
  page.toggleFollow();
  page.receive(2);
  assert.equal(page.badge, '2 条新日志 · 回到底部');
  page.refresh();
  page.receive(3);
  assert.deepEqual([page.badge, page.scroll.mock.callCount()], ['5 条新日志 · 回到底部', 0]);
  page.finishRefresh();
  assert.deepEqual([page.badge, page.scroll.mock.callCount()], ['5 条新日志 · 回到底部', 0]);
  page.refresh();
  page.finishRefresh();
  assert.equal(page.badge, '5 条新日志 · 回到底部');
  assert.equal(page.scroll.mock.callCount(), 0);
});

test('上滚暂停后清空：丢弃旧未读，第一条新日志计一次，手动恢复前始终不滚动', (t) => {
  const page = renderLogPage(t);
  page.scrollUp();
  page.receive(2);
  assert.equal(page.badge, '2 条新日志 · 回到底部');
  page.clear();
  assert.equal(page.badge, null);
  page.receive(1);
  assert.deepEqual([page.badge, page.scroll.mock.callCount()], ['1 条新日志 · 回到底部', 0]);
  page.receive(2);
  assert.deepEqual([page.badge, page.scroll.mock.callCount()], ['3 条新日志 · 回到底部', 0]);
  page.toggleFollow();
  assert.deepEqual([page.badge, page.scroll.mock.callCount()], [null, 1]);
  page.receive(1);
  assert.equal(page.scroll.mock.callCount(), 2);
});

test('保持跟随时刷新和清空：刷新完成、清空后首批到达各定位一次', (t) => {
  const page = renderLogPage(t);
  page.refresh();
  page.receive(3);
  assert.equal(page.scroll.mock.callCount(), 0);
  page.finishRefresh();
  assert.equal(page.scroll.mock.callCount(), 1);
  page.clear();
  assert.equal(page.scroll.mock.callCount(), 1);
  page.receive(1);
  assert.deepEqual([page.badge, page.scroll.mock.callCount()], [null, 2]);
});
