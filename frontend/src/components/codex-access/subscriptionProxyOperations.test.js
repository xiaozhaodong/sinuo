import test from 'node:test';
import assert from 'node:assert/strict';
import { createSubscriptionProxyController, modeSwitchBlockReason } from './subscriptionProxyOperations.js';

const initialConfig = () => ({
  initialized: true, enabled: true, mode: 'subscription', revision: 4,
  listenHost: '::1', listenPort: 18999, wsCompression: 'disabled', accessConfigured: true
});
const runtimeFor = (config) => ({
  state: 'running', enabled: true, mode: config.mode, revision: config.revision,
  activeWebSockets: 6, activeRequests: 2
});
const deferred = () => {
  let resolve;
  const promise = new Promise((done) => { resolve = done; });
  return { promise, resolve };
};
const fixture = () => {
  const data = { config: initialConfig(), status: runtimeFor(initialConfig()), writes: [] };
  const api = {
    getConfig: async () => ({ ...data.config }),
    getStatus: async () => ({ ...data.status }),
    saveConfig: async (input, revision) => {
      data.writes.push({ input, revision });
      assert.equal(revision, data.config.revision);
      data.config = { ...data.config, mode: input.mode, revision: revision + 1 };
      data.status = runtimeFor(data.config);
      return { config: { ...data.config } };
    }
  };
  return { data, api };
};

test('快捷切换只改模式，保留 IPv6 地址、端口、启用意图和 WS 配置，并核对读回版本', async () => {
  const { data, api } = fixture();
  const controller = createSubscriptionProxyController(api);
  await controller.refresh();
  const expected = controller.getSnapshot().config;
  const result = await controller.switchMode('pool', expected);
  assert.equal(result.saved, true);
  assert.equal(result.confirmed, true);
  assert.deepEqual(data.writes, [{
    revision: 4,
    input: { enabled: true, mode: 'pool', listen_host: '::1', listen_port: 18999, ws_compression: 'disabled' }
  }]);
  assert.equal(expected.mode, 'subscription');
  assert.equal(expected.revision, 4);
  assert.equal(controller.getSnapshot().status.revision, 5);
  assert.match(result.notice.text, /HTTP 从下一请求生效/);
  assert.match(result.notice.text, /WS 在重连后生效/);
  assert.match(result.notice.text, /上下游两侧/);
});

test('弹窗打开后别处保存新版本，拒绝覆盖并不自动重试', async () => {
  const { data, api } = fixture();
  const controller = createSubscriptionProxyController(api);
  await controller.refresh();
  const expected = controller.getSnapshot().config;
  data.config = { ...data.config, revision: 5, listenPort: 20001 };
  data.status = runtimeFor(data.config);
  const result = await controller.switchMode('pool', expected);
  assert.equal(result.saved, false);
  assert.match(result.notice.text, /已被其他写入/);
  assert.equal(data.writes.length, 0);
  assert.equal(controller.getSnapshot().config.listenPort, 20001);
});

test('保存前检查之后的并发写入交给后端 revision 校验，不重发', async () => {
  const { api } = fixture();
  let attempts = 0;
  api.saveConfig = async () => {
    attempts++;
    throw '配置已被其他写入更新：revision=6';
  };
  const controller = createSubscriptionProxyController(api);
  const result = await controller.switchMode('pool', initialConfig());
  assert.equal(result.saved, false);
  assert.match(result.notice.text, /不会自动覆盖/);
  assert.equal(attempts, 1);
  assert.equal(controller.getSnapshot().busy, false);
});

test('未初始化、停用或启动失败的入口不执行快捷保存', async () => {
  for (const [variant, expected] of [
    ['uninitialized', /初始化/], ['disabled', /停用意图.*仍在运行/],
    ['failed', /启动失败/], ['draining', /停用中/]
  ]) {
    const { data, api } = fixture();
    if (variant === 'uninitialized') data.config = { ...data.config, initialized: false, revision: 0 };
    if (variant === 'disabled') data.config.enabled = false;
    if (variant === 'failed' || variant === 'draining') data.status.state = variant;
    const controller = createSubscriptionProxyController(api);
    const result = await controller.switchMode('pool', { ...data.config });
    assert.equal(result.saved, false, variant);
    assert.equal(data.writes.length, 0, variant);
    assert.match(result.notice.text, expected, variant);
  }
});

test('主动停用与意图启用但未运行给出不同提示', () => {
  const base = { initialized: true, enabled: false, mode: 'subscription', revision: 3, accessConfigured: true };
  // 主动停用：说明原有入口策略和客户端配置边界，不宣称请求已自动切换。
  const stoppedByUser = modeSwitchBlockReason({
    config: base,
    status: { state: 'stopped', enabled: false },
    fallbackRouteLabel: '模型路由'
  });
  assert.match(stoppedByUser, /已停用/);
  assert.match(stoppedByUser, /模型路由/);
  assert.match(stoppedByUser, /启用/);
  assert.match(stoppedByUser, /仍指向独立入口.*无法连接/);
  assert.match(stoppedByUser, /修改客户端接入配置/);
  assert.doesNotMatch(stoppedByUser, /请求当前走原有入口/);
  assert.doesNotMatch(stoppedByUser, /未运行/);

  // 拿不到回退标签时不留下空括号
  const withoutRoute = modeSwitchBlockReason({ config: base, status: { state: 'stopped', enabled: false } });
  assert.match(withoutRoute, /已停用/);
  assert.doesNotMatch(withoutRoute, /（）/);

  // 意图启用却没起来：仍然按异常提示检查并启用
  const notRunning = modeSwitchBlockReason({
    config: { ...base, enabled: true },
    status: { state: 'stopped', enabled: false }
  });
  assert.match(notRunning, /未运行/);
  assert.doesNotMatch(notRunning, /已停用/);
});

