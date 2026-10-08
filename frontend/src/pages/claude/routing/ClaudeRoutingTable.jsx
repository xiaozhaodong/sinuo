import { Globe } from 'lucide-react';
import { CustomSelect } from '@components/ui';
import Switch from '@components/desktop/Switch.jsx';
import { useTimezone } from '@contexts/TimezoneContext.jsx';
import { ENDPOINT_STATUS_LABELS, ROUTE_MODES, endpointStatusTone, isWebToolsBinding } from '../../endpoints/utils/modelRouting.js';
import { routingOutcomeLabel } from '@components/model-routing/routingView.js';
import { claudeRouteModePatch } from './routingView.js';

const ModelCell = ({ binding }) => (
  <td className="max-w-[240px]">
    {isWebToolsBinding(binding) ? (
      <div title="请求 tools 里带 web_search / web_fetch 时优先命中，不看模型名" className="flex items-center gap-1.5">
        <Globe size={13} className="shrink-0 text-accent" />
        <span className="whitespace-nowrap">联网工具</span>
        <span className="truncate font-mono text-[11px] text-fg-muted">web_search / web_fetch</span>
      </div>
    ) : <div className="truncate font-mono text-[12.5px]" title={binding.model}>{binding.model}</div>}
    {binding.note && <div className="truncate text-[11px] text-fg-muted" title={binding.note}>{binding.note}</div>}
  </td>
);

const EndpointChips = ({ binding }) => (
  <td className="max-w-[260px]">
    <div className="flex flex-wrap gap-1 py-1.5">
      {binding.endpoints.map((endpoint) => (
        <span key={endpoint.name} title={endpoint.detail || ENDPOINT_STATUS_LABELS[endpoint.status] || endpoint.status}
          className={`${endpointStatusTone(endpoint.status)} max-w-[180px] truncate rounded border px-1.5 text-[11px] leading-[18px] ${endpoint.name === binding.route_target ? 'font-semibold' : ''}`}>
          {endpoint.name}{endpoint.status !== 'ok' && ` · ${ENDPOINT_STATUS_LABELS[endpoint.status] || endpoint.status}`}
        </span>
      ))}
      {binding.endpoints.length === 0 && <span className="text-danger">未选择任何端点</span>}
    </div>
  </td>
);

const RoutingRow = ({ binding, selectedId, busy, onSelect, onPatch, onRowContextMenu, formatTimestamp }) => (
  <tr data-row-id={binding.assignment_id} aria-selected={selectedId === binding.assignment_id}
    className={binding.enabled ? undefined : 'text-fg-subtle'} onClick={() => onSelect(binding.assignment_id)}
    onContextMenu={(event) => onRowContextMenu(event, binding)}>
    <ModelCell binding={binding} />
    <EndpointChips binding={binding} />
    <td onClick={(event) => event.stopPropagation()} onKeyDown={(event) => event.stopPropagation()}>
      <CustomSelect options={ROUTE_MODES} value={binding.route_mode} disabled={busy} size="sm" className="w-[100px]"
        onChange={(mode) => onPatch(binding, claudeRouteModePatch(binding, mode))} />
    </td>
    <td onClick={(event) => event.stopPropagation()} onKeyDown={(event) => event.stopPropagation()}>
      <CustomSelect options={binding.endpoints.map((endpoint) => ({ value: endpoint.name, label: endpoint.name }))}
        value={binding.route_target} disabled={busy || binding.route_mode === 'auto'} size="sm" className="w-[150px]"
        placeholder={binding.route_mode === 'auto' ? '自动挑选' : '选择目标端点'}
        onChange={(target) => onPatch(binding, { route_target: target })} />
    </td>
    <td className="max-w-[160px] text-[11.5px] text-fg-muted">
      <div className="truncate" title={binding.last_effective_endpoint}>{binding.last_effective_endpoint || '—'}</div>
      {binding.last_decision_at && <div className="truncate" title={formatTimestamp(binding.last_decision_at)}>{formatTimestamp(binding.last_decision_at)}</div>}
      {binding.last_outcome && <div className="truncate">{routingOutcomeLabel(binding.last_outcome)}</div>}
    </td>
    <td onClick={(event) => event.stopPropagation()} onKeyDown={(event) => event.stopPropagation()}>
      <Switch checked={binding.enabled} disabled={busy} label={`启用映射 ${isWebToolsBinding(binding) ? '联网工具' : binding.model}`}
        onChange={(enabled) => onPatch(binding, { enabled })} />
    </td>
  </tr>
);

const ClaudeRoutingTable = (props) => {
  const { formatTimestamp } = useTimezone();
  return (
    <table className="desk-table min-w-[820px]">
      <thead><tr><th>模型</th><th>端点</th><th className="w-[116px]">策略</th><th className="w-[166px]">目标</th><th>最近命中</th><th className="w-14">启用</th></tr></thead>
      <tbody>{props.bindings.map((binding) => <RoutingRow key={binding.assignment_id} {...props} binding={binding} formatTimestamp={formatTimestamp} />)}</tbody>
    </table>
  );
};

export default ClaudeRoutingTable;
