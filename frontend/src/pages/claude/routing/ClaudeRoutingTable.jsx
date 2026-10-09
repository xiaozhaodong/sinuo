import { Globe } from 'lucide-react';
import { CustomSelect } from '@components/ui';
import Switch from '@components/desktop/Switch.jsx';
import { ENDPOINT_STATUS_LABELS, ROUTE_MODES, endpointStatusTone, isWebToolsBinding } from '../../endpoints/utils/modelRouting.js';
import { routingOutcomeLabel, routingOutcomeTone } from '@components/model-routing/routingView.js';
import { MemberChipsCell, RecentHitCell } from '@components/model-routing/RoutingTableCells.jsx';
import { claudeRouteModePatch } from './routingView.js';

const ModelCell = ({ binding }) => (
  <td className="max-w-[200px]">
    <div className="flex min-w-0 items-center gap-1.5">
      {isWebToolsBinding(binding) ? (
        <span className={`flex min-w-0 items-center gap-1.5 ${binding.note ? 'max-w-[70%] shrink-0' : 'flex-1'}`}>
          <Globe size={13} className="shrink-0 text-accent" />
          <span className="shrink-0 whitespace-nowrap">联网工具</span>
          <span className="min-w-0 flex-1 truncate font-mono text-[11px] text-fg-muted" title="请求 tools 里带 web_search / web_fetch 时优先命中，不看模型名">web_search / web_fetch</span>
        </span>
      ) : <span className={`min-w-0 truncate font-mono text-[12.5px] ${binding.note ? 'max-w-[70%] shrink-0' : 'flex-1'}`} title={binding.model}>{binding.model}</span>}
      {binding.note && <span className="min-w-0 flex-1 truncate text-[11px] text-fg-muted" title={binding.note}>{binding.note}</span>}
    </div>
  </td>
);

const EndpointChips = ({ binding }) => (
  <MemberChipsCell
    members={binding.endpoints.map((endpoint) => {
      const statusLabel = ENDPOINT_STATUS_LABELS[endpoint.status] || endpoint.status;
      return {
        key: endpoint.name,
        label: `${endpoint.name}${endpoint.status !== 'ok' ? ` · ${statusLabel}` : ''}`,
        tone: endpointStatusTone(endpoint.status),
        title: `${endpoint.name} · ${endpoint.detail || statusLabel}`,
        className: endpoint.name === binding.route_target ? 'font-semibold' : ''
      };
    })}
    emptyText="未选择任何端点"
  />
);

const RoutingRow = ({ binding, selectedId, busy, onSelect, onPatch, onRowContextMenu }) => (
  <tr data-row-id={binding.assignment_id} aria-selected={selectedId === binding.assignment_id}
    className={binding.enabled ? undefined : 'text-fg-subtle'} onClick={() => onSelect(binding.assignment_id)}
    onContextMenu={(event) => onRowContextMenu(event, binding)}>
    <ModelCell binding={binding} />
    <EndpointChips binding={binding} />
    <td onClick={(event) => event.stopPropagation()} onKeyDown={(event) => event.stopPropagation()}>
      <CustomSelect options={ROUTE_MODES} value={binding.route_mode} disabled={busy} size="xs" className="w-[80px]"
        onChange={(mode) => onPatch(binding, claudeRouteModePatch(binding, mode))} />
    </td>
    <td onClick={(event) => event.stopPropagation()} onKeyDown={(event) => event.stopPropagation()}>
      <CustomSelect options={binding.endpoints.map((endpoint) => ({ value: endpoint.name, label: endpoint.name }))}
        value={binding.route_target} disabled={busy || binding.route_mode === 'auto'} size="sm" className="w-[130px]"
        placeholder={binding.route_mode === 'auto' ? '自动挑选' : '选择目标端点'}
        onChange={(target) => onPatch(binding, { route_target: target })} />
    </td>
    <RecentHitCell
      name={binding.last_effective_endpoint}
      at={binding.last_decision_at}
      outcomeLabel={binding.last_outcome ? routingOutcomeLabel(binding.last_outcome) : undefined}
      outcomeTone={routingOutcomeTone(binding.last_outcome)}
    />
    <td onClick={(event) => event.stopPropagation()} onKeyDown={(event) => event.stopPropagation()}>
      <Switch checked={binding.enabled} disabled={busy} label={`启用映射 ${isWebToolsBinding(binding) ? '联网工具' : binding.model}`}
        onChange={(enabled) => onPatch(binding, { enabled })} />
    </td>
  </tr>
);

const ClaudeRoutingTable = (props) => {
  return (
    <table className="desk-table min-w-[700px]">
      <thead><tr><th>模型</th><th>端点</th><th className="w-[96px]">策略</th><th className="w-[146px]">目标</th><th className="@max-[900px]:hidden">最近命中</th><th className="w-14">启用</th></tr></thead>
      <tbody>{props.bindings.map((binding) => <RoutingRow key={binding.assignment_id} {...props} binding={binding} />)}</tbody>
    </table>
  );
};

export default ClaudeRoutingTable;
