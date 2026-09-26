// ============================================
// 请求追踪：Claude 接入与路由快捷切换
// 2026-09-24
// ============================================
// Claude Code 入口未启用时就是原来的端点策略切换器；启用后面板分「接入模式 / 端点策略」两页。
// 接入模式只影响经入口的 Claude Code 请求，主端口始终使用端点策略。

import { useId, useRef, useState } from 'react';
import { createPortal } from 'react-dom';
import { AlertCircle, Check, ChevronDown, Loader2, RefreshCw, Route } from 'lucide-react';
import useClaudeCodeEntry from '@components/claude-code-entry/useClaudeCodeEntry.js';
import {
  CLAUDE_CODE_ENTRY_MODE_OPTIONS,
  describeClaudeCodeEntryMode,
  describeClaudeCodeEntryState,
  resolveClaudeAccessLabel
} from '@components/claude-code-entry/claudeCodeEntryViewModel.js';
import { resolveClaudeRouteLabel } from '../utils/claudeRouteLabel.js';
import ClaudeEndpointSwitcher from './ClaudeEndpointSwitcher.jsx';
import useAnchoredPanel from './useAnchoredPanel.js';

const PANEL_TABS = [
  { id: 'access', label: '接入模式' },
  { id: 'endpoint', label: '端点策略' }
];

const ClaudeAccessSwitcher = (props) => {
  const { routingState, modelRoutingEnabled = false, modelRouteBindings = [] } = props;
  const { info, loading, busy, error, refresh, setMode } = useClaudeCodeEntry();
  const [open, setOpen] = useState(false);
  const [tab, setTab] = useState('access');
  const triggerRef = useRef(null);
  const panelRef = useRef(null);
  const menuId = useId();
  const position = useAnchoredPanel({ open, setOpen, triggerRef, panelRef, maxWidth: 560 });

  if (!info?.enabled) return <ClaudeEndpointSwitcher {...props} />;

  const routeLabel = resolveClaudeRouteLabel({ modelRoutingEnabled, bindings: modelRouteBindings, routingState });
  const { label, detail } = resolveClaudeAccessLabel({ info, loading, error, routeLabel });
  const entryState = describeClaudeCodeEntryState(info);
  const ready = info.running && !info.error;
  const triggerTitle = `Claude 接入：${label}${detail ? `（${detail}）` : ''}；点击切换接入模式或调整端点策略`;

  const chooseMode = (mode) => {
    if (busy || mode === info.mode) return;
    setMode(mode);
  };

  return (
    <>
      <button
        ref={triggerRef}
        type="button"
        aria-expanded={open}
        aria-controls={menuId}
        aria-label={`Claude 接入与路由：${label}`}
        onClick={() => { if (!open) refresh(); setOpen((value) => !value); }}
        title={triggerTitle}
        className="flex w-[170px] shrink-0 items-center gap-2 rounded-lg border border-line bg-surface px-3 py-1.5 text-sm font-medium text-fg-body shadow-sm hover:border-accent-line hover:text-accent lg:w-[190px] xl:w-[220px]"
      >
        {busy ? <Loader2 size={14} className="shrink-0 animate-spin text-accent" /> : <Route size={14} className={`shrink-0 ${ready ? 'text-accent' : 'text-warn'}`} />}
        <span className="min-w-0 flex-1 truncate text-left">Claude: {label}</span>
        <ChevronDown size={14} className={`shrink-0 transition-transform ${open ? 'rotate-180' : ''}`} />
      </button>

      {open && position && createPortal(
        <div ref={panelRef} id={menuId} style={position} className="fixed z-50 overflow-y-auto rounded-xl border border-line bg-surface shadow-xl ring-1 ring-hairline">
          <div className="flex items-center justify-between gap-3 border-b border-line-soft bg-surface-sub px-4 py-3">
            <div><h2 className="text-sm font-semibold text-fg">Claude 接入与路由</h2><p className="mt-0.5 text-[11px] text-fg-subtle">Claude Code 入口接入模式 · 端点调度策略</p></div>
            <button type="button" onClick={refresh} disabled={loading || busy} title="重新读取入口状态" aria-label="刷新 Claude Code 入口状态" className="rounded-lg border border-line p-1.5 text-fg-muted hover:bg-surface disabled:opacity-50"><RefreshCw size={14} className={loading ? 'animate-spin' : ''} /></button>
          </div>
          <div className="flex gap-1 border-b border-line-soft p-2">
            {PANEL_TABS.map((item) => <button key={item.id} type="button" aria-pressed={tab === item.id} onClick={() => setTab(item.id)} className={`flex-1 rounded-lg px-3 py-2 text-xs font-medium ${tab === item.id ? 'bg-accent-soft text-accent-fg' : 'text-fg-muted hover:bg-surface-sub'}`}>{item.label}</button>)}
          </div>

          {error && <p role="alert" className="mx-3 mt-3 flex items-start gap-2 rounded-lg border border-danger-line bg-danger-soft p-3 text-xs text-danger"><AlertCircle size={15} className="shrink-0" /><span>{error}</span></p>}

          {tab === 'access' ? (
            <div className="space-y-3 p-3">
              <div className="flex flex-wrap items-center justify-between gap-2 text-xs text-fg-muted">
                <span>Claude Code 入口：<span className={ready ? 'text-success' : 'text-warn'}>{entryState.label}</span></span>
                {info.baseUrl && <span className="font-mono text-fg-subtle">{info.baseUrl}</span>}
              </div>
              {!ready && (
                <p className="rounded-lg border border-warn-line bg-warn-soft p-3 text-xs leading-relaxed text-warn">
                  {info.error ? `入口未能启动：${info.error}` : '入口未在监听。'}请到「Claude 管理 → 客户端接入」检查；接入模式仍可先保存。
                </p>
              )}
              <div className="grid grid-cols-2 gap-2">
                {CLAUDE_CODE_ENTRY_MODE_OPTIONS.map((option) => {
                  const selected = info.mode === option.value;
                  return (
                    <button
                      key={option.value}
                      type="button"
                      disabled={busy || selected}
                      onClick={() => chooseMode(option.value)}
                      className={`flex items-center justify-center gap-2 rounded-xl border px-3 py-3 text-sm font-semibold disabled:cursor-default ${selected ? 'border-accent-line bg-accent-soft text-accent-fg' : 'border-line text-fg-muted hover:bg-surface-sub disabled:opacity-50'}`}
                    >
                      {selected && <Check size={15} />}{option.label}
                    </button>
                  );
                })}
              </div>
              <p className="text-xs leading-relaxed text-fg-muted">{describeClaudeCodeEntryMode(info.mode).hint}</p>
              <p className="text-[11px] leading-relaxed text-fg-subtle">切换立即对新请求生效，进行中的请求不受影响；只影响经入口的 Claude Code 请求，主端口始终使用端点策略。</p>
            </div>
          ) : (
            <div>
              <p className="border-b border-line-soft px-4 py-3 text-[11px] leading-relaxed text-fg-muted">端点策略作用于主端口请求，以及接入模式为「端点」时的 Claude Code 请求；订阅直通不经过这里。</p>
              <ClaudeEndpointSwitcher {...props} embedded />
            </div>
          )}
        </div>, document.body
      )}
    </>
  );
};

export default ClaudeAccessSwitcher;
