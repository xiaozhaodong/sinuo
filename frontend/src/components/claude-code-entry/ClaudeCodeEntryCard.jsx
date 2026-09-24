// Claude Code 接入卡片：专用 loopback 入口的启停、端口与客户端配置
// 只有经此入口进来的请求才能使用「订阅直通」端点；主端口行为不变。

import { useCallback, useEffect, useState } from 'react';
import { AlertCircle, CheckCircle2, Copy, RefreshCw, TerminalSquare } from 'lucide-react';
import { Button } from '@components/ui';
import { copyTextToClipboard } from '@pages/requests/components/clipboard.js';
import { resolveErrorMessage } from '@utils/errors.js';
import { getClaudeCodeEntryStatus, isWailsEnvironment, saveClaudeCodeEntrySettings } from '@utils/wailsApi.js';
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
  const available = isWailsEnvironment();
  const [info, setInfo] = useState(null);
  const [portInput, setPortInput] = useState('');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  const [notice, setNotice] = useState('');

  const applyInfo = (next) => {
    setInfo(next);
    setPortInput(String(next.listenPort || ''));
  };

  const load = useCallback(async () => {
    if (!available) return;
    setError('');
    try {
      applyInfo(await getClaudeCodeEntryStatus());
    } catch (err) {
      setError(resolveErrorMessage(err, '读取 Claude Code 入口状态失败'));
    }
  }, [available]);

  useEffect(() => {
    load();
  }, [load]);

  const save = async (patch) => {
    if (!info) return;
    setBusy(true);
    setError('');
    setNotice('');
    try {
      applyInfo(await saveClaudeCodeEntrySettings({ enabled: info.enabled, listenPort: info.listenPort, mode: info.mode, ...patch }));
    } catch (err) {
      setError(resolveErrorMessage(err, '保存 Claude Code 入口配置失败'));
    } finally {
      setBusy(false);
    }
  };

  const parsedPort = parseClaudeCodeEntryPort(portInput);
  const portDirty = info && String(info.listenPort) !== portInput.trim();
  const state = describeClaudeCodeEntryState(info);
  const onCopied = (copied, label) => setNotice(copied ? `已复制${label}` : '');

  return (
    <div className="rounded-2xl border border-line bg-surface shadow-sm">
      <div className="flex items-start justify-between gap-4 border-b border-line-soft px-5 py-4">
        <div className="flex min-w-0 items-start gap-3">
          <div className="rounded-lg bg-accent-soft p-2 text-accent"><TerminalSquare size={18} /></div>
          <div className="min-w-0">
            <div className="flex items-center gap-2">
              <h2 className="text-sm font-semibold text-fg">Claude Code 接入（订阅直通）</h2>
              <span className={`${state.tone} rounded-full border px-2 py-0.5 text-[11px] font-medium`}>{state.label}</span>
            </div>
            <p className="mt-1 text-xs text-fg-muted">
              Claude Code 用本机 claude.ai 登录时，把 ANTHROPIC_BASE_URL 指向这个入口；只有经此入口的请求能使用「订阅直通」端点，其余模型按模型路由走第三方 API。
            </p>
          </div>
        </div>
        <Button variant="secondary" size="sm" icon={RefreshCw} onClick={load} disabled={busy || !available} className="shrink-0">刷新</Button>
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
                disabled={busy || !info}
                onClick={() => save({ enabled: !info.enabled })}
                className={`relative inline-flex h-6 w-11 shrink-0 rounded-full transition-colors ${info?.enabled ? 'bg-indigo-600' : 'bg-surface-emph'} ${busy || !info ? 'cursor-not-allowed opacity-50' : ''}`}
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
                onChange={(event) => setPortInput(event.target.value)}
                disabled={busy || !info}
                inputMode="numeric"
                className="w-24 rounded-lg border border-line bg-surface px-2 py-1.5 text-sm text-fg outline-none focus:border-accent-ring disabled:bg-surface-mut"
              />
              <Button size="sm" variant="secondary" onClick={() => save({ listenPort: parsedPort })} disabled={busy || !portDirty || parsedPort === null}>
                保存端口
              </Button>
            </div>
          </div>
          {portDirty && parsedPort === null && <p className="text-xs text-danger">端口必须是 1-65535 的整数</p>}

          <div className="space-y-1.5 border-t border-line-soft pt-4">
            <div className="flex flex-wrap items-center justify-between gap-3">
              <span className="text-sm text-fg-body">接入模式</span>
              <ModeSegmented value={info?.mode || 'auto'} disabled={busy || !info} onChange={(mode) => save({ mode })} />
            </div>
            <p className="text-xs text-fg-subtle">{describeClaudeCodeEntryMode(info?.mode).hint} 切换立即对新请求生效，进行中的请求不受影响。</p>
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
    </div>
  );
};

export default ClaudeCodeEntryCard;
