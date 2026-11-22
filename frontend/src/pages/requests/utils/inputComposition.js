import { fullInputTokens, requestInputIncludesCache } from './requestInputUsage.js';
import { UNSETTLED_STATUSES } from './constants.js';

// 按最大余数取整，确保条目、分类和可视化格数各自加总一致。
export function distribute(weights, amount) {
  if (!weights.length || amount <= 0) return weights.map(() => 0);
  const sum = weights.reduce((a, b) => a + b, 0);
  if (sum <= 0) return weights.map(() => 0);
  const exact = weights.map((w) => (w / sum) * amount);
  const units = exact.map(Math.floor);
  const extra = amount - units.reduce((a, b) => a + b, 0);
  const order = exact
    .map((value, i) => ({ i, fraction: value - units[i] }))
    .sort((a, b) => b.fraction - a.fraction);
  for (const { i } of order.slice(0, extra)) units[i]++;
  return units;
}

// 点阵分配：只要分类非零且总格数足够，保证每个非零分类至少占 1 格，避免小项彻底消失。
export function distributeCells(weights, amount) {
  if (!weights.length || amount <= 0) return weights.map(() => 0);
  const positives = weights.filter((w) => w > 0).length;
  if (positives === 0) return weights.map(() => 0);
  if (amount <= positives) {
    return distribute(weights, amount);
  }
  const base = weights.map((w) => (w > 0 ? 1 : 0));
  const remaining = amount - positives;
  const extra = distribute(weights, remaining);
  return base.map((count, i) => count + extra[i]);
}

export function formatExact(value) {
  return Math.round(value || 0).toLocaleString('en-US');
}

export function formatCompact(value) {
  const n = Number(value) || 0;
  if (n >= 1_000_000) return `${(n / 1_000_000).toFixed(2).replace(/\.?0+$/, '')}M`;
  if (n >= 1_000) return `${(n / 1_000).toFixed(1).replace(/\.0$/, '')}K`;
  return String(Math.round(n));
}

export function formatPercent(value, max) {
  if (!max || max <= 0) return '0%';
  const pct = (value * 100) / max;
  if (pct > 0 && pct < 0.1) return '<0.1%';
  if (value < max && pct >= 99.95) return '>99.9%';
  return `${pct.toFixed(1).replace(/\.0$/, '')}%`;
}

const groupNames = {
  files: '文件内容', tools: '工具定义', chat: '对话历史', results: '工具结果',
  system: '系统提示', memory: '项目指令', skills: '技能说明', unassigned: '输入差额'
};

// 同名工具在定义和结果中分别占用输入。仅转换显示名，原标识仍可悬停查看。
const messageTools = {
  Agent: '调用子代理', ListAgents: '列出子代理', AskUserQuestion: '询问用户',
  Bash: '执行终端命令', Read: '读取文件', Write: '写入文件', Edit: '编辑文件',
  Glob: '按名称查找文件', Grep: '搜索文件内容', NotebookEdit: '编辑笔记本',
  DeferredToolPlaceholder: '延迟加载工具占位', ToolSearch: '查找可用工具', Skill: '调用技能',
  ReportFindings: '汇报发现', ScheduleWakeup: '定时唤醒', Workflow: '运行工作流',
  WebSearch: '网页搜索', web_search: '服务端网页搜索', WebFetch: '读取网页', web_fetch: '服务端网页读取',
  Task: '委派任务', TaskCreate: '创建任务', TaskGet: '查看任务', TaskList: '列出任务',
  TaskUpdate: '更新任务', TaskOutput: '读取任务输出', TaskStop: '停止任务', TodoWrite: '更新待办清单',
  EnterPlanMode: '进入规划模式', ExitPlanMode: '退出规划模式'
};

const codexTools = {
  collaboration: '子代理协作', 'MCP · cua_repl': 'MCP · 电脑操作',
  exec_command: '执行终端命令', write_stdin: '与终端会话交互',
  request_user_input_async: '询问用户（继续执行）', request_user_input: '询问用户（等待答复）',
  wait: '等待脚本结果', send_message_to_user_async: '向用户发消息', clock: '时间与定时等待'
};

