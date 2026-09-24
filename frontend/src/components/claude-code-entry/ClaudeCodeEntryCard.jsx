// Claude Code 客户端接入卡片：专用 loopback 入口的启停、端口、接入模式与客户端配置。
// 接入模式二选一：订阅直通直连 Anthropic，端点与主端口一样走第三方端点；两者不混用。

import { useState } from 'react';
import { AlertCircle, CheckCircle2, Copy, RefreshCw, TerminalSquare } from 'lucide-react';
import { Button } from '@components/ui';
import useConfirm from '@hooks/useConfirm.js';
import { copyTextToClipboard } from '@pages/requests/components/clipboard.js';
import useClaudeCodeEntry from './useClaudeCodeEntry.js';
import {
  buildClaudeCodeSettingsSnippet,
  buildClaudeCodeTrialCommand,
  CLAUDE_CODE_ENTRY_MODE_OPTIONS,
  describeClaudeCodeEntryMode,
  describeClaudeCodeEntryState,
  parseClaudeCodeEntryPort
} from './claudeCodeEntryViewModel.js';

const ModeSegmented = ({ value, disabled, onChange }) => (
  <div role="radiogroup" aria-label="Claude Code 接入模式" className="inline-flex rounded-lg border border-line bg-surface-sub p-0.5">
    {CLAUDE_CODE_ENTRY_MODE_OPTIONS.map((option) => (
      <button
        key={option.value}
        type="button"
        role="radio"
        aria-checked={value === option.value}
        disabled={disabled}
        onClick={() => value !== option.value && onChange(option.value)}
        className={`rounded-md px-3 py-1.5 text-xs font-medium transition-colors ${value === option.value ? 'bg-surface text-accent shadow-sm' : 'text-fg-muted hover:text-fg-body'} ${disabled ? 'cursor-not-allowed opacity-50' : ''}`}
      >
        {option.label}
      </button>
    ))}
  </div>
);

const CopyBlock = ({ label, text, onCopied }) => (
  <div className="space-y-1.5">
    <div className="flex items-center justify-between gap-3">
      <span className="text-xs font-medium text-fg-body">{label}</span>
      <button
        type="button"
        onClick={async () => onCopied(await copyTextToClipboard(text, label), label)}
        className="inline-flex items-center gap-1 rounded-md border border-line px-2 py-1 text-xs text-fg-muted hover:border-accent-line hover:text-accent-fg"
      >
        <Copy size={12} />复制
      </button>
    </div>
    <pre className="overflow-x-auto rounded-lg border border-line bg-surface-sub px-3 py-2 font-mono text-xs text-fg-body">{text}</pre>
  </div>
);

