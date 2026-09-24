// Claude Code 专用入口卡片的纯函数：端口校验、状态文案与客户端配置片段

export const CLAUDE_CODE_ENTRY_DEFAULT_PORT = 18088;

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
