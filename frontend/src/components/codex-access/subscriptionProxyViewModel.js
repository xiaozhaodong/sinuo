// ============================================
// Codex 客户端接入与请求追踪共用的展示逻辑
//
// 只做展示映射、前置校验与文案组装，不持有访问密钥：
// 明文密钥仅由卡片的组件内存状态承载，不落 localStorage、不进日志。
// ============================================

// 该分类受后端保护，通用设置保存/重置入口一律拒绝，界面不能把它当普通分类渲染。
export const SUBSCRIPTION_PROXY_CONFIG_CATEGORY = 'codex_subscription_proxy';

export const SUBSCRIPTION_PROXY_MODE_OPTIONS = [
  { value: 'subscription', label: '订阅直通' },
  { value: 'pool', label: '账号池' }
];

// 池模式不能暗示“输入内容全部匿名”：只剥离客户端身份头，正文仍按当前隐私设置处理。
export const SUBSCRIPTION_PROXY_MODE_HINTS = {
  subscription: 'Responses 使用客户端自带的本次凭据直连 ChatGPT Codex 后端，不经过账号池。',
  pool: '仅 Responses 发往所选池上游；客户端身份头始终剥离，正文按当前隐私设置处理。'
};

export const SUBSCRIPTION_PROXY_STATE_META = {
  stopped: { label: '未运行', tone: 'neutral' },
  starting: { label: '启动中', tone: 'warn' },
  running: { label: '运行中', tone: 'success' },
  draining: { label: '停用中', tone: 'warn' },
  failed: { label: '启动失败', tone: 'danger' }
};

export const subscriptionProxyStateMeta = (state) =>
  SUBSCRIPTION_PROXY_STATE_META[state] || { label: '状态未知', tone: 'neutral' };

// 与后端 NormalizeCodexSubscriptionProxyInput 同口径：只允许回环 IP 字面量。
const LOOPBACK_IPV4 = /^127(\.\d{1,3}){3}$/;

export const isLoopbackHost = (host) => {
  const normalized = String(host ?? '').trim().replace(/^\[/, '').replace(/\]$/, '');
  return normalized === '::1' || LOOPBACK_IPV4.test(normalized);
};

// 先给即时反馈；最终判定仍以后端校验为准。
export const validateSubscriptionProxyForm = ({ listenHost, listenPort } = {}) => {
  const host = String(listenHost ?? '').trim();
  if (!host) return '监听地址不能为空';
  if (!isLoopbackHost(host)) return '监听地址只允许回环地址（127.0.0.1 或 ::1）';
  const port = Number(listenPort);
  if (!Number.isInteger(port) || port < 1 || port > 65535) return '端口必须是 1-65535 之间的整数';
  return '';
};

// 运行中（含启动/停用过渡）后端不接受监听地址变更：必须先停用再改，不暗中重启。
const LISTEN_LOCKED_STATES = new Set(['running', 'starting', 'draining']);
export const isListenLocked = (status) => LISTEN_LOCKED_STATES.has(status?.state);

// 模式切换的生效边界：不承诺“下一轮必定切换”，已有 WS 保持到重连。
export const MODE_EFFECT_NOTICE =
  'HTTP 从下一请求生效，已建立的 WS 在重连后生效；其他接口继续订阅透传。';

// revision 冲突只提示刷新，不自动覆盖。
export const isRevisionConflict = (message) => /已被其他写入|revision\s*=/i.test(String(message ?? ''));

export const buildCodexConfigSnippet = (accessUrl) => [
  '# ~/.codex/config.toml（用户级；项目级同名配置不能覆盖此地址）',
  `openai_base_url = "${accessUrl}"`
].join('\n');

// 与后端 service.BuildCodexSubscriptionProxyAccessURL 同格式，供「保存后监听地址变了、
// 但普通保存不再返回明文」时用内存中的密钥重建地址；格式由两侧测试同时钉住。
export const buildSubscriptionProxyAccessURL = (config, secret) => {
  const host = String(config?.listenHost ?? '').trim();
  const port = Number(config?.listenPort ?? 0);
  const authority = host.includes(':') ? `[${host}]:${port}` : `${host}:${port}`;
  return `http://${authority}/subscription/${secret}/backend-api/codex`;
};

