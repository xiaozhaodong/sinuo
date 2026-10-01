import { useId, useRef, useState } from 'react';
import { AlertCircle, Info, KeyRound, UserRound } from 'lucide-react';
import useCodexSubscriptionProxy from '@components/codex-access/useCodexSubscriptionProxy.js';
import { modeSwitchBlockReason } from '@components/codex-access/subscriptionProxyOperations.js';
import {
  SUBSCRIPTION_PROXY_MODE_OPTIONS,
  SUBSCRIPTION_PROXY_MODE_SHORT_HINTS,
  isSubscriptionProxyStoppedByUser,
  resolveSubscriptionProxyDisplayState
} from '@components/codex-access/subscriptionProxyViewModel.js';
import { resolveCodexAccessLabel, resolveFallbackRouteLabel } from '../utils/accountSwitcherState.js';
import AccountPoolSwitcher from './AccountPoolSwitcher.jsx';
import CodexModelRoutingSwitcher from './CodexModelRoutingSwitcher.jsx';
import CodexModeConfirmDialog from './CodexModeConfirmDialog.jsx';
import SwitcherPanel from './route-switcher/SwitcherPanel.jsx';
import SwitcherTrigger from './route-switcher/SwitcherTrigger.jsx';
import ModeCard from './route-switcher/ModeCard.jsx';
import useAnchoredPanel from '@hooks/useAnchoredPanel.js';

const PANEL_TABS = [
  { id: 'access', label: '接入模式' },
  { id: 'pool', label: '账号池策略' }
];

const modeIcon = (value) => (value === 'subscription' ? KeyRound : UserRound);

