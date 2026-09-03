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

const EndpointRow = ({ endpoint, routingState, busy, onEdit, onDelete, onTest, onAvailabilityChange, onAutoScheduleChange, onSetRouting, onClearCooldown, modelRoutingEnabled = false }) => {
  const { formatTimestamp } = useTimezone();
  const rewrite = summarizeEndpointModelRewriteRules(endpoint.modelRewriteRules || '');
  const lastCheckRaw = getEndpointLastCheckDisplayValue(endpoint);
  const lastCheck = lastCheckRaw === '-' ? '-' : formatTimestamp(lastCheckRaw);
  return (
    <tr className={`${endpoint.availabilityEnabled ? 'bg-surface hover:bg-surface-sub' : 'bg-surface-sub text-fg-subtle'} transition-colors`}>
      <td className="px-3 py-3 align-top">
        <div className="font-semibold text-fg">{endpoint.name}</div>
        <div className="mt-1 max-w-[200px] truncate font-mono text-[11px] text-fg-subtle" title={endpoint.url}>{endpoint.url}</div>
      </td>
      <td className="px-3 py-3 align-top">
        <div className="inline-flex items-center gap-1.5 rounded-lg border border-line bg-surface-sub px-2 py-1 text-xs text-fg-body"><ShieldCheck size={13} className="text-warn-solid" />{summarizeEndpointAuthentication(endpoint)}</div>
      </td>
      <td className="px-3 py-3 text-center align-top"><span className="inline-flex min-w-8 justify-center rounded-full bg-surface-mut px-2 py-1 font-mono text-xs font-semibold text-fg-body">{endpoint.priority ?? 1}</span></td>
      <td className="px-3 py-3 align-top">
        <div className="flex items-center gap-2"><ToggleSwitch enabled={endpoint.availabilityEnabled} disabled={busy} onChange={() => onAvailabilityChange?.(endpoint.name, !endpoint.availabilityEnabled)} title="硬启用" /><span className="text-xs text-fg-muted">{endpoint.availabilityEnabled ? '启用' : '停用'}</span></div>
      </td>
      <td className="px-3 py-3 align-top">
        <div className="flex items-center gap-2"><ToggleSwitch enabled={endpoint.failoverEnabled} disabled={busy} onChange={() => onAutoScheduleChange?.(endpoint.name, !endpoint.failoverEnabled)} title="参与自动调度" /><span className="text-xs text-fg-muted">{endpoint.failoverEnabled ? '参与' : '排除'}</span></div>
      </td>
      <td className="px-3 py-3 align-top">
        <EndpointHealthBadge endpoint={endpoint} />
        <div className="mt-1 text-[11px] text-fg-subtle">{lastCheck || '-'}</div>
        {endpoint.responseTimeMs > 0 && <div className="text-[11px] font-mono text-fg-subtle">{Math.round(endpoint.responseTimeMs)}ms</div>}
      </td>
      <td className="px-3 py-3 align-top">
        <EndpointCooldownInfo endpoint={endpoint} busy={busy} onClearCooldown={onClearCooldown} className="max-w-[150px]" emptyFallback={<span className="text-xs text-fg-subtle/60">—</span>} />
      </td>
      <td className="px-3 py-3 align-top">{rewrite ? <span className="tone-cyan block max-w-[150px] truncate rounded-md border px-2 py-1 text-[11px]" title={rewrite.title}>{rewrite.label}</span> : <span className="text-xs text-fg-subtle/60">无</span>}</td>
      <td className="px-3 py-3 align-top">
        <ActionButtons endpointName={endpoint.name} routingState={routingState} onSetRouting={onSetRouting} disabled={busy || !endpoint.availabilityEnabled || modelRoutingEnabled} disabledReason="模型路由开启中，请在模型路由面板按模型设置" />
        <EndpointQuickActions endpoint={endpoint} busy={busy} onEdit={onEdit} onDelete={onDelete} onTest={onTest} className="mt-2 justify-end" />
      </td>
    </tr>
  );
};

export default EndpointRow;
