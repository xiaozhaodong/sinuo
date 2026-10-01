// Claude Code 接入的纯函数：模式说明、端口校验、状态文案、客户端配置片段与请求页按钮标签

export const CLAUDE_CODE_ENTRY_DEFAULT_PORT = 18088;

// 接入模式，与后端 endpoint.ClaudeCodeEntryMode* 一致；两种模式不混用
export const CLAUDE_CODE_ENTRY_MODE_OPTIONS = [
  {
    value: 'subscription',
    label: '订阅直通',
    shortHint: '用 claude.ai 登录直连 Anthropic 官方',
    hint: '全部请求带 Claude Code 自己的 claude.ai 登录直连 Anthropic，官方 5h / 周额度照常显示；不经过端点与模型路由。'
  },
  {
    value: 'endpoint',
    label: '端点',
    shortHint: '与主端口相同，转发到第三方端点',
    hint: '与主端口相同，按全局路由或模型路由使用第三方端点；订阅额度用完或暂停订阅时切到这里。'
  }
];

export const describeClaudeCodeEntryMode = (mode) => (
  CLAUDE_CODE_ENTRY_MODE_OPTIONS.find((option) => option.value === mode) || CLAUDE_CODE_ENTRY_MODE_OPTIONS[0]
);

/**
 * 解析端口输入；合法返回整数，否则返回 null
 * @param {string|number} value
 */
export const parseClaudeCodeEntryPort = (value) => {
  const text = String(value ?? '').trim();
  if (!/^\d+$/.test(text)) return null;
  const port = Number(text);
  return port >= 1 && port <= 65535 ? port : null;
};

/**
 * 入口状态徽章：启动失败优先，其次运行态，最后启用意图
 * @param {{enabled:boolean, running:boolean, error:string}|null} info
 */
export const describeClaudeCodeEntryState = (info) => {
  if (!info) return { label: '加载中', tone: 'tone-slate' };
  if (info.error) return { label: '启动失败', tone: 'tone-rose' };
  if (info.running) return { label: '运行中', tone: 'tone-emerald' };
  if (info.enabled) return { label: '未运行', tone: 'tone-amber' };
  return { label: '未启用', tone: 'tone-slate' };
};

/**
 * 写入 ~/.claude/settings.json 的配置片段
 * @param {string} baseUrl
 */
export const buildClaudeCodeSettingsSnippet = (baseUrl) => JSON.stringify({
  env: { ANTHROPIC_BASE_URL: baseUrl }
}, null, 2);

/**
 * 首次试用命令：只对这一次启动的进程生效，不影响正在使用的 Claude Code
 * @param {string} baseUrl
 */
export const buildClaudeCodeTrialCommand = (baseUrl) => `ANTHROPIC_BASE_URL=${baseUrl} claude`;

/**
 * 请求页 Claude 按钮的主标签，回答「经入口的 Claude Code 请求现在走哪」。
 * 入口未启用时只有主端口，标签就是端点策略；入口异常时由入口状态抢标签。
 * @param {{info:object|null, loading:boolean, error:string, routeLabel:string}} state
 * @returns {{label:string, detail:string}}
 */
export const resolveClaudeAccessLabel = ({ info = null, loading = false, error = '', routeLabel = 'Auto' } = {}) => {
  if (!info) {
    if (loading) return { label: '读取中', detail: '' };
    return { label: routeLabel, detail: error ? `入口状态读取失败：${error}` : '' };
  }
  if (!info.enabled) return { label: routeLabel, detail: 'Claude Code 入口未启用' };
  if (info.error) return { label: '入口启动失败', detail: info.error };
  if (!info.running) return { label: '入口未运行', detail: '' };
  if (info.mode === 'endpoint') return { label: `端点 · ${routeLabel}`, detail: '' };
  return { label: '订阅', detail: '' };
};
