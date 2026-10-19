import test from 'node:test';
import assert from 'node:assert/strict';
import { runAppUpdateRestart } from './appUpdateRestart.js';

test('空闲重启必须先确认，取消不调用后端', async () => {
  for (const accepted of [false, true]) {
    const calls = [];
    await runAppUpdateRestart({
      readStatus: async () => ({ state: 'ready', active_requests: 0 }),
      confirmIdle: async () => { calls.push('confirm'); return accepted; },
      chooseRequests: async () => assert.fail('空闲不询问请求'),
      restart: async (mode) => { calls.push(`restart:${mode}`); return {}; }
    });
    assert.deepEqual(calls, accepted ? ['confirm', 'restart:'] : ['confirm']);
  }
});

test('活跃请求提供等待、立即和取消三种选择', async () => {
  for (const mode of ['wait', 'now', '']) {
    const calls = [];
    await runAppUpdateRestart({
      readStatus: async () => ({ state: 'ready', active_requests: 3 }),
      confirmIdle: async () => assert.fail('有请求时不使用空闲提示'),
      chooseRequests: async (count) => { assert.equal(count, 3); return mode; },
      restart: async (value) => { calls.push(value); return {}; }
    });
    assert.deepEqual(calls, mode ? [mode] : []);
  }
});

test('确认期间新增请求需要再次选择，取消不得立即重启', async () => {
  const calls = [];
  await runAppUpdateRestart({
    readStatus: async () => ({ state: 'ready', active_requests: 0 }),
    confirmIdle: async () => true,
    chooseRequests: async (count) => { assert.equal(count, 2); return ''; },
    restart: async (mode) => { calls.push(mode); return { needs_confirm: true, active_requests: 2 }; }
  });
  assert.deepEqual(calls, ['']);
});

test('状态未就绪和重启错误明确抛出', async () => {
  await assert.rejects(runAppUpdateRestart({ readStatus: async () => ({ state: 'downloading' }) }), /尚未就绪/);
  await assert.rejects(runAppUpdateRestart({
    readStatus: async () => ({ state: 'ready', active_requests: 0 }),
    confirmIdle: async () => true, restart: async () => { throw new Error('helper failed'); }
  }), /helper failed/);
});
