// 请求页 Codex 按钮：独立入口接入模式 + 账号池策略（或模型路由）
// 2026-10-07 桌面化：浮层改为控制中心模块式的一列，不再分「接入模式 / 账号池策略」两个页签；
//            切换接入模式仍先弹确认框；底部「模型路由…」打开绑定抽屉
import { useId, useRef, useState } from 'react';
import { KeyRound, UserRound } from 'lucide-react';
import { PopoverAlert, PopoverMenuItem, PopoverNote, PopoverSection, PopoverSeparator } from '@components/desktop/PopoverParts.jsx';
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
import ModeRow from './route-switcher/ModeRow.jsx';
import useAnchoredPanel from '@hooks/useAnchoredPanel.js';

const NOTICE_TONES = { success: 'success', error: 'danger' };

const modeIcon = (value) => (value === 'subscription' ? KeyRound : UserRound);

const CodexAccessSwitcher = ({
  codexModelRoutingEnabled = false, bindings = [], accounts = [], onPatch,
  modelRoutePatching = false, activeAccount = null, recentSelectedAccountId = null,
  onSwitch, onSwitchAuto, accountSwitching = false, onOpenModelRouting, onNavigateToCodexAccess
}) => {
  const { config, status, loading, configError, statusError, busy, notice, refresh, switchMode, clearNotice } = useCodexSubscriptionProxy();
  const [open, setOpen] = useState(false);
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
  const stale = Boolean(pending && (!ready || loading || config?.revision !== pending.config.revision || config?.mode !== pending.config.mode));
  const position = useAnchoredPanel({ open, setOpen, triggerRef, panelRef, maxWidth: codexModelRoutingEnabled ? 400 : 360, dismissible: !pending });
  const handleOpenModelRouting = () => {
    setOpen(false);
    onOpenModelRouting?.();
  };
  const handleNavigateToCodexAccess = () => {
    setOpen(false);
    onNavigateToCodexAccess?.();
  };

  // 独立入口为订阅直通时，账号池 / 模型路由策略只作用于原有入口
  const poolScopeLabel = config?.enabled && status?.state === 'running' && config.mode === 'subscription' ? '仅影响原有入口' : '';
  const poolPanel = codexModelRoutingEnabled
    ? <CodexModelRoutingSwitcher bindings={bindings} accounts={accounts} onPatch={onPatch} loading={modelRoutePatching} scopeLabel={poolScopeLabel} />
    : <AccountPoolSwitcher accounts={accounts} activeAccount={activeAccount} recentSelectedAccountId={recentSelectedAccountId} onSwitch={onSwitch} onSwitchAuto={onSwitchAuto} loading={accountSwitching} scopeLabel={poolScopeLabel} />;

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

  const accessStatus = (
    <>
      <span className={`h-1.5 w-1.5 shrink-0 rounded-full ${fallbackPanel ? 'bg-fg-subtle' : ready ? 'bg-success-solid' : configError || statusError ? 'bg-danger-solid' : 'bg-warn-solid'}`} />
      <span className="truncate">{fallbackPanel ? '独立入口已停用' : <>独立入口{accessStateLabel}{config?.initialized && !ready ? ` · 已保存：${modeLabel}` : ''}</>}</span>
    </>
  );

  // 独立入口未初始化时没有可切换的接入模式，只显示账号池策略
  const accessSection = config?.initialized && !fallbackPanel && (
    <>
      <PopoverSection title="接入模式" aside={accessStatus} />
      {(configError || statusError) && <PopoverAlert tone="danger">{configError ? `配置读取失败：${configError}` : `运行态读取失败：${statusError}`}。请重新打开重试。</PopoverAlert>}
      {!ready && !loading && !configError && !statusError && <PopoverAlert>{blockReason}{status?.lastError && ` 最近一次运行错误：${status.lastError}`}</PopoverAlert>}
      <div role="radiogroup" aria-label="接入模式">
        {SUBSCRIPTION_PROXY_MODE_OPTIONS.map((option) => (
          <ModeRow
            key={option.value}
            icon={modeIcon(option.value)}
            label={option.label}
            meta={SUBSCRIPTION_PROXY_MODE_SHORT_HINTS[option.value]}
            checked={ready && config?.mode === option.value}
            disabled={!ready || loading || busy || Boolean(configError) || Boolean(statusError)}
            onSelect={() => chooseMode(option.value)}
          />
        ))}
      </div>
      <PopoverNote>切换前会确认影响范围；HTTP 下一请求、WS 重连后生效</PopoverNote>
      <PopoverSeparator />
    </>
  );

  const noticeAlert = notice && !pending && <PopoverAlert tone={NOTICE_TONES[notice.type] || 'warn'} role="status">{notice.text}</PopoverAlert>;

  const stoppedSection = fallbackPanel && (
    <>
      <PopoverSection title="接入模式" aside={accessStatus} />
      <div title="停用不会切换客户端接入地址；仍指向独立入口的客户端将无法连接。如需使用原有入口，请先修改客户端接入配置；重新启用独立入口请到 Codex 管理 → 客户端接入。">
        <PopoverNote>下面的策略只作用于原有入口；仍指向独立入口的客户端会连不上</PopoverNote>
      </div>
      <PopoverMenuItem onClick={handleNavigateToCodexAccess}>启用独立入口…</PopoverMenuItem>
      <PopoverSeparator />
    </>
  );

  return (
    <>
      <SwitcherTrigger triggerRef={triggerRef} menuId={menuId} open={open} brand="Codex" label={displayLabel} ariaLabel={`Codex 接入与路由：${displayLabel}${displayDetail ? `（${displayDetail}）` : ''}`} warn={triggerIconWarn} busy={loading || busy} title={triggerTitle} onClick={() => { if (!open) refresh(); setOpen((value) => !value); }} />
      <SwitcherPanel open={open} position={position} panelRef={panelRef} panelId={menuId} title="Codex 接入">
        {noticeAlert}
        {stoppedSection}
        {loading && !config && <PopoverNote>读取中…</PopoverNote>}
        {accessSection}
        {displayDetail && !fallbackPanel && <PopoverNote>{displayDetail} · 原有入口策略：{fallbackRouteLabel}</PopoverNote>}
        {poolPanel}
        <PopoverSeparator />
        <PopoverMenuItem onClick={handleOpenModelRouting}>模型路由…</PopoverMenuItem>
      </SwitcherPanel>
      <CodexModeConfirmDialog pending={pending} status={status} busy={busy} stale={stale} result={result} notice={pending ? notice : null} onClose={() => setPending(null)} onConfirm={confirmMode} onRefresh={refreshAfterAttempt} returnFocusRef={triggerRef} />
    </>
  );
};

export default CodexAccessSwitcher;