function itemLabels({ group, name }, isMessages) {
  const callSuffix = ' 工具调用';
  const isCall = group === 'chat' && name.endsWith(callSuffix);
  if (!isCall && group !== 'tools' && group !== 'results') return { name, rawName: name };

  const rawName = isCall ? name.slice(0, -callSuffix.length) : name;
  if (rawName === 'exec' || rawName === 'functions.exec') {
    const kind = isCall ? '调用参数' : group === 'tools' ? '定义' : '输出（未细分）';
    return { name: `执行脚本 · ${kind}`, rawName };
  }
  const key = isMessages ? rawName : rawName.replace(/^functions\./, '');
  const labels = isMessages ? messageTools : codexTools;
  if (!Object.hasOwn(labels, key)) return { name, rawName };

  let display = labels[key];
  if (isCall) display += ' · 调用参数';
  else if (isMessages) display += group === 'tools' ? ' · 工具定义' : ' · 工具结果';
  else if (group === 'results') display += ' · 输出';
  return { name: display, rawName };
}

const windowSources = {
  codex_catalog: '请求当时的 Codex 模型目录',
  model_spec: '请求当时的厂商模型规格',
  historical_model_spec: '按历史记录中的模型和现有厂商规格推算'
};

// 摘要只保存可确认的协议线索；上游没有返回按原因拆分的 token 数。
const partialReasonLabels = new Map([
  ['history_reference', '请求引用了上游历史'],
  ['encrypted_reasoning', '上下文含加密推理'], ['sealed_tool_result', '工具结果含加密内容'],
  ['reasoning_history', '推理历史没有可估算的明文'],
  ['image', '请求含图片'], ['attachment', '请求含附件'], ['audio', '请求含音频'],
  ['auto_truncation', '上游可自动截断历史'], ['compaction', '上下文含压缩记录'], ['redacted_thinking', '请求含隐藏思考块'],
  ['ws_create', 'WebSocket 仅采集了创建帧'],
  ['body_limit', '请求体超过 8 MiB 采集上限'],
  ['unknown_block', '存在未识别的输入块']
]);

const toolEchoNotes = new Map([
  ['unreported', '已采集的上游响应未提供工具清单。'],
  ['empty', '上游回显了空工具清单。'],
  ['unavailable', '本次工具回显超出采集范围或格式无法识别。'],
  ['partial', '仅估算回显清单中可识别的工具条目。']
]);

function attributionNote(summary, reasons, total, visible, echoed, inherited, cacheRead) {
  const labels = reasons.map((key) => partialReasonLabels.get(key)).filter(Boolean);
  const evidence = labels.length ? labels.join('，') + '。' : (summary.partial
    ? '这条摘要没有记录具体原因。' : '没有可估算的输入条目。');
  const echoNote = toolEchoNotes.get(summary.tool_echo);
  if (total <= visible + echoed + inherited) return [evidence, echoNote].filter(Boolean).join('\n');
  const echo = echoed ? ` − 上游回显工具估算 ${formatExact(echoed)}` : '';
  const parent = inherited ? ` − 父响应摘要估算 ${formatExact(inherited)}` : '';
  const calculation = `上游输入 ${formatExact(total)} − 可见估算 ${formatExact(visible)}${echo}${parent} = 差额 ${formatExact(total - visible - echoed - inherited)} tokens。`;
  const cache = Number.isSafeInteger(cacheRead) && cacheRead > 0 && cacheRead <= total
    ? `上游输入已含缓存读取 ${formatExact(cacheRead)} tokens。` : '';
  return ['差额已包含在输入总量里。', calculation, evidence + '上游未报告差额的内容明细。', echoNote, cache].filter(Boolean).join('\n');
}

