import { useId, useRef, useState } from 'react';
import { AlertCircle, Info, KeyRound, Route } from 'lucide-react';
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
import ModeCard from './route-switcher/ModeCard.jsx';
import useAnchoredPanel from '@hooks/useAnchoredPanel.js';

const PANEL_TABS = [
  { id: 'access', label: '接入模式' },
  { id: 'endpoint', label: '端点策略' }
];

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
  const [tab, setTab] = useState('access');
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
  const position = useAnchoredPanel({ open, setOpen, triggerRef, panelRef, maxWidth: !info?.enabled && !modelRoutingEnabled ? 400 : 560 });
  const handleOpenModelRouting = () => {
    setOpen(false);
    onOpenModelRouting?.();
  };

  const chooseMode = (mode) => {
    if (busy || mode === info?.mode) return;
    setMode(mode);
  };

  const accessTab = info?.enabled && (
    <div className="space-y-3 p-4">
      <div className="flex items-center gap-2 text-xs text-fg-muted">
        <span className={`h-1.5 w-1.5 rounded-full ${ready ? 'bg-success-solid' : info?.error ? 'bg-danger-solid' : 'bg-warn-solid'}`} />
        {entryState.label}
        {resolveHostPort(info?.baseUrl) && <><span className="text-fg-subtle">·</span><span className="font-mono text-fg-subtle">{resolveHostPort(info.baseUrl)}</span></>}
      </div>
      {error && <p role="alert" className="flex items-start gap-2 rounded-lg border border-danger-line bg-danger-soft p-3 text-xs text-danger"><AlertCircle size={15} className="shrink-0" /><span>{error}</span></p>}
      {!ready && <p className="rounded-lg border border-warn-line bg-warn-soft p-3 text-xs leading-relaxed text-warn">{info?.error ? `入口未能启动：${info.error}` : '入口未在监听。'}请到「Claude 管理 → 客户端接入」检查；接入模式仍可先保存。</p>}
      <div role="radiogroup" aria-label="接入模式" className="grid grid-cols-2 gap-2">
        {CLAUDE_CODE_ENTRY_MODE_OPTIONS.map((option) => (
          <ModeCard
            key={option.value}
            card={{ ...option, Icon: modeIcon(option.value) }}
            selected={info?.mode === option.value}
            disabled={busy}
            meta={option.value === 'endpoint' ? `当前策略：${resolveClaudeRouteLabel({ modelRoutingEnabled, bindings: modelRouteBindings, routingState, hasAvailableEndpoints })}` : '不经端点与模型路由'}
            onSelect={() => chooseMode(option.value)}
          />
        ))}
      </div>
      <p className="flex items-start gap-1.5 text-[11px] leading-relaxed text-fg-subtle" title="切换立即对新请求生效，进行中的请求不受影响"><Info size={12} className="mt-0.5 shrink-0" />只影响经入口的 Claude Code 请求，主端口始终走端点策略</p>
      {subscriptionMode && subscriptionQuota.available && <SubscriptionRateLimitPanel quota={subscriptionQuota.quota} error={subscriptionQuota.error} refreshing={subscriptionQuota.refreshing} nowMs={subscriptionQuota.nowMs} onRefresh={subscriptionQuota.refresh} />}
    </div>
  );

  const endpointTab = (
    <div>
      {subscriptionMode && <div className="flex items-center gap-2 border-b border-line-soft bg-surface-sub px-4 py-2 text-[11px] text-fg-muted"><Info size={12} className="shrink-0" />当前接入模式为订阅直通，这里的策略只影响主端口请求</div>}
      <ClaudeEndpointSwitcher {...props} embedded onOpenModelRouting={handleOpenModelRouting} />
    </div>
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
      <SwitcherPanel
        open={open}
        position={position}
        panelRef={panelRef}
        panelId={menuId}
        title={info?.enabled ? 'Claude 接入' : 'Claude 路由'}
        tabs={info?.enabled ? PANEL_TABS : null}
        activeTab={tab}
        onTabChange={setTab}
        refresh={info?.enabled ? refresh : null}
        refreshing={loading || busy}
      >
        {info?.enabled ? (tab === 'access' ? accessTab : endpointTab) : <ClaudeEndpointSwitcher {...props} embedded onOpenModelRouting={handleOpenModelRouting} />}
      </SwitcherPanel>
    </>
  );
};

export default ClaudeAccessSwitcher;
