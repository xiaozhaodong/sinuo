// Claude Code 专用入口卡片的纯函数：端口校验、状态文案与客户端配置片段

export const CLAUDE_CODE_ENTRY_DEFAULT_PORT = 18088;

// 入口全局模式，与后端 endpoint.ClaudeCodeEntryMode* 一致
export const CLAUDE_CODE_ENTRY_MODE_OPTIONS = [
  { value: 'auto', label: '按模型路由', hint: '按模型路由分配：映射里订阅直通端点排在最前的模型走订阅，其余走第三方。' },
  { value: 'subscription', label: '仅订阅', hint: '只用订阅直通端点；模型的端点集合里没有订阅端点时直接报错，不会改走第三方。' },
  { value: 'api', label: '仅 API', hint: '只用第三方端点，订阅额度用完时切到这里；相当于 Codex 的池模式。' }
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
