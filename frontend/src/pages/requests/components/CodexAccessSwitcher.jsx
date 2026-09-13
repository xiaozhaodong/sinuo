import { useEffect, useId, useRef, useState } from 'react';
import { createPortal } from 'react-dom';
import { AlertCircle, Check, ChevronDown, Loader2, RefreshCw, Route } from 'lucide-react';
import useCodexSubscriptionProxy from '@components/codex-access/useCodexSubscriptionProxy.js';
import { modeSwitchBlockReason } from '@components/codex-access/subscriptionProxyOperations.js';
import {
  SUBSCRIPTION_PROXY_MODE_HINTS,
  SUBSCRIPTION_PROXY_MODE_OPTIONS,
  subscriptionProxyStateMeta
} from '@components/codex-access/subscriptionProxyViewModel.js';
import AccountPoolSwitcher from './AccountPoolSwitcher.jsx';
import CodexModelRoutingSwitcher from './CodexModelRoutingSwitcher.jsx';
import CodexModeConfirmDialog from './CodexModeConfirmDialog.jsx';

const CodexAccessSwitcher = ({
  codexModelRoutingEnabled = false, bindings = [], accounts = [], onPatch,
  modelRoutePatching = false, activeAccount = null, recentSelectedAccountId = null,
  onSwitch, onSwitchAuto, accountSwitching = false
}) => {
  const { config, status, loading, configError, statusError, busy, notice, refresh, switchMode, clearNotice } = useCodexSubscriptionProxy();
  const [open, setOpen] = useState(false);
  const [tab, setTab] = useState('access');
  const [position, setPosition] = useState(null);
  const [pending, setPending] = useState(null);
  const [result, setResult] = useState(null);
  const triggerRef = useRef(null);
  const panelRef = useRef(null);
  const menuId = useId();

  const blockReason = modeSwitchBlockReason({ config, status, configError, statusError });
  const ready = !blockReason;
  const modeLabel = SUBSCRIPTION_PROXY_MODE_OPTIONS.find((option) => option.value === config?.mode)?.label || '未知模式';
  const stateLabel = subscriptionProxyStateMeta(status?.state).label;
  const displayLabel = loading ? '读取中' : configError ? '配置读取失败' : !config?.initialized ? '未初始化' : statusError ? '状态未知' : status?.state !== 'running' ? stateLabel : !ready ? '状态待确认' : modeLabel;
  const stale = Boolean(pending && (!ready || loading || config?.revision !== pending.config.revision || config?.mode !== pending.config.mode));

  useEffect(() => {
    if (!open) return undefined;
    const positionPanel = () => {
      const rect = triggerRef.current?.getBoundingClientRect();
      if (!rect) return;
      const width = Math.min(560, window.innerWidth - 24);
      const top = Math.min(rect.bottom + 8, Math.max(12, window.innerHeight - 180));
      setPosition({ width, top, left: Math.max(12, Math.min(rect.left, window.innerWidth - width - 12)), maxHeight: Math.max(120, window.innerHeight - top - 12) });
    };
    positionPanel();
    window.addEventListener('resize', positionPanel);
    window.addEventListener('scroll', positionPanel, true);
    return () => {
      window.removeEventListener('resize', positionPanel);
      window.removeEventListener('scroll', positionPanel, true);
    };
  }, [open]);

  useEffect(() => {
    if (!open || pending) return undefined;
    const outside = (event) => {
      if (!triggerRef.current?.contains(event.target) && !panelRef.current?.contains(event.target)) setOpen(false);
    };
    const escape = (event) => {
      if (event.key === 'Escape') {
        setOpen(false);
        triggerRef.current?.focus();
      }
    };
    document.addEventListener('mousedown', outside);
    document.addEventListener('keydown', escape);
    return () => {
      document.removeEventListener('mousedown', outside);
      document.removeEventListener('keydown', escape);
    };
  }, [open, pending]);

  const chooseMode = (mode) => {
    if (!ready || busy || loading || mode === config.mode) return;
    clearNotice();
    setResult(null);
    setPending({ mode, config: { ...config } });
  };

  const confirmMode = async () => {
    if (!pending || stale || busy || result?.saved) return;
    const nextResult = await switchMode(pending.mode, pending.config);
    setResult(nextResult);
    if (nextResult.confirmed) setPending(null);
  };

  const refreshAfterAttempt = async () => {
    await refresh();
    setPending(null);
    setResult(null);
  };

  return (
    <>
      <button ref={triggerRef} type="button" aria-expanded={open} aria-controls={menuId} aria-label={`Codex 接入与账号池策略：${displayLabel}`} onClick={() => { if (!open) refresh(); setOpen((value) => !value); }} title={`独立入口：${displayLabel}；点击切换接入模式或调整账号池策略`} className="flex w-[170px] shrink-0 items-center gap-2 rounded-lg border border-line bg-surface px-3 py-1.5 text-sm font-medium text-fg-body shadow-sm hover:border-accent-line hover:text-accent lg:w-[190px] xl:w-[220px]">
        {loading || busy ? <Loader2 size={14} className="shrink-0 animate-spin text-accent" /> : <Route size={14} className={`shrink-0 ${ready ? 'text-accent' : 'text-fg-subtle'}`} />}
        <span className="min-w-0 flex-1 truncate text-left">Codex: {displayLabel}</span>
        <ChevronDown size={14} className={`shrink-0 transition-transform ${open ? 'rotate-180' : ''}`} />
      </button>

      {open && position && createPortal(
        <div ref={panelRef} id={menuId} style={position} className="fixed z-50 overflow-y-auto rounded-xl border border-line bg-surface shadow-xl ring-1 ring-hairline">
          <div className="flex items-center justify-between gap-3 border-b border-line-soft bg-surface-sub px-4 py-3">
            <div><h2 className="text-sm font-semibold text-fg">Codex 接入与路由</h2><p className="mt-0.5 text-[11px] text-fg-subtle">独立入口全局模式 · 账号池调度策略</p></div>
            <button type="button" onClick={refresh} disabled={loading || busy} title="重新读取配置与运行态" aria-label="刷新 Codex 接入状态" className="rounded-lg border border-line p-1.5 text-fg-muted hover:bg-surface disabled:opacity-50"><RefreshCw size={14} className={loading ? 'animate-spin' : ''} /></button>
          </div>
          <div className="flex gap-1 border-b border-line-soft p-2">
            {[{ id: 'access', label: '接入模式' }, { id: 'pool', label: '账号池策略' }].map((item) => <button key={item.id} type="button" aria-pressed={tab === item.id} onClick={() => setTab(item.id)} className={`flex-1 rounded-lg px-3 py-2 text-xs font-medium ${tab === item.id ? 'bg-accent-soft text-accent-fg' : 'text-fg-muted hover:bg-surface-sub'}`}>{item.label}</button>)}
          </div>

          {notice && !pending && <div role="status" className={`mx-3 mt-3 rounded-lg border px-3 py-2 text-xs ${notice.type === 'success' ? 'border-success-line bg-success-soft text-success' : notice.type === 'error' ? 'border-danger-line bg-danger-soft text-danger' : 'border-warn-line bg-warn-soft text-warn'}`}>{notice.text}</div>}

          {tab === 'access' ? (
            <div className="space-y-3 p-3">
              <div className="flex flex-wrap items-center justify-between gap-2 text-xs text-fg-muted"><span>独立入口：<span className={ready ? 'text-success' : 'text-warn'}>{displayLabel}</span></span>{config?.initialized && <span>已保存：{modeLabel}</span>}</div>
              {(configError || statusError) && <p role="alert" className="flex items-start gap-2 rounded-lg border border-danger-line bg-danger-soft p-3 text-xs text-danger"><AlertCircle size={15} className="shrink-0" /><span>{configError ? `配置读取失败：${configError}` : `运行态读取失败：${statusError}`}。请刷新重试。</span></p>}
              {!ready && !loading && !configError && !statusError && <p className="rounded-lg border border-warn-line bg-warn-soft p-3 text-xs leading-relaxed text-warn">{blockReason}{status?.lastError && ` 最近一次运行错误：${status.lastError}`}</p>}
              <div className="grid grid-cols-2 gap-2">
                {SUBSCRIPTION_PROXY_MODE_OPTIONS.map((option) => <button key={option.value} type="button" disabled={!ready || loading || busy || option.value === config?.mode} onClick={() => chooseMode(option.value)} className={`flex items-center justify-center gap-2 rounded-xl border px-3 py-3 text-sm font-semibold disabled:cursor-default ${ready && config.mode === option.value ? 'border-accent-line bg-accent-soft text-accent-fg' : 'border-line text-fg-muted hover:bg-surface-sub disabled:opacity-50'}`}>{ready && config.mode === option.value && <Check size={15} />}{option.label}</button>)}
              </div>
              {ready && <p className="text-xs leading-relaxed text-fg-muted">{SUBSCRIPTION_PROXY_MODE_HINTS[config.mode]}</p>}
              <p className="text-[11px] leading-relaxed text-fg-subtle">切换前会确认影响范围。HTTP 下一请求、已建立 WS 重连后生效，在途请求保持原行为。</p>
            </div>
          ) : (
            <div>
              <p className="border-b border-line-soft px-4 py-3 text-[11px] leading-relaxed text-fg-muted">池策略供原有入口和独立入口的账号池模式使用；独立入口订阅直通时，原有入口仍可使用账号池。</p>
              {codexModelRoutingEnabled ? <CodexModelRoutingSwitcher embedded bindings={bindings} accounts={accounts} onPatch={onPatch} loading={modelRoutePatching} /> : <AccountPoolSwitcher embedded accounts={accounts} activeAccount={activeAccount} recentSelectedAccountId={recentSelectedAccountId} onSwitch={onSwitch} onSwitchAuto={onSwitchAuto} loading={accountSwitching} modelRoutingEnabled={false} />}
            </div>
          )}
        </div>, document.body
      )}

      <CodexModeConfirmDialog pending={pending} status={status} busy={busy} stale={stale} result={result} notice={pending ? notice : null} onClose={() => setPending(null)} onConfirm={confirmMode} onRefresh={refreshAfterAttempt} returnFocusRef={triggerRef} />
    </>
  );
};

export default CodexAccessSwitcher;