test('保存停用意图后，过渡、失败和未知运行态仍阻止切换且不冒充已停用', async () => {
  for (const [state, expected] of [
    ['starting', /启动中/], ['draining', /停用中/], ['failed', /启动失败/],
    ['running', /停用意图.*仍在运行/], ['unknown', /状态未知/]
  ]) {
    const { data, api } = fixture();
    data.config.enabled = false;
    data.status.state = state;
    const result = await createSubscriptionProxyController(api).switchMode('pool', { ...data.config });
    assert.equal(result.saved, false, state);
    assert.equal(data.writes.length, 0, state);
    assert.match(result.notice.text, expected, state);
    assert.doesNotMatch(result.notice.text, /已停用|当前走原有入口/, state);
  }
});

test('运行态未启用时不能仅凭 running 放行模式切换', async () => {
  const { data, api } = fixture();
  data.status.enabled = false;
  const result = await createSubscriptionProxyController(api).switchMode('pool', { ...data.config });
  assert.equal(result.saved, false);
  assert.equal(data.writes.length, 0);
  assert.match(result.notice.text, /配置与运行态尚未一致/);
});

test('读取失败不以默认订阅状态代替，也不写配置', async () => {
  for (const kind of ['getConfig', 'getStatus']) {
    const { data, api } = fixture();
    api[kind] = async () => { throw new Error('fixture read unavailable'); };
    const controller = createSubscriptionProxyController(api);
    await controller.refresh();
    const snapshot = controller.getSnapshot();
    assert.equal(snapshot[kind === 'getConfig' ? 'config' : 'status'], null);
    assert.match(modeSwitchBlockReason(snapshot), /fixture read unavailable/);
    const result = await controller.switchMode('pool', initialConfig());
    assert.equal(result.saved, false);
    assert.equal(data.writes.length, 0);
  }
});

test('保存成功但读回失败，只报已保存待确认，不报运行态切换成功', async () => {
  for (const kind of ['getConfig', 'getStatus']) {
    const { data, api } = fixture();
    const original = api[kind];
    api[kind] = async () => {
      if (data.writes.length) throw new Error('fixture readback unavailable');
      return original();
    };
    const controller = createSubscriptionProxyController(api);
    const result = await controller.switchMode('pool', initialConfig());
    assert.equal(result.saved, true);
    assert.equal(result.confirmed, false);
    assert.equal(result.notice.type, 'info');
    assert.match(result.notice.text, /已保存.*尚未确认运行态/);
    assert.equal(controller.getSnapshot().config.mode, 'pool');
    assert.equal(data.writes.length, 1);
  }
});

test('保存后运行态滞后、已停止或其他写入覆盖均不能确认本次生效', async () => {
  for (const variant of ['oldRuntime', 'stopped', 'newerConfig']) {
    const { data, api } = fixture();
    const save = api.saveConfig;
    api.saveConfig = async (...args) => {
      const result = await save(...args);
      if (variant === 'oldRuntime') data.status = runtimeFor(initialConfig());
      if (variant === 'stopped') data.status.state = 'stopped';
      if (variant === 'newerConfig') {
        data.config = { ...data.config, mode: 'subscription', revision: 6 };
        data.status = runtimeFor(data.config);
      }
      return result;
    };
    const result = await createSubscriptionProxyController(api).switchMode('pool', initialConfig());
    assert.equal(result.saved, true, variant);
    assert.equal(result.confirmed, false, variant);
    assert.notEqual(result.notice.type, 'success', variant);
  }
});

test('重复确认与保存中的刷新不会重复保存或抢占读回', async () => {
  const { data, api } = fixture();
  const gate = deferred();
  const save = api.saveConfig;
  api.saveConfig = async (...args) => { await gate.promise; return save(...args); };
  const controller = createSubscriptionProxyController(api);
  const first = controller.switchMode('pool', initialConfig());
  const second = await controller.switchMode('pool', initialConfig());
  await controller.refresh();
  assert.equal(second.saved, false);
  assert.equal(controller.getSnapshot().busy, true);
  gate.resolve();
  assert.equal((await first).confirmed, true);
  assert.equal(data.writes.length, 1);
});

test('保存前发出的慢刷新不能在保存完成后覆盖新模式', async () => {
  const { api } = fixture();
  const gate = deferred();
  const read = api.getConfig;
  let first = true;
  api.getConfig = async () => {
    if (first) { first = false; return gate.promise; }
    return read();
  };
  const controller = createSubscriptionProxyController(api);
  const staleRefresh = controller.refresh();
  const result = await controller.switchMode('pool', initialConfig());
  assert.equal(result.confirmed, true);
  gate.resolve(initialConfig());
  await staleRefresh;
  assert.equal(controller.getSnapshot().config.mode, 'pool');
  assert.equal(controller.getSnapshot().status.mode, 'pool');
});

test('只打开和刷新模式菜单不产生配置写入；非法模式也不产生写入', async () => {
  const { data, api } = fixture();
  const controller = createSubscriptionProxyController(api);
  await controller.refresh();
  await controller.refresh();
  assert.equal(data.writes.length, 0);
  const result = await controller.switchMode('unexpected', initialConfig());
  assert.equal(result.saved, false);
  assert.equal(data.writes.length, 0);
});
