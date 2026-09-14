import test from 'node:test';
import assert from 'node:assert/strict';

import {
  buildCodexConfigSnippet,
  buildSubscriptionProxyAccessURL,
  buildSubscriptionProxyNotice,
  describePrivacyForSubscription,
  isListenLocked,
  isRevisionConflict,
  isSubscriptionProxyStoppedByUser,
  resolveSubscriptionProxyDisplayState,
  subscriptionProxyPrivacyWarning,
  subscriptionProxyStateMeta,
  validateSubscriptionProxyForm
} from './subscriptionProxyViewModel.js';

test('表单校验只接受回环地址与合法端口', () => {
  assert.equal(validateSubscriptionProxyForm({ listenHost: '127.0.0.1', listenPort: '18089' }), '');
  assert.equal(validateSubscriptionProxyForm({ listenHost: '::1', listenPort: 18089 }), '');
  assert.match(validateSubscriptionProxyForm({ listenHost: '0.0.0.0', listenPort: 18089 }), /回环/);
  assert.match(validateSubscriptionProxyForm({ listenHost: 'example.com', listenPort: 18089 }), /回环/);
  assert.match(validateSubscriptionProxyForm({ listenHost: '127.0.0.1', listenPort: '0' }), /1-65535/);
  assert.match(validateSubscriptionProxyForm({ listenHost: '127.0.0.1', listenPort: '70000' }), /1-65535/);
  assert.match(validateSubscriptionProxyForm({ listenHost: '', listenPort: 18089 }), /不能为空/);
});

test('运行中不允许改监听地址，必须先停用', () => {
  assert.equal(isListenLocked({ state: 'running' }), true);
  assert.equal(isListenLocked({ state: 'starting' }), true);
  assert.equal(isListenLocked({ state: 'draining' }), true);
  assert.equal(isListenLocked({ state: 'stopped' }), false);
  assert.equal(isListenLocked({ state: 'failed' }), false);
  assert.equal(isListenLocked(null), false);
});

test('revision 冲突只提示刷新，不与普通错误混同', () => {
  assert.equal(isRevisionConflict('Codex 订阅代理配置已被其他写入更新，请刷新后重试: 当前 revision=3, expected=2'), true);
  assert.equal(isRevisionConflict('监听 127.0.0.1:18089 失败: address already in use'), false);
});

test('保存成功不等于启动成功：提示一律取自独立读回的运行态', () => {
  const failed = buildSubscriptionProxyNotice({
    intent: 'enable',
    status: { state: 'failed', lastError: '监听 127.0.0.1:18089 失败: address already in use' }
  });
  assert.equal(failed.type, 'error');
  assert.match(failed.text, /address already in use/);

  const running = buildSubscriptionProxyNotice({
    intent: 'enable',
    status: { state: 'running', listenAddr: '127.0.0.1:18089' }
  });
  assert.equal(running.type, 'success');
  assert.match(running.text, /127\.0\.0\.1:18089/);

  const notListened = buildSubscriptionProxyNotice({
    intent: 'enable',
    status: { state: 'stopped' }
  });
  assert.equal(notListened.type, 'info');
});

test('模式切换只说 HTTP 下一请求与 WS 重连生效，并带出仍在运行的活动 WS 数', () => {
  const notice = buildSubscriptionProxyNotice({
    intent: 'save',
    modeChanged: true,
    status: { state: 'running', activeWebSockets: 3 }
  });
  assert.equal(notice.type, 'success');
  assert.match(notice.text, /HTTP 从下一请求生效/);
  assert.match(notice.text, /WS 在重连后生效/);
  assert.match(notice.text, /WS 连接数为 3/);
  assert.match(notice.text, /上下游两侧/);
  // 不能承诺“下一轮必定切换”
  assert.doesNotMatch(notice.text, /下一轮必定/);
  assert.doesNotMatch(notice.text, /立即/);
});

test('停用意图与停用结果分开表述', () => {
  const draining = buildSubscriptionProxyNotice({ intent: 'disable', status: { state: 'running' } });
  assert.equal(draining.type, 'info');
  const stopped = buildSubscriptionProxyNotice({ intent: 'disable', status: { state: 'stopped' } });
  assert.equal(stopped.type, 'success');
});

test('运行态没读到就不能确认已停用', () => {
  // refreshRuntime 失败返回 null；此时不能落到“已停用、不再监听”的成功分支
  const unknown = buildSubscriptionProxyNotice({ intent: 'disable', status: null });
  assert.equal(unknown.type, 'info');
  assert.match(unknown.text, /尚未读到运行态/);
  assert.doesNotMatch(unknown.text, /已停用/);

  const unrecognized = buildSubscriptionProxyNotice({ intent: 'disable', status: { state: 'weird' } });
  assert.equal(unrecognized.type, 'info');
  assert.doesNotMatch(unrecognized.text, /已停用/);

  // 运行态读取失败时保存提示也不能声称已经监听
  const saved = buildSubscriptionProxyNotice({
    intent: 'save',
    listenChanged: true,
    status: null
  });
  assert.doesNotMatch(saved.text, /重新监听/);
  assert.equal(saved.type, 'info');
});

test('模式配置保存后只有相同版本运行态才可确认应用', () => {
  const config = { mode: 'pool', revision: 8 };
  for (const status of [null, { state: 'stopped' }, { state: 'running', mode: 'pool', revision: 7 }, { state: 'running', mode: 'subscription', revision: 8 }]) {
    const notice = buildSubscriptionProxyNotice({ intent: 'save', modeChanged: true, config, status });
    assert.equal(notice.type, 'info');
    assert.match(notice.text, /刷新/);
  }
});

