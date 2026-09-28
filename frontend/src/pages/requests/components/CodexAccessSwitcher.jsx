import { useId, useRef, useState } from 'react';
import { createPortal } from 'react-dom';
import { AlertCircle, Check, ChevronDown, Loader2, RefreshCw, Route } from 'lucide-react';
import useCodexSubscriptionProxy from '@components/codex-access/useCodexSubscriptionProxy.js';
import { modeSwitchBlockReason } from '@components/codex-access/subscriptionProxyOperations.js';
import {
  SUBSCRIPTION_PROXY_MODE_HINTS,
  SUBSCRIPTION_PROXY_MODE_OPTIONS,
  isSubscriptionProxyStoppedByUser,
  resolveSubscriptionProxyDisplayState
} from '@components/codex-access/subscriptionProxyViewModel.js';
import { resolveCodexAccessLabel, resolveFallbackRouteLabel } from '../utils/accountSwitcherState.js';
import AccountPoolSwitcher from './AccountPoolSwitcher.jsx';
import CodexModelRoutingSwitcher from './CodexModelRoutingSwitcher.jsx';
import CodexModeConfirmDialog from './CodexModeConfirmDialog.jsx';
import useAnchoredPanel from './useAnchoredPanel.js';

const CodexAccessSwitcher = ({
  codexModelRoutingEnabled = false, bindings = [], accounts = [], onPatch,
  modelRoutePatching = false, activeAccount = null, recentSelectedAccountId = null,
  onSwitch, onSwitchAuto, accountSwitching = false
}) => {
  const { config, status, loading, configError, statusError, busy, notice, refresh, switchMode, clearNotice } = useCodexSubscriptionProxy();
  const [open, setOpen] = useState(false);
  const [tab, setTab] = useState(null);
  const [pending, setPending] = useState(null);
  const [result, setResult] = useState(null);
  const triggerRef = useRef(null);
  const panelRef = useRef(null);
  const menuId = useId();

  const fallbackRouteLabel = resolveFallbackRouteLabel({
    modelRoutingEnabled: codexModelRoutingEnabled, activeAccount, modelRouteBindingCount: bindings.length
  });
  const blockReason = modeSwitchBlockReason({ config, status, configError, statusError, fallbackRouteLabel });
  const ready = !blockReason;
  const modeLabel = SUBSCRIPTION_PROXY_MODE_OPTIONS.find((option) => option.value === config?.mode)?.label || '未知模式';
  const accessStateLabel = resolveSubscriptionProxyDisplayState({ config, status }).label;
  // 按钮主标签始终回答「Codex 请求现在怎么路由」；独立入口未初始化或已停用时不占主标签，
  // 入口状态只进 tooltip 与面板副标题。停用不会自动改写客户端配置，面板内仍明确提示。
  const { label: displayLabel, detail: displayDetail } = resolveCodexAccessLabel({
    loading, configError, statusError, config, status, ready, routeLabel: fallbackRouteLabel
  });
  // 图标与主标签同口径：入口未初始化/已停用时主标签展示的是原有入口策略，同样是正常路由；只有真实异常才提示。
  const triggerIconTone = ready || displayDetail ? 'text-accent' : 'text-warn';
  const triggerTitle = displayDetail
    ? `原有入口策略：${displayLabel} · ${displayDetail}；点击调整账号池策略`
    : `Codex 接入状态：${displayLabel}；点击切换接入模式或调整账号池策略`;
  const panelSubtitle = displayDetail ? `${displayDetail} · 原有入口策略：${fallbackRouteLabel}` : '独立入口全局模式 · 账号池调度策略';
  const stale = Boolean(pending && (!ready || loading || config?.revision !== pending.config.revision || config?.mode !== pending.config.mode));

  // 主动停用后不会自动切换客户端配置：面板展示原有入口策略，但明确要求用户自行切回。
  // 未初始化的默认配置同样是 enabled=false + stopped，但那不是「停用」，要保留接入模式页供发现。
  // 刻意不看 loading：打开面板会触发一次刷新，跟着 loading 切换会让视图闪回再闪走。
  const fallbackPanel = Boolean(config?.initialized) && !configError && !statusError && isSubscriptionProxyStoppedByUser({ config, status });
  // 未初始化的用户还没用上独立入口：默认落到账号池策略页，而不是一页全灰的接入模式按钮。
  const activeTab = tab || (config && !config.initialized ? 'pool' : 'access');
  // 原有入口策略面板供两个视图共用：停用时的默认视图、接入模式视图里的“账号池策略”页。
  const poolPanel = codexModelRoutingEnabled
    ? <CodexModelRoutingSwitcher embedded bindings={bindings} accounts={accounts} onPatch={onPatch} loading={modelRoutePatching} />
    : <AccountPoolSwitcher embedded accounts={accounts} activeAccount={activeAccount} recentSelectedAccountId={recentSelectedAccountId} onSwitch={onSwitch} onSwitchAuto={onSwitchAuto} loading={accountSwitching} modelRoutingEnabled={false} />;

  const position = useAnchoredPanel({ open, setOpen, triggerRef, panelRef, maxWidth: 560, dismissible: !pending });

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
      <button ref={triggerRef} type="button" aria-expanded={open} aria-controls={menuId} aria-label={`Codex 接入与路由：${displayLabel}${displayDetail ? `（${displayDetail}）` : ''}`} onClick={() => { if (!open) refresh(); setOpen((value) => !value); }} title={triggerTitle} className="flex w-[170px] shrink-0 items-center gap-2 rounded-lg border border-line bg-surface px-3 py-1.5 text-sm font-medium text-fg-body shadow-sm hover:border-accent-line hover:text-accent lg:w-[190px] xl:w-[220px]">
        {loading || busy ? <Loader2 size={14} className="shrink-0 animate-spin text-accent" /> : <Route size={14} className={`shrink-0 ${triggerIconTone}`} />}
        <span className="min-w-0 flex-1 truncate text-left">Codex: {displayLabel}</span>
        <ChevronDown size={14} className={`shrink-0 transition-transform ${open ? 'rotate-180' : ''}`} />
      </button>

      {open && position && createPortal(
        <div ref={panelRef} id={menuId} style={position} className="fixed z-50 overflow-y-auto rounded-xl border border-line bg-surface shadow-xl ring-1 ring-hairline motion-popover origin-top-left">
          <div className="flex items-center justify-between gap-3 border-b border-line-soft bg-surface-sub px-4 py-3">
            <div><h2 className="text-sm font-semibold text-fg">Codex 接入与路由</h2><p className="mt-0.5 text-[11px] text-fg-subtle">{panelSubtitle}</p></div>
            <button type="button" onClick={refresh} disabled={loading || busy} title="重新读取配置与运行态" aria-label="刷新 Codex 接入状态" className="rounded-lg border border-line p-1.5 text-fg-muted hover:bg-surface disabled:opacity-50"><RefreshCw size={14} className={loading ? 'animate-spin' : ''} /></button>
          </div>
          {!fallbackPanel && (
            <div className="flex gap-1 border-b border-line-soft p-2">
              {[{ id: 'access', label: '接入模式' }, { id: 'pool', label: '账号池策略' }].map((item) => <button key={item.id} type="button" aria-pressed={activeTab === item.id} onClick={() => setTab(item.id)} className={`flex-1 rounded-lg px-3 py-2 text-xs font-medium ${activeTab === item.id ? 'bg-accent-soft text-accent-fg' : 'text-fg-muted hover:bg-surface-sub'}`}>{item.label}</button>)}
            </div>
          )}

          {notice && !pending && <div role="status" className={`mx-3 mt-3 rounded-lg border px-3 py-2 text-xs ${notice.type === 'success' ? 'border-success-line bg-success-soft text-success' : notice.type === 'error' ? 'border-danger-line bg-danger-soft text-danger' : 'border-warn-line bg-warn-soft text-warn'}`}>{notice.text}</div>}

          {fallbackPanel ? (
            <div>
              <p className="border-b border-line-soft px-4 py-3 text-[11px] leading-relaxed text-fg-muted">下面的策略仅对原有入口的请求生效。停用不会切换客户端接入地址；仍指向独立入口的客户端将无法连接。如需使用原有入口，请先修改客户端接入配置；重新启用独立入口请到 Codex 管理 → 客户端接入。</p>
              {poolPanel}
            </div>
          ) : activeTab === 'access' ? (
            <div className="space-y-3 p-3">
              <div className="flex flex-wrap items-center justify-between gap-2 text-xs text-fg-muted"><span>独立入口：<span className={ready ? 'text-success' : 'text-warn'}>{accessStateLabel}</span></span>{config?.initialized && <span>已保存：{modeLabel}</span>}</div>
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
              {poolPanel}
            </div>
          )}
        </div>, document.body
      )}

      <CodexModeConfirmDialog pending={pending} status={status} busy={busy} stale={stale} result={result} notice={pending ? notice : null} onClose={() => setPending(null)} onConfirm={confirmMode} onRefresh={refreshAfterAttempt} returnFocusRef={triggerRef} />
    </>
  );
};

export default CodexAccessSwitcher;