const PRIVACY_MODE_LABELS = {
  disabled: '关闭',
  detect: '仅检测',
  redact: '脱敏转发'
};

// 关闭、仅检测、无适用规则都能正常运行，不能统一说成“已脱敏”。
export const describePrivacyForSubscription = (privacy) => {
  const mode = privacy?.mode;
  const label = PRIVACY_MODE_LABELS[mode] || '状态未知';
  const degradedNote = privacy?.status === 'degraded' ? '部分规则未激活，实际范围可能更小。' : '';

  if (mode === 'disabled') {
    return { label, detail: '正文原样发往所选上游，不做替换。' };
  }
  if (mode === 'detect') {
    return { label, detail: '扫描并记录安全摘要，不替换正文。' };
  }
  if (mode === 'redact') {
    const rules = Number(privacy?.enabled_rules ?? 0);
    const detail = rules > 0
      ? `按当前 ${rules} 条已启用规则替换命中内容；未命中的字节保持原样。`
      : '当前没有启用的规则，正文会原样发往上游。';
    return { label, detail: `${detail}${degradedNote}` };
  }
  return { label, detail: '未读到隐私设置，请到隐私保护页确认。' };
};

// 隐私不阻止启用：任何模式都可以运行，只在界面上提示是否生效。
export const subscriptionProxyPrivacyWarning = (privacy) =>
  privacy?.status === 'degraded' ? '部分隐私规则未激活，脱敏范围可能小于预期。' : '';

// 保存成功不等于启动成功：这里一律按独立读回的运行态组装提示。
export const buildSubscriptionProxyNotice = ({
  intent,
  config,
  status,
  modeChanged = false,
  listenChanged = false
} = {}) => {
  const state = status?.state;
  const lastError = status?.lastError || '未知错误';
  const wsCount = Number(status?.activeWebSockets ?? 0);

  if (intent === 'disable') {
    if (state === 'failed') {
      return { type: 'error', text: `停用意图已保存，但停用过程报错：${lastError}` };
    }
    if (state === 'stopped') {
      return { type: 'success', text: '已停用：独立入口不再监听，原有入口与其他服务不受影响。' };
    }
    if (state === 'running' || state === 'starting' || state === 'draining') {
      return { type: 'info', text: '停用意图已保存，独立入口正在关闭。' };
    }
    // 运行态没读到（或值不认识）时不能确认已停用：只报意图已保存。
    return { type: 'info', text: '停用意图已保存，尚未读到运行态；请刷新运行态确认。' };
  }

  if (intent === 'enable') {
    if (state === 'running') {
      return { type: 'success', text: `已启用，当前监听 ${status?.listenAddr || '（地址读取中）'}。` };
    }
    if (state === 'failed') {
      return { type: 'error', text: `配置已保存，但独立入口启动失败：${lastError}` };
    }
    return { type: 'info', text: '启用意图已保存，运行态尚未显示监听；可刷新运行态确认。' };
  }

  if (intent === 'rotate') {
    return { type: 'success', text: '访问密钥已轮换：旧接入地址立即失效，请把新地址重新填入 Codex 配置。' };
  }

  if (!SUBSCRIPTION_PROXY_STATE_META[state]) {
    return { type: 'info', text: '配置已保存，尚未读到有效运行态；请刷新确认。', action: 'refresh' };
  }
  if (modeChanged && state !== 'failed' && (
    state !== 'running' || (config && (status.mode !== config.mode || status.revision !== config.revision))
  )) {
    return { type: 'info', text: '模式配置已保存，但运行态尚未确认应用此模式；请刷新确认。已有 WS 在重连后生效。', action: 'refresh' };
  }

  const parts = [];
  if (listenChanged) {
    parts.push(state === 'running' ? '监听地址已保存并重新监听。' : '监听地址已保存。');
  }
  if (modeChanged) {
    parts.push(wsCount > 0
      ? `模式已保存。${MODE_EFFECT_NOTICE}当前底层 WS 连接数为 ${wsCount}（包含上下游两侧）。`
      : `模式已保存。${MODE_EFFECT_NOTICE}`);
  }
  if (state === 'failed') {
    return { type: 'error', text: `配置已保存，但独立入口启动失败：${lastError}` };
  }
  return { type: 'success', text: parts.length > 0 ? parts.join('') : '配置已保存。' };
};
