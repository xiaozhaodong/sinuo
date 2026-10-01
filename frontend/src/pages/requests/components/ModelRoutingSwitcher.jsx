import { useMemo, useState } from 'react';
import { AlertCircle } from 'lucide-react';
import { ENDPOINT_STATUS_LABELS, bindingDisplayName, isWebToolsBinding } from '../../endpoints/utils/modelRouting.js';
import BindingList from './route-switcher/BindingList.jsx';
import AutoScheduleRow from './route-switcher/AutoScheduleRow.jsx';
import RouteTargetRow from './route-switcher/RouteTargetRow.jsx';
import ManageFooter from './route-switcher/ManageFooter.jsx';

const describeMode = (binding) => {
  const count = (binding.endpoints || []).length;
  if (binding.route_mode === 'manual_fixed') return `只用 ${binding.route_target || '指定端点'}，不可用时直接失败`;
  if (binding.route_mode === 'manual_preferred') return `优先用 ${binding.route_target || '指定端点'}，不可用时回落到其余端点`;
  return `在 ${count} 个端点里按优先级与健康状态挑选`;
};

const ModelRoutingSwitcher = ({ bindings = [], onPatch, loading = false, onOpenModelRouting }) => {
  const [selectedId, setSelectedId] = useState(null);
  const sorted = useMemo(() => [...bindings].sort((a, b) => Number(isWebToolsBinding(b)) - Number(isWebToolsBinding(a)) || Number(b.enabled) - Number(a.enabled) || bindingDisplayName(a).localeCompare(bindingDisplayName(b))), [bindings]);
  const selected = sorted.find((binding) => binding.assignment_id === selectedId) || sorted[0] || null;
  const patch = async (binding, change) => onPatch?.(binding, change);

  if (!sorted.length) {
    return <div><div className="flex items-center gap-2 px-4 py-6 text-sm text-fg-subtle"><AlertCircle className="h-4 w-4" />尚无模型映射，所有 Claude 请求都会返回未分配错误</div><ManageFooter description="增删映射、调整端点集合在「Claude 管理 → 端点」" onClick={onOpenModelRouting} /></div>;
  }

  return (
    <div>
      <div className="flex max-h-[380px]">
        <BindingList
          bindings={sorted}
          selectedId={selected?.assignment_id}
          onSelect={setSelectedId}
          displayName={bindingDisplayName}
          isWebTools={isWebToolsBinding}
          modeOf={(binding) => binding.route_mode}
          getSubtitle={(binding) => {
            if (!binding.enabled) return '已停用';
            if (binding.route_mode !== 'auto') return `→ ${binding.route_target || '未选择'}`;
            return binding.last_effective_endpoint ? `自动 · 最近 ${binding.last_effective_endpoint}` : '自动';
          }}
        />
        <div className="min-w-0 flex-1 max-h-[380px] overflow-y-auto">
          {selected && !selected.enabled && <div className="px-4 py-4 text-xs text-fg-subtle">该映射已停用，请到端点页启用后再切换策略</div>}
          {selected && selected.enabled && (
            <>
              <div className="px-4 pb-2 pt-3"><h3 className="text-sm font-semibold text-fg">{isWebToolsBinding(selected) ? '联网工具' : <span className="font-mono">{selected.model}</span>}</h3><p className="mt-0.5 text-[11px] text-fg-muted">{describeMode(selected)}</p></div>
              <div className="px-2 pb-2">
                <AutoScheduleRow isCurrent={selected.route_mode === 'auto'} onRestore={() => patch(selected, { route_mode: 'auto' })} loading={loading} />
                {(selected.endpoints || []).length === 0 && <div className="px-2 py-3 text-xs text-fg-subtle">未选择任何端点</div>}
                {(selected.endpoints || []).map((endpoint) => <RouteTargetRow key={endpoint.name} target={{ name: endpoint.name, title: endpoint.detail || endpoint.name, value: endpoint.name, priority: endpoint.priority, statusLabel: endpoint.status === 'ok' ? undefined : (ENDPOINT_STATUS_LABELS[endpoint.status] || endpoint.status), statusClass: endpoint.status === 'ok' ? 'text-success' : endpoint.status === 'cooldown' || endpoint.status === 'auto_schedule_disabled' ? 'text-warn' : 'text-danger', indicatorClass: endpoint.status === 'ok' ? 'bg-success-solid' : endpoint.status === 'cooldown' || endpoint.status === 'auto_schedule_disabled' ? 'bg-warn-solid' : 'bg-danger-solid', lastHit: selected.last_effective_endpoint === endpoint.name }} mode={selected.route_mode} isCurrent={selected.route_mode !== 'auto' && selected.route_target === endpoint.name} loading={loading} onCommit={(change) => patch(selected, change)} />)}
              </div>
            </>
          )}
        </div>
      </div>
      <ManageFooter description="增删映射、调整端点集合在「Claude 管理 → 端点」" onClick={onOpenModelRouting} />
    </div>
  );
};

export default ModelRoutingSwitcher;
