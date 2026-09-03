import { ShieldCheck } from 'lucide-react';
import { getEndpointLastCheckDisplayValue } from '../utils/lastCheckDisplay.js';
import { summarizeEndpointModelRewriteRules } from '../utils/modelRewrite.js';
import { summarizeEndpointAuthentication } from '../utils/endpointFormState.js';
import ActionButtons from './ActionButtons.jsx';
import EndpointCooldownInfo from './EndpointCooldownInfo.jsx';
import EndpointHealthBadge from './EndpointHealthBadge.jsx';
import EndpointQuickActions from './EndpointQuickActions.jsx';
import ToggleSwitch from './ToggleSwitch.jsx';
import { useTimezone } from '@contexts/TimezoneContext.jsx';

// 头部名称前的状态点:冷却 > 不可达 > 可达 > 未检测
const statusDotClass = (endpoint) => {
  if (endpoint.inCooldown || endpoint.cooldownPersistPending) return 'bg-warn-solid';
  if (endpoint.neverChecked || !endpoint.lastCheck) return 'bg-fg-subtle/60';
  return endpoint.healthy ? 'bg-success-solid' : 'bg-danger-solid';
};

const EndpointCard = ({ endpoint, routingState, busy, onEdit, onDelete, onTest, onAvailabilityChange, onAutoScheduleChange, onSetRouting, onClearCooldown, modelRoutingEnabled = false }) => {
  const { formatTimestamp } = useTimezone();
  const rewrite = summarizeEndpointModelRewriteRules(endpoint.modelRewriteRules || '');
  const lastCheckRaw = getEndpointLastCheckDisplayValue(endpoint);
  const lastCheck = lastCheckRaw === '-' ? '-' : formatTimestamp(lastCheckRaw);

  const routingMode = routingState?.mode || 'auto';
  const routingTarget = routingState?.endpointName || routingState?.endpoint_name || '';
  const routingSelected = routingMode !== 'auto' && routingTarget === endpoint.name;

  const disabled = !endpoint.availabilityEnabled;

  return (
    <div
      className={`flex flex-col rounded-2xl border p-4 shadow-sm transition ${
        disabled
          ? 'border-line bg-surface-sub text-fg-subtle'
          : routingSelected
            ? 'border-accent-line bg-surface ring-1 ring-accent-line'
            : 'border-line bg-surface hover:border-line-strong'
      }`}
    >
      <div className="flex items-start justify-between gap-2">
        <div className="flex min-w-0 items-center gap-2">
          <span className={`h-2 w-2 shrink-0 rounded-full ${statusDotClass(endpoint)}`} />
          <span className="truncate font-semibold text-fg" title={endpoint.name}>{endpoint.name}</span>
        </div>
        <ActionButtons endpointName={endpoint.name} routingState={routingState} onSetRouting={onSetRouting} disabled={busy || disabled || modelRoutingEnabled} disabledReason="模型路由开启中，请在模型路由面板按模型设置" />
      </div>
      <div className="mt-1 truncate font-mono text-[11px] text-fg-subtle" title={endpoint.url}>{endpoint.url}</div>

      <div className="mt-3 flex flex-wrap items-center gap-1.5">
        <span className="inline-flex items-center gap-1.5 rounded-lg border border-line bg-surface-sub px-2 py-1 text-xs text-fg-body"><ShieldCheck size={13} className="text-warn-solid" />{summarizeEndpointAuthentication(endpoint)}</span>
        <span className="inline-flex items-center rounded-full bg-surface-mut px-2 py-1 font-mono text-xs font-semibold text-fg-body">P{endpoint.priority ?? 1}</span>
        {rewrite && <span className="tone-cyan max-w-[160px] truncate rounded-md border px-2 py-1 text-[11px]" title={rewrite.title}>{rewrite.label}</span>}
      </div>

      <div className="mt-3 space-y-1.5 text-xs">
        <div className="flex h-5 flex-wrap items-center gap-x-2 gap-y-1">
          <EndpointHealthBadge endpoint={endpoint} />
          <span className="text-[11px] text-fg-subtle">{lastCheck || '-'}</span>
          {endpoint.responseTimeMs > 0 && <span className="font-mono text-[11px] text-fg-subtle">{Math.round(endpoint.responseTimeMs)}ms</span>}
        </div>
        <div className="flex h-5 items-center">
          <EndpointCooldownInfo endpoint={endpoint} busy={busy} onClearCooldown={onClearCooldown} compact />
        </div>
      </div>

      <div className="mt-4 flex items-center justify-between gap-2 border-t border-line-soft pt-3">
        <div className="flex items-center gap-4">
          <label className="flex items-center gap-1.5" title="硬启用">
            <ToggleSwitch enabled={endpoint.availabilityEnabled} disabled={busy} onChange={() => onAvailabilityChange?.(endpoint.name, !endpoint.availabilityEnabled)} title="硬启用" />
            <span className="text-xs text-fg-muted">启用</span>
          </label>
          <label className="flex items-center gap-1.5" title="参与自动调度">
            <ToggleSwitch enabled={endpoint.failoverEnabled} disabled={busy} onChange={() => onAutoScheduleChange?.(endpoint.name, !endpoint.failoverEnabled)} title="参与自动调度" />
            <span className="text-xs text-fg-muted">调度</span>
          </label>
        </div>
        <EndpointQuickActions endpoint={endpoint} busy={busy} onEdit={onEdit} onDelete={onDelete} onTest={onTest} />
      </div>
    </div>
  );
};

export default EndpointCard;
