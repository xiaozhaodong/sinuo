// 侧边栏底部的代理状态行：主端口固定一行，Claude Code / Codex 独立入口只在运行中时各加一行，
// 未启用、启动失败、启停过渡都不占行（失败原因到各自的客户端接入页看）。
// 侧边栏带端口时文字只剩约 118px，「Claude Code · 订阅直通」放不下，
// 所以行内写模式简称，完整模式名与监听地址放悬停提示。

import { describeClaudeCodeEntryMode } from '../claude-code-entry/claudeCodeEntryViewModel.js';
import { SUBSCRIPTION_PROXY_MODE_OPTIONS } from '../codex-access/subscriptionProxyViewModel.js';

const DOT = { success: 'bg-success-solid', warn: 'bg-warn-solid', danger: 'bg-danger-solid' };

const CLAUDE_MODE_SHORT = { subscription: '订阅', endpoint: '端点' };
const CODEX_MODE_SHORT = { subscription: '订阅', pool: '账号池' };

const MAIN_PORT_TITLE = '主端口的代理网关状态';

// 事件通道未连通时先报通道问题，连通后以代理网关真实状态为准
export const resolveMainPortStatus = (connectionStatus, { running = false, port = 0 } = {}) => {
  const row = { key: 'main', name: '主端口', detail: '', port: 0, title: MAIN_PORT_TITLE };
  if (connectionStatus !== 'connected') {
    return { ...row, dot: DOT.warn, name: connectionStatus === 'connecting' ? '连接中…' : '事件通道断开' };
  }
  if (running && port > 0) return { ...row, dot: DOT.success, port };
  return { ...row, dot: DOT.danger, name: '主端口未运行' };
};

// 非 Wails 环境读不到状态时 info 为 null，同样不占行
export const resolveClaudeCodeEntryStatus = (info) => {
  if (!info?.enabled || !info.running) return null;
  const modeLabel = describeClaudeCodeEntryMode(info.mode).label;
  return {
    key: 'claude-code',
    dot: DOT.success,
    name: 'Claude Code',
    detail: CLAUDE_MODE_SHORT[info.mode] || modeLabel,
    port: info.listenPort,
    title: `Claude Code 入口运行中 · ${modeLabel}${info.baseUrl ? `\n${info.baseUrl}` : ''}`
  };
};

const portFromListenAddr = (addr) => Number(String(addr || '').split(':').pop()) || 0;

export const resolveCodexEntryStatus = ({ config, status } = {}) => {
  if (status?.state !== 'running') return null;
  const mode = status.mode || config?.mode;
  const modeLabel = SUBSCRIPTION_PROXY_MODE_OPTIONS.find((option) => option.value === mode)?.label || mode;
  return {
    key: 'codex',
    dot: DOT.success,
    name: 'Codex',
    detail: CODEX_MODE_SHORT[mode] || modeLabel,
    port: portFromListenAddr(status.listenAddr) || config?.listenPort || 0,
    title: `Codex 独立入口运行中 · ${modeLabel}${status.listenAddr ? `\n${status.listenAddr}` : ''}`
  };
};
