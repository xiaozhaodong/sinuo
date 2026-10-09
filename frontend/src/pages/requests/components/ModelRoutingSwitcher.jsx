// 请求页 Claude 浮层：模型路由开启时的映射列表，每个模型一行写当前策略，点开切换它的端点（方案 A）
// 2026-10-07 桌面化：左右两栏改为可展开列表；增删映射走浮层底部的「模型路由…」
import { useMemo, useState } from 'react';
import { PopoverNote, PopoverSection } from '@components/desktop/PopoverParts.jsx';
import { ENDPOINT_STATUS_LABELS, bindingDisplayName, isWebToolsBinding } from '../../endpoints/utils/modelRouting.js';
import AutoScheduleRow from './route-switcher/AutoScheduleRow.jsx';
import BindingTree from './route-switcher/BindingTree.jsx';
import RouteTargetRow from './route-switcher/RouteTargetRow.jsx';

const MODE_NAMES = { manual_preferred: '优选', manual_fixed: '固定' };

const describeMode = (binding) => {
  const count = (binding.endpoints || []).length;
  if (binding.route_mode === 'manual_fixed') return `只用 ${binding.route_target || '指定端点'}，不可用时直接失败`;
  if (binding.route_mode === 'manual_preferred') return `优先用 ${binding.route_target || '指定端点'}，不可用时回落到其余端点`;
  return `在 ${count} 个端点里按优先级与健康状态挑选`;
};

const summarizeBinding = (binding) => {
  if (!binding.enabled) return '已停用';
  if (binding.route_mode !== 'auto') return `${MODE_NAMES[binding.route_mode] || ''} → ${binding.route_target || '未选择'}`;
  return binding.last_effective_endpoint ? `自动 · 最近 ${binding.last_effective_endpoint}` : '自动';
};

const endpointTone = (status) => {
  if (status === 'ok') return { statusClass: 'text-success', indicatorClass: 'bg-success-solid' };
  if (status === 'cooldown' || status === 'auto_schedule_disabled') return { statusClass: 'text-warn', indicatorClass: 'bg-warn-solid' };
  return { statusClass: 'text-danger', indicatorClass: 'bg-danger-solid' };
};

const ModelRoutingSwitcher = ({ bindings = [], onPatch, loading = false, scopeLabel = '' }) => {
  const [expandedId, setExpandedId] = useState(null);
  const sorted = useMemo(() => [...bindings].sort((a, b) => Number(isWebToolsBinding(b)) - Number(isWebToolsBinding(a)) || Number(b.enabled) - Number(a.enabled) || bindingDisplayName(a).localeCompare(bindingDisplayName(b))), [bindings]);
  const patch = (binding, change) => onPatch?.(binding, change);

  const renderTargets = (binding) => {
    if (!binding.enabled) return <PopoverNote nested>该映射已停用，请到端点页启用后再切换策略</PopoverNote>;
    const endpoints = binding.endpoints || [];
    return (
      <>
        <AutoScheduleRow indent isCurrent={binding.route_mode === 'auto'} onRestore={() => patch(binding, { route_mode: 'auto' })} loading={loading} title={describeMode(binding)} />
        {endpoints.length === 0 && <PopoverNote nested>未选择任何端点</PopoverNote>}
        {endpoints.map((endpoint) => (
          <RouteTargetRow
            key={endpoint.name}
            indent
            target={{
              name: endpoint.name,
              title: endpoint.detail || endpoint.name,
              value: endpoint.name,
              priority: endpoint.priority,
              statusLabel: endpoint.status === 'ok' ? undefined : (ENDPOINT_STATUS_LABELS[endpoint.status] || endpoint.status),
              lastHit: binding.last_effective_endpoint === endpoint.name,
              ...endpointTone(endpoint.status)
            }}
            mode={binding.route_mode}
            isCurrent={binding.route_mode !== 'auto' && binding.route_target === endpoint.name}
            loading={loading}
            onCommit={(change) => patch(binding, change)}
          />
        ))}
      </>
    );
  };

  return (
    <>
      <PopoverSection title={sorted.length ? `模型路由 · ${sorted.length} 个映射` : '模型路由'} aside={scopeLabel} />
      {sorted.length === 0
        ? <PopoverNote>尚无模型映射，所有 Claude 请求都会返回未分配错误；在下方「模型路由…」里添加</PopoverNote>
        : <BindingTree bindings={sorted} expandedId={expandedId} onToggle={setExpandedId} isWebTools={isWebToolsBinding} summaryOf={summarizeBinding} strategyKeyOf={(b) => `${b.enabled}|${b.route_mode}|${b.route_target || ''}`} renderTargets={renderTargets} />}
    </>
  );
};

export default ModelRoutingSwitcher;