export function inputComposition(summary, request) {
  if (summary?.version !== 1 || !Array.isArray(summary.items)) return null;
  const validItems = (rows) => Array.isArray(rows) ? rows.filter((i) => i && i.group !== 'unassigned'
    && Object.hasOwn(groupNames, i.group) && typeof i.name === 'string' && i.name
    && Number.isSafeInteger(i.estimate) && i.estimate > 0) : [];
  const items = validItems(summary.items);
  const echoes = ['reported', 'partial'].includes(summary.tool_echo) && !items.some((i) => i.group === 'tools')
    ? validItems(summary.echo_items).filter((i) => i.group === 'tools') : [];
  const history = validItems(summary.history_items);
  const sum = (rows) => rows.reduce((n, i) => n + i.estimate, 0);
  const estimate = sum(items);
  const echoEstimate = sum(echoes);
  const historyEstimate = sum(history);
  const includesCache = requestInputIncludesCache(request.requestFamily, request.path);
  const fullInput = fullInputTokens(includesCache, request.inputTokens, request.cacheCreationTokens, request.cacheReadTokens);
  // 旧代理在登记时就把 usage_known 置真，失败或无 usage 的全零行不能校准估算值。
  const counted = request.usageKnown === true && fullInput !== null
    && (fullInput > 0 || request.outputTokens > 0
      || (!UNSETTLED_STATUSES.has(request.status) && request.requestSource === 'codex_subscription_proxy'));
  const total = counted ? fullInput : estimate + echoEstimate + historyEstimate;
  const pendingTotal = !counted && total === 0 && summary.partial === true;
  const visible = (summary.partial || historyEstimate || echoEstimate) ? Math.min(total, estimate) : (estimate ? total : 0);
  const echoed = Math.min(echoEstimate, total - visible);
  const inherited = Math.min(historyEstimate, total - visible - echoed);
  const attributed = visible + echoed + inherited;
  const groups = new Map();
  const add = (key, name, tokens, rawName = name, source) => {
    const historyTokens = source === 'history' ? tokens : 0;
    const echoTokens = source === 'echo' ? tokens : 0;
    if (!groups.has(key)) groups.set(key, { key, name: groupNames[key], color: `var(--ctx-${key})`, tokens: 0, items: [] });
    const g = groups.get(key);
    g.tokens += tokens;
    const existing = g.items.find((item) => item.rawName === rawName);
    if (existing) {
      existing.tokens += tokens;
      existing.historyTokens += historyTokens;
      existing.echoTokens += echoTokens;
    } else {
      g.items.push({ name, rawName, tokens, historyTokens, echoTokens });
    }
  };
  const isMessages = request.requestFamily === 'claude' || request.path === '/v1/messages';
  function addRows(rows, amount, source) {
    const units = distribute(rows.map((i) => i.estimate), amount);
    rows.forEach((i, idx) => {
      const tokens = units[idx];
      if (source !== 'current' && !tokens) return;
      const { name, rawName } = itemLabels(i, isMessages);
      add(i.group, name, tokens, rawName, source);
    });
  }
  addRows(items, visible, 'current');
  addRows(echoes, echoed, 'echo');
  addRows(history, inherited, 'history');
  const reasons = Array.isArray(summary.partial_reasons) ? summary.partial_reasons : [];
  if (total > attributed) {
    const historyOnly = reasons.includes('history_reference')
      && reasons.every((key) => key === 'history_reference' || key === 'ws_create');
    const name = historyOnly ? '历史续接差额' : groupNames.unassigned;
    add('unassigned', name, total - attributed);
    if (historyOnly) groups.get('unassigned').name = name;
  }
  for (const g of groups.values()) g.items.sort((a, b) => b.tokens - a.tokens);
  const limit = Object.hasOwn(windowSources, summary.window_source) && Number.isSafeInteger(summary.window) && summary.window > 0 ? summary.window : null;
  return { groups: [...groups.values()].sort((a, b) => b.tokens - a.tokens), total, window: limit,
    windowDescription: limit ? `${formatExact(limit)} tokens · ${windowSources[summary.window_source]}` : '',
    counted, pendingTotal, inherited, attributionNote: total > attributed || pendingTotal
      ? attributionNote(summary, reasons, total, visible, echoed, inherited, request.cacheReadTokens) : null,
    partial: summary.partial === true, model: summary.window_model || request.responseModel || request.upstreamModel || request.requestModel || request.model };
}
