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

// 可配置为强制走 API 的请求类别，与后端 endpoint.ClaudeCodeAPIRequestClasses 一致（main 不可选）
export const CLAUDE_CODE_REQUEST_CLASS_OPTIONS = [
  { value: 'subagent', label: '子代理', hint: 'Explore、Plan 等子代理的对话' },
  { value: 'auxiliary', label: '后台辅助', hint: '会话标题、分类、摘要等附带请求' },
  { value: 'compaction', label: '压缩', hint: '上下文压缩时的摘要请求，影响后续对话质量' },
  { value: 'workflow', label: 'Workflow', hint: 'workflow 内运行的代理' }
];

// 打开按请求类别路由时，没有已选类别则默认勾选子代理与后台辅助
export const DEFAULT_API_REQUEST_CLASSES = ['subagent', 'auxiliary'];

/**
 * 勾选 / 取消一个请求类别，结果按固定顺序排列
 * @param {string[]} selected
 * @param {string} value
 */
export const toggleRequestClass = (selected = [], value) => {
  const next = new Set(selected);
  if (next.has(value)) next.delete(value);
  else next.add(value);
  return CLAUDE_CODE_REQUEST_CLASS_OPTIONS.map((option) => option.value).filter((item) => next.has(item));
};

// Claude Code 只在设置该变量时才向自定义 base URL 发送 x-claude-code-request-class
export const GATEWAY_HINT_HEADERS_ENV = 'CLAUDE_CODE_GATEWAY_HINT_HEADERS';

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
 * @param {{hintHeaders?: boolean}} options hintHeaders 为 true 时带上请求类别头开关
 */
export const buildClaudeCodeSettingsSnippet = (baseUrl, { hintHeaders = false } = {}) => JSON.stringify({
  env: {
    ANTHROPIC_BASE_URL: baseUrl,
    ...(hintHeaders ? { [GATEWAY_HINT_HEADERS_ENV]: '1' } : {})
  }
}, null, 2);

/**
 * 首次试用命令：只对这一次启动的进程生效，不影响正在使用的 Claude Code
 * @param {string} baseUrl
 * @param {{hintHeaders?: boolean}} options
 */
export const buildClaudeCodeTrialCommand = (baseUrl, { hintHeaders = false } = {}) => (
  `${hintHeaders ? `${GATEWAY_HINT_HEADERS_ENV}=1 ` : ''}ANTHROPIC_BASE_URL=${baseUrl} claude`
);