const ClaudeCodeEntryCard = () => {
  const { available, info, busy, error, refresh, save } = useClaudeCodeEntry();
  const { confirm, confirmDialog } = useConfirm();
  // 端口草稿只在用户编辑时存在；未编辑时直接显示已保存端口（含其他页面保存的新值）
  const [portDraft, setPortDraft] = useState(null);
  const [notice, setNotice] = useState('');

  const portInput = portDraft ?? String(info?.listenPort || '');
  const parsedPort = parseClaudeCodeEntryPort(portInput);
  const portDirty = Boolean(info) && portDraft !== null && String(info.listenPort) !== portDraft.trim();
  const state = describeClaudeCodeEntryState(info);
  const controlsDisabled = busy || !info;
  const onCopied = (copied, label) => setNotice(copied ? `已复制${label}` : '');

  const toggleEnabled = async () => {
    if (info.enabled && !(await confirm({
      title: '停用 Claude Code 入口',
      message: '停用后入口不再监听，仍指向它的 Claude Code 会连接失败；Switchboard 不会修改 Claude Code 的配置。确定停用吗？',
      confirmText: '停用',
      danger: true
    }))) {
      return;
    }
    save({ enabled: !info.enabled });
  };

  const savePort = async () => {
    if (await save({ listenPort: parsedPort })) setPortDraft(null);
  };

  return (
    <div className="rounded-2xl border border-line bg-surface shadow-sm">
      <div className="flex items-start justify-between gap-4 border-b border-line-soft px-5 py-4">
        <div className="flex min-w-0 items-start gap-3">
          <div className="rounded-lg bg-accent-soft p-2 text-accent"><TerminalSquare size={18} /></div>
          <div className="min-w-0">
            <div className="flex items-center gap-2">
              <h2 className="text-sm font-semibold text-fg">Claude Code 客户端接入</h2>
              <span className={`${state.tone} rounded-full border px-2 py-0.5 text-[11px] font-medium`}>{state.label}</span>
            </div>
            <p className="mt-1 text-xs text-fg-muted">
              Claude Code 用本机 claude.ai 登录时，把 ANTHROPIC_BASE_URL 指向这个入口。订阅直通与第三方端点二选一、不混用；主端口始终使用第三方端点。
            </p>
          </div>
        </div>
        <Button variant="secondary" size="sm" icon={RefreshCw} onClick={refresh} disabled={busy || !available} className="shrink-0">刷新</Button>
      </div>

      {!available ? (
        <div className="px-5 py-4 text-xs text-fg-subtle">仅桌面端可用。</div>
      ) : (
        <div className="space-y-4 px-5 py-4">
          {error && (
            <div className="flex items-start gap-2 rounded-lg border border-danger-line bg-danger-soft px-3 py-2 text-xs text-danger">
              <AlertCircle size={14} className="mt-0.5 shrink-0" />{error}
            </div>
          )}
          {info?.error && (
            <div className="flex items-start gap-2 rounded-lg border border-danger-line bg-danger-soft px-3 py-2 text-xs text-danger">
              <AlertCircle size={14} className="mt-0.5 shrink-0" />入口未能启动：{info.error}
            </div>
          )}

          <div className="flex flex-wrap items-center justify-between gap-4">
            <div className="flex items-center gap-3">
              <button
                type="button"
                role="switch"
                aria-checked={info?.enabled === true}
                aria-label="启用 Claude Code 入口"
                disabled={controlsDisabled}
                onClick={toggleEnabled}
                className={`relative inline-flex h-6 w-11 shrink-0 rounded-full transition-colors ${info?.enabled ? 'bg-indigo-600' : 'bg-surface-emph'} ${controlsDisabled ? 'cursor-not-allowed opacity-50' : ''}`}
              >
                <span className={`absolute left-[2px] top-0.5 h-5 w-5 rounded-full bg-surface shadow-sm transition-transform ${info?.enabled ? 'translate-x-full' : ''}`} />
              </button>
              <span className="text-sm text-fg-body">启用入口</span>
            </div>
            <div className="flex items-center gap-2">
              <span className="text-xs text-fg-muted">127.0.0.1 :</span>
              <input
                aria-label="Claude Code 入口端口"
                value={portInput}
                onChange={(event) => setPortDraft(event.target.value)}
                disabled={controlsDisabled}
                inputMode="numeric"
                className="w-24 rounded-lg border border-line bg-surface px-2 py-1.5 text-sm text-fg outline-none focus:border-accent-ring disabled:bg-surface-mut"
              />
              <Button size="sm" variant="secondary" onClick={savePort} disabled={busy || !portDirty || parsedPort === null}>
                保存端口
              </Button>
            </div>
          </div>
          {portDirty && parsedPort === null && <p className="text-xs text-danger">端口必须是 1-65535 的整数</p>}

          <div className="space-y-1.5 border-t border-line-soft pt-4">
            <div className="flex flex-wrap items-center justify-between gap-3">
              <span className="text-sm text-fg-body">接入模式</span>
              <ModeSegmented value={info?.mode || 'subscription'} disabled={controlsDisabled} onChange={(mode) => save({ mode })} />
            </div>
            <p className="text-xs text-fg-subtle">{describeClaudeCodeEntryMode(info?.mode).hint} 切换立即对新请求生效，进行中的请求不受影响；请求追踪页也可快捷切换。</p>
          </div>
          <p className="text-xs text-fg-subtle">端口写进 Claude Code 配置后不会自动更换；被占用时入口启动失败，请换一个端口。</p>

          {info?.baseUrl && (
            <div className="space-y-3 rounded-xl border border-line p-3">
              <CopyBlock label="首次试用（另开终端运行，不影响正在用的 Claude Code）" text={buildClaudeCodeTrialCommand(info.baseUrl)} onCopied={onCopied} />
              <CopyBlock label="确认无误后写入 ~/.claude/settings.json" text={buildClaudeCodeSettingsSnippet(info.baseUrl)} onCopied={onCopied} />
              <p className="text-xs leading-5 text-fg-subtle">
                不要同时设置 ANTHROPIC_AUTH_TOKEN、ANTHROPIC_API_KEY 或 apiKeyHelper，否则会顶替订阅登录、改按 token 计费。Switchboard 未运行时 Claude Code 无法请求，删掉 ANTHROPIC_BASE_URL 即可恢复直连。
              </p>
              {notice && <p className="flex items-center gap-1 text-xs text-success"><CheckCircle2 size={12} />{notice}</p>}
            </div>
          )}
        </div>
      )}
      {confirmDialog}
    </div>
  );
};

export default ClaudeCodeEntryCard;