test('隐私关闭、仅检测、无规则各自显示真实状态，不统一说成已脱敏', () => {
  const disabled = describePrivacyForSubscription({ mode: 'disabled' });
  assert.equal(disabled.label, '关闭');
  assert.match(disabled.detail, /原样/);

  const detect = describePrivacyForSubscription({ mode: 'detect' });
  assert.equal(detect.label, '仅检测');
  assert.match(detect.detail, /不替换正文/);

  const noRules = describePrivacyForSubscription({ mode: 'redact', enabled_rules: 0 });
  assert.equal(noRules.label, '脱敏转发');
  assert.match(noRules.detail, /没有启用的规则/);

  const withRules = describePrivacyForSubscription({ mode: 'redact', enabled_rules: 5 });
  assert.match(withRules.detail, /5 条/);
  assert.doesNotMatch(withRules.detail, /没有启用的规则/);

  // 规则编译降级要显式提示，而不是当成完整脱敏
  assert.match(describePrivacyForSubscription({ mode: 'redact', enabled_rules: 2, status: 'degraded' }).detail, /未激活/);
  assert.match(subscriptionProxyPrivacyWarning({ status: 'degraded' }), /未激活/);
  assert.equal(subscriptionProxyPrivacyWarning({ status: 'ok' }), '');
});

test('配置片段只给用户级 openai_base_url，保留 provider 后缀', () => {
  const url = 'http://127.0.0.1:18089/subscription/secret-value/backend-api/codex';
  const snippet = buildCodexConfigSnippet(url);
  assert.match(snippet, /~\/\.codex\/config\.toml/);
  assert.match(snippet, new RegExp(`openai_base_url = "${url}"`));
  assert.match(snippet, /项目级同名配置不能覆盖/);
});

test('改监听地址后用内存密钥重建接入地址，格式与后端一致', () => {
  const secret = 'secret-value';
  // 与后端 service.BuildCodexSubscriptionProxyAccessURL 的格式逐字对齐
  assert.equal(
    buildSubscriptionProxyAccessURL({ listenHost: '127.0.0.1', listenPort: 18089 }, secret),
    'http://127.0.0.1:18089/subscription/secret-value/backend-api/codex'
  );
  assert.equal(
    buildSubscriptionProxyAccessURL({ listenHost: '127.0.0.1', listenPort: 19000 }, secret),
    'http://127.0.0.1:19000/subscription/secret-value/backend-api/codex'
  );
  // IPv6 回环要加方括号，与 net.JoinHostPort 一致
  assert.equal(
    buildSubscriptionProxyAccessURL({ listenHost: '::1', listenPort: 18089 }, secret),
    'http://[::1]:18089/subscription/secret-value/backend-api/codex'
  );
});

test('未知运行态不冒充运行中', () => {
  assert.equal(subscriptionProxyStateMeta('weird').label, '状态未知');
  assert.equal(subscriptionProxyStateMeta('running').label, '运行中');
});

test('只有主动停用才算“已停用”，过渡与故障态不算', () => {
  const disabled = { enabled: false };
  assert.equal(isSubscriptionProxyStoppedByUser({ config: disabled, status: { state: 'stopped' } }), true);
  // 意图启用却没起来不是“主动停用”
  assert.equal(isSubscriptionProxyStoppedByUser({ config: { enabled: true }, status: { state: 'stopped' } }), false);
  // 停用过程中、停用失败、启动失败都不算“已停稳的主动停用”
  assert.equal(isSubscriptionProxyStoppedByUser({ config: disabled, status: { state: 'draining' } }), false);
  assert.equal(isSubscriptionProxyStoppedByUser({ config: disabled, status: { state: 'failed' } }), false);
  assert.equal(isSubscriptionProxyStoppedByUser({ config: disabled, status: { state: 'starting' } }), false);
  // 配置或运行态没读到时不冒充
  assert.equal(isSubscriptionProxyStoppedByUser({ config: null, status: { state: 'stopped' } }), false);
  assert.equal(isSubscriptionProxyStoppedByUser({ config: disabled, status: null }), false);
  assert.equal(isSubscriptionProxyStoppedByUser(), false);
});

test('主动停用是正常终态，不与“未运行”混同', () => {
  const disabled = { enabled: false, mode: 'subscription' };
  // 用户主动关闭（enabled=false）后已停稳：说“已停用”，不能说成“未运行”
  assert.equal(resolveSubscriptionProxyDisplayState({ config: disabled, status: { state: 'stopped' } }).label, '已停用');
  // 意图启用却没起来才是“未运行”
  assert.equal(resolveSubscriptionProxyDisplayState({ config: { enabled: true }, status: { state: 'stopped' } }).label, '未运行');
  // 过渡与失败态各有准确文案，不能被“已停用”吞掉
  assert.equal(resolveSubscriptionProxyDisplayState({ config: disabled, status: { state: 'starting' } }).label, '启动中');
  assert.equal(resolveSubscriptionProxyDisplayState({ config: disabled, status: { state: 'draining' } }).label, '停用中');
  assert.equal(resolveSubscriptionProxyDisplayState({ config: disabled, status: { state: 'failed' } }).label, '启动失败');
  // 运行中仍按运行态呈现
  assert.equal(resolveSubscriptionProxyDisplayState({ config: { enabled: true }, status: { state: 'running' } }).label, '运行中');
  // 配置或运行态没读到时不冒充“已停用”
  assert.equal(resolveSubscriptionProxyDisplayState({ config: null, status: { state: 'stopped' } }).label, '未运行');
  assert.equal(resolveSubscriptionProxyDisplayState({ config: disabled, status: null }).label, '状态未知');
  assert.equal(resolveSubscriptionProxyDisplayState({ config: disabled, status: { state: 'weird' } }).label, '状态未知');
});