const CodexAccessSwitcher = ({
  codexModelRoutingEnabled = false, bindings = [], accounts = [], onPatch,
  modelRoutePatching = false, activeAccount = null, recentSelectedAccountId = null,
  onSwitch, onSwitchAuto, accountSwitching = false, onOpenModelRouting, onNavigateToCodexAccess
}) => {
  const { config, status, loading, configError, statusError, busy, notice, refresh, switchMode, clearNotice } = useCodexSubscriptionProxy();
  const [open, setOpen] = useState(false);
  const [tab, setTab] = useState(null);
  const [pending, setPending] = useState(null);
  const [result, setResult] = useState(null);
  const triggerRef = useRef(null);
  const panelRef = useRef(null);
  const menuId = useId();
  const runningPool = Boolean(config?.initialized && config?.mode === 'pool' && status?.state === 'running');
  const fallbackRouteLabel = resolveFallbackRouteLabel({ modelRoutingEnabled: codexModelRoutingEnabled, activeAccount, modelRouteBindingCount: bindings.length });
  const triggerRouteLabel = resolveFallbackRouteLabel({ modelRoutingEnabled: codexModelRoutingEnabled, activeAccount, modelRouteBindingCount: bindings.length, compact: runningPool });
  const blockReason = modeSwitchBlockReason({ config, status, configError, statusError, fallbackRouteLabel });
  const ready = !blockReason;
  const modeLabel = SUBSCRIPTION_PROXY_MODE_OPTIONS.find((option) => option.value === config?.mode)?.label || '未知模式';
  const accessStateLabel = resolveSubscriptionProxyDisplayState({ config, status }).label;
  const { label: displayLabel, detail: displayDetail } = resolveCodexAccessLabel({ loading, configError, statusError, config, status, ready, routeLabel: runningPool ? triggerRouteLabel : fallbackRouteLabel });
  const triggerIconWarn = (!ready && !displayDetail) || Boolean(configError) || Boolean(statusError);
  const triggerTitle = displayDetail ? `原有入口策略：${displayLabel} · ${displayDetail}；点击调整账号池策略` : `Codex 接入状态：${displayLabel}；点击切换接入模式或调整账号池策略`;
  const fallbackPanel = Boolean(config?.initialized) && !configError && !statusError && isSubscriptionProxyStoppedByUser({ config, status });
  const activeTab = tab || (config && !config.initialized ? 'pool' : 'access');
  const stale = Boolean(pending && (!ready || loading || config?.revision !== pending.config.revision || config?.mode !== pending.config.mode));
  const position = useAnchoredPanel({ open, setOpen, triggerRef, panelRef, maxWidth: 560, dismissible: !pending });
  const handleOpenModelRouting = () => {
    setOpen(false);
    onOpenModelRouting?.();
  };
  const handleNavigateToCodexAccess = () => {
    setOpen(false);
    onNavigateToCodexAccess?.();
  };

  const poolPanel = codexModelRoutingEnabled
    ? <CodexModelRoutingSwitcher bindings={bindings} accounts={accounts} onPatch={onPatch} loading={modelRoutePatching} onOpenModelRouting={handleOpenModelRouting} />
    : <AccountPoolSwitcher accounts={accounts} activeAccount={activeAccount} recentSelectedAccountId={recentSelectedAccountId} onSwitch={onSwitch} onSwitchAuto={onSwitchAuto} loading={accountSwitching} />;

  const chooseMode = (mode) => {
    if (!ready || busy || loading || mode === config?.mode) return;
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

  const accessTab = (
    <div className="space-y-3 p-4">
      <div className="flex items-center gap-2 text-xs text-fg-muted"><span className={`h-1.5 w-1.5 rounded-full ${ready ? 'bg-success-solid' : configError || statusError ? 'bg-danger-solid' : 'bg-warn-solid'}`} />独立入口：{accessStateLabel}{config?.initialized && <><span className="text-fg-subtle">·</span><span>已保存：{modeLabel}</span></>}</div>
      {(configError || statusError) && <p role="alert" className="flex items-start gap-2 rounded-lg border border-danger-line bg-danger-soft p-3 text-xs text-danger"><AlertCircle size={15} className="shrink-0" /><span>{configError ? `配置读取失败：${configError}` : `运行态读取失败：${statusError}`}。请刷新重试。</span></p>}
      {!ready && !loading && !configError && !statusError && <p className="rounded-lg border border-warn-line bg-warn-soft p-3 text-xs leading-relaxed text-warn">{blockReason}{status?.lastError && ` 最近一次运行错误：${status.lastError}`}</p>}
      <div role="radiogroup" aria-label="接入模式" className="grid grid-cols-2 gap-2">
        {SUBSCRIPTION_PROXY_MODE_OPTIONS.map((option) => (
          <ModeCard
            key={option.value}
            card={{ ...option, shortHint: SUBSCRIPTION_PROXY_MODE_SHORT_HINTS[option.value], Icon: modeIcon(option.value) }}
            selected={ready && config?.mode === option.value}
            disabled={!ready || loading || busy || Boolean(configError) || Boolean(statusError)}
            meta={option.value === 'pool' ? `当前策略：${fallbackRouteLabel}` : '不经账号池'}
            onSelect={() => chooseMode(option.value)}
          />
        ))}
      </div>
      <p className="flex items-start gap-1.5 text-[11px] leading-relaxed text-fg-subtle"><Info size={12} className="mt-0.5 shrink-0" />切换前会确认影响范围；HTTP 下一请求、WS 重连后生效</p>
    </div>
  );

  const poolTab = (
    <div>
      {displayDetail && !fallbackPanel && <div className="border-b border-line-soft px-4 py-2 text-[11px] text-fg-muted">{displayDetail} · 原有入口策略：{fallbackRouteLabel}</div>}
      {config?.enabled && status?.state === 'running' && config.mode === 'subscription' && <div className="flex items-center gap-2 border-b border-line-soft bg-surface-sub px-4 py-2 text-[11px] text-fg-muted"><Info size={12} className="shrink-0" />独立入口为订阅直通，这里的策略只影响原有入口</div>}
      {poolPanel}
    </div>
  );

  const noticePanel = notice && !pending && <div role="status" className={`mx-3 mt-3 rounded-lg border px-3 py-2 text-xs ${notice.type === 'success' ? 'border-success-line bg-success-soft text-success' : notice.type === 'error' ? 'border-danger-line bg-danger-soft text-danger' : 'border-warn-line bg-warn-soft text-warn'}`}>{notice.text}</div>;

  const stoppedStatus = <span className="tone-slate rounded border px-1.5 py-px text-[10px] font-medium">独立入口已停用</span>;
  const stoppedWarning = <div className="flex items-center gap-2 border-b border-warn-line bg-warn-soft px-4 py-2 text-[11px] text-warn" title="停用不会切换客户端接入地址；仍指向独立入口的客户端将无法连接。如需使用原有入口，请先修改客户端接入配置；重新启用独立入口请到 Codex 管理 → 客户端接入。"><AlertCircle size={13} className="shrink-0" /><span className="min-w-0 flex-1">独立入口已停用：下面的策略只作用于原有入口，仍指向独立入口的客户端会连不上</span><button type="button" onClick={handleNavigateToCodexAccess} className="shrink-0 font-medium text-warn hover:text-fg">去启用 →</button></div>;

  return (
    <>
      <SwitcherTrigger triggerRef={triggerRef} menuId={menuId} open={open} brand="Codex" label={displayLabel} ariaLabel={`Codex 接入与路由：${displayLabel}${displayDetail ? `（${displayDetail}）` : ''}`} warn={triggerIconWarn} busy={loading || busy} title={triggerTitle} onClick={() => { if (!open) refresh(); setOpen((value) => !value); }} />
      <SwitcherPanel open={open} position={position} panelRef={panelRef} panelId={menuId} title="Codex 接入" headerStatus={fallbackPanel ? stoppedStatus : null} tabs={fallbackPanel ? null : PANEL_TABS} activeTab={activeTab} onTabChange={setTab} refresh={refresh} refreshing={loading || busy}>
        {noticePanel}
        {fallbackPanel ? <>{stoppedWarning}{poolTab}</> : (activeTab === 'access' ? accessTab : poolTab)}
      </SwitcherPanel>
      <CodexModeConfirmDialog pending={pending} status={status} busy={busy} stale={stale} result={result} notice={pending ? notice : null} onClose={() => setPending(null)} onConfirm={confirmMode} onRefresh={refreshAfterAttempt} returnFocusRef={triggerRef} />
    </>
  );
};

export default CodexAccessSwitcher;
