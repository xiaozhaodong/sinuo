// lifecycleDetailController 六场景单测（方案 F3 测试计划）
import test from 'node:test';
import assert from 'node:assert/strict';
import { createLifecycleDetailController } from './lifecycleDetailController.js';

const deferred = () => {
  let resolve;
  let reject;
  const promise = new Promise((res, rej) => {
    resolve = res;
    reject = rej;
  });
  return { promise, resolve, reject };
};

const flush = () => new Promise((resolve) => setTimeout(resolve, 0));

const setup = (fetchDetail) => {
  let subscribeCallback = null;
  let unsubscribeCalled = false;
  const subscribe = (callback) => {
    subscribeCallback = callback;
    return () => {
      unsubscribeCalled = true;
    };
  };
  const changes = [];
  const controller = createLifecycleDetailController({
    fetchDetail,
    subscribe,
    onChange: (result) => changes.push(result)
  });
  return {
    controller,
    getSubscribeCallback: () => subscribeCallback,
    changes,
    wasUnsubscribed: () => unsubscribeCalled
  };
};

test('场景1：打开后无任何事件也拉取一次', async () => {
  let calls = 0;
  const { controller } = setup(async (id) => {
    calls += 1;
    return { found: true, request: { request_id: id } };
  });

  controller.open('req-a');
  await flush();
  await flush();

  assert.equal(calls, 1);
  controller.close();
});

test('场景2：打开状态切换 requestId 立即拉取新请求', async () => {
  const seen = [];
  const { controller } = setup(async (id) => {
    seen.push(id);
    return { found: true, request: { request_id: id } };
  });

  controller.open('req-a');
  await flush();
  controller.open('req-b');
  await flush();

  assert.deepEqual(seen, ['req-a', 'req-b']);
  controller.close();
});

test('场景3：非本请求事件忽略', async () => {
  let calls = 0;
  const { controller, getSubscribeCallback } = setup(async () => {
    calls += 1;
    return { found: true };
  });

  controller.open('req-a');
  await flush();
  getSubscribeCallback()({ request_id: 'req-other' });
  await flush();

  assert.equal(calls, 1);
  controller.close();
});

test('场景4：连发事件合并为一次拉取', async () => {
  let calls = 0;
  const { controller, getSubscribeCallback } = setup(async () => {
    calls += 1;
    return { found: true };
  });

  controller.open('req-a');
  await new Promise((resolve) => setTimeout(resolve, 300)); // 初始 triggerNow 完成

  getSubscribeCallback()({ request_id: 'req-a' });
  getSubscribeCallback()({ request_id: 'req-a' });
  getSubscribeCallback()({ request_id: 'req-a' });
  await new Promise((resolve) => setTimeout(resolve, 300)); // debounce 合并后刷新一次

  assert.equal(calls, 2); // 初始 1 次 + 合并后 1 次
  controller.close();
});

test('场景5：快速切换请求不串数据', async () => {
  const pending = new Map();
  const { controller, changes } = setup((id) => {
    const entry = deferred();
    pending.set(id, entry);
    return entry.promise;
  });

  controller.open('req-a');
  controller.open('req-b');

  pending.get('req-b').resolve({ found: true, request: { request_id: 'req-b' } });
  await flush();
  assert.equal(changes.length, 1);
  assert.equal(changes[0].request.request_id, 'req-b');

  // A 的慢响应晚到：代际保护丢弃。
  pending.get('req-a').resolve({ found: true, request: { request_id: 'req-a' } });
  await flush();
  assert.equal(changes.length, 1);

  controller.close();
});

test('场景6：关闭后慢响应不回写', async () => {
  const entry = deferred();
  const { controller, changes } = setup(() => entry.promise);

  controller.open('req-a');
  controller.close();

  entry.resolve({ found: true, request: { request_id: 'req-a' } });
  await flush();
  await flush();

  assert.equal(changes.length, 0);
});

test('关闭会退订事件订阅', () => {
  const { controller, wasUnsubscribed } = setup(async () => ({ found: true }));
  controller.open('req-a');
  controller.close();
  assert.equal(wasUnsubscribed(), true);
});

test('刷新失败或读到旧版本时保留已显示详情，后续生命周期事件可重试', async () => {
  const responses = [
    { found: true, requestRevision: 5 },
    new Error('temporary failure'),
    { found: true, requestRevision: 4 },
    { found: true, requestRevision: 6 }
  ];
  const changes = [];
  let notify, run;
  const controller = createLifecycleDetailController({
    fetchDetail: async () => {
      const next = responses.shift();
      if (next instanceof Error) throw next;
      return next;
    },
    onChange: (value) => changes.push(value.requestRevision),
    subscribe: (cb) => { notify = cb; return () => {}; },
    schedulerFactory: ({ refresh }) => ({
      triggerNow: refresh, schedule: () => { run = refresh; }, cancel() {}
    })
  });
  controller.open('req');
  await flush();
  for (const expected of [[5], [5], [5, 6]]) {
    run = null;
    // 匹配请求的生命周期通知都能刷新，即使它带着旧的版本号。
    notify({ request_id: 'req', request_revision: 4 });
    assert.ok(run);
    await run();
    assert.deepEqual(changes, expected);
  }
  controller.close();
});
