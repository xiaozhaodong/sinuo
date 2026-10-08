// 请求页 Claude 按钮：接入模式 + 订阅额度 + 端点策略（或模型路由）
// 2026-10-07 桌面化：浮层改为控制中心模块式的一列，不再分「接入模式 / 端点策略」两个页签；
//            打开即刷新入口状态，底部「模型路由…」打开映射抽屉
import { useId, useRef, useState } from 'react';
import { KeyRound, Route } from 'lucide-react';
import { PopoverAlert, PopoverMenuItem, PopoverNote, PopoverSection, PopoverSeparator } from '@components/desktop/PopoverParts.jsx';
import useClaudeCodeEntry from '@components/claude-code-entry/useClaudeCodeEntry.js';
import useClaudeSubscriptionRateLimit from '@components/claude-code-entry/useClaudeSubscriptionRateLimit.js';
import SubscriptionRateLimitPanel from '@components/claude-code-entry/SubscriptionRateLimitPanel.jsx';
import {
  CLAUDE_CODE_ENTRY_MODE_OPTIONS,
  describeClaudeCodeEntryState,
  resolveClaudeAccessLabel
} from '@components/claude-code-entry/claudeCodeEntryViewModel.js';
import {
  describeSubscriptionQuotaBadge,
  describeSubscriptionQuotaSummary
} from '@components/claude-code-entry/subscriptionRateLimit.js';
import { resolveClaudeRouteLabel } from '../utils/claudeRouteLabel.js';
import ClaudeEndpointSwitcher from './ClaudeEndpointSwitcher.jsx';
import SwitcherPanel from './route-switcher/SwitcherPanel.jsx';
import SwitcherTrigger from './route-switcher/SwitcherTrigger.jsx';
import ModeRow from './route-switcher/ModeRow.jsx';
import useAnchoredPanel from '@hooks/useAnchoredPanel.js';

const modeIcon = (value) => (value === 'subscription' ? KeyRound : Route);

const resolveHostPort = (baseUrl) => {
  if (!baseUrl) return '';
  try {
    return new URL(baseUrl).host;
  } catch {
    return String(baseUrl).replace(/^https?:\/\//, '').replace(/\/.*$/, '');
  }
};

const ClaudeAccessSwitcher = (props) => {
  const { endpoints = [], routingState, modelRoutingEnabled = false, modelRouteBindings = [], onOpenModelRouting } = props;
  const { info, loading, busy, error, refresh, setMode } = useClaudeCodeEntry();
  const subscriptionQuota = useClaudeSubscriptionRateLimit();
  const [open, setOpen] = useState(false);
  const triggerRef = useRef(null);
  const panelRef = useRef(null);
  const menuId = useId();
  const hasAvailableEndpoints = Array.isArray(endpoints) && endpoints.some((endpoint) => endpoint.availabilityEnabled !== false);
  const endpointStrategyActive = !info || !info.enabled || info.mode === 'endpoint';
  const routeLabel = resolveClaudeRouteLabel({ modelRoutingEnabled, bindings: modelRouteBindings, routingState, compact: Boolean(info?.enabled), hasAvailableEndpoints });
  const { label, detail } = resolveClaudeAccessLabel({ info, loading, error, routeLabel });
  const entryState = describeClaudeCodeEntryState(info);
  const ready = Boolean(info?.running && !info?.error);
  const subscriptionMode = info?.mode === 'subscription';
  const quotaBadge = subscriptionMode ? describeSubscriptionQuotaBadge(subscriptionQuota.quota, subscriptionQuota.nowMs) : null;
  const quotaSummary = subscriptionMode ? describeSubscriptionQuotaSummary(subscriptionQuota.quota, subscriptionQuota.nowMs) : '';
  const triggerTitle = `Claude 接入：${label}${detail ? `（${detail}）` : ''}${quotaSummary ? `；订阅额度 ${quotaSummary}` : ''}；点击切换接入模式或调整端点策略`;
  const position = useAnchoredPanel({ open, setOpen, triggerRef, panelRef, maxWidth: modelRoutingEnabled ? 400 : 360 });
  const handleOpenModelRouting = () => {
    setOpen(false);
    onOpenModelRouting?.();
  };

  const chooseMode = (mode) => {
    if (busy || mode === info?.mode) return;
    setMode(mode);
  };

  const hostPort = resolveHostPort(info?.baseUrl);
  const entryStatus = (
    <>
      <span className={`h-1.5 w-1.5 shrink-0 rounded-full ${ready ? 'bg-success-solid' : info?.error ? 'bg-danger-solid' : 'bg-warn-solid'}`} />
      <span className="truncate">{entryState.label}{hostPort && <> · <span className="font-mono">{hostPort}</span></>}</span>
    </>
  );

  const accessSection = info?.enabled && (
    <>
      <PopoverSection title="接入模式" aside={entryStatus} />
      {error && <PopoverAlert tone="danger">{error}</PopoverAlert>}
      {!ready && <PopoverAlert>{info?.error ? `入口未能启动：${info.error}` : '入口未在监听。'}请到「Claude 管理 → 客户端接入」检查；接入模式仍可先保存。</PopoverAlert>}
      <div role="radiogroup" aria-label="接入模式">
        {CLAUDE_CODE_ENTRY_MODE_OPTIONS.map((option) => (
          <ModeRow
            key={option.value}
            icon={modeIcon(option.value)}
            label={option.label}
            meta={option.shortHint}
            checked={info?.mode === option.value}
            disabled={busy}
            onSelect={() => chooseMode(option.value)}
          />
        ))}
      </div>
      <div title="切换立即对新请求生效，进行中的请求不受影响">
        <PopoverNote>只影响经入口的 Claude Code 请求，主端口始终走端点策略</PopoverNote>
      </div>
      {subscriptionMode && subscriptionQuota.available && (
        <>
          <PopoverSeparator />
          <SubscriptionRateLimitPanel quota={subscriptionQuota.quota} error={subscriptionQuota.error} refreshing={subscriptionQuota.refreshing} nowMs={subscriptionQuota.nowMs} onRefresh={subscriptionQuota.refresh} />
        </>
      )}
      <PopoverSeparator />
    </>
  );

  return (
    <>
      <SwitcherTrigger
        triggerRef={triggerRef}
        menuId={menuId}
        open={open}
        brand="Claude"
        label={label}
        ariaLabel={`Claude 接入与路由：${label}${detail ? `（${detail}）` : ''}`}
        warn={Boolean(error) || (Boolean(info?.enabled) && !ready) || (!modelRoutingEnabled && endpointStrategyActive && !hasAvailableEndpoints)}
        busy={busy || (loading && !info)}
        badge={quotaBadge ? { ...quotaBadge, text: `${quotaBadge.label} ${quotaBadge.remainingPercent.toFixed(0)}%` } : null}
        title={triggerTitle}
        onClick={() => { if (!open) refresh(); setOpen((value) => !value); }}
      />
      <SwitcherPanel open={open} position={position} panelRef={panelRef} panelId={menuId} title={info?.enabled ? 'Claude 接入' : 'Claude 路由'}>
        {accessSection}
        <ClaudeEndpointSwitcher {...props} scopeLabel={info?.enabled && subscriptionMode ? '仅影响主端口' : ''} />
        <PopoverSeparator />
        <PopoverMenuItem onClick={handleOpenModelRouting}>模型路由…</PopoverMenuItem>
      </SwitcherPanel>
    </>
  );
};

export default ClaudeAccessSwitcher;
