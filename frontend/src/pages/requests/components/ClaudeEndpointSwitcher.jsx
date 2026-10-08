// 请求页 Claude 浮层的「端点策略」：自动调度 + 端点列表（方案 A）；模型路由开启时换成模型映射列表
// 2026-10-07 桌面化：浮层小标题 + 26px 行；scopeLabel 标注作用范围（如订阅直通时「仅影响主端口」）
import { useMemo } from 'react';
import { PopoverNote, PopoverSection } from '@components/desktop/PopoverParts.jsx';
import ModelRoutingSwitcher from './ModelRoutingSwitcher.jsx';
import AutoScheduleRow from './route-switcher/AutoScheduleRow.jsx';
import RouteTargetRow from './route-switcher/RouteTargetRow.jsx';

const ClaudeEndpointSwitcher = ({
  endpoints = [],
  routingState = {},
  onSwitch,
  onRestoreAuto,
  loading = false,
  modelRoutingEnabled = false,
  modelRouteBindings = [],
  onModelRoutePatch,
  modelRoutePatching = false,
  scopeLabel = ''
}) => {
  const sorted = useMemo(() => [...endpoints].filter((endpoint) => endpoint.availabilityEnabled !== false).sort((left, right) => (left.priority ?? 1) - (right.priority ?? 1) || String(left.name).localeCompare(String(right.name))), [endpoints]);
  const mode = routingState.mode || 'auto';
  const target = routingState.endpointName || routingState.endpoint_name || '';
  const lastEffectiveEndpoint = routingState.lastEffectiveEndpoint || routingState.last_effective_endpoint || '';

  if (modelRoutingEnabled) {
    return <ModelRoutingSwitcher bindings={modelRouteBindings} onPatch={onModelRoutePatch} loading={modelRoutePatching} scopeLabel={scopeLabel} />;
  }

  return (
    <>
      <PopoverSection title="端点策略" aside={scopeLabel} />
      {sorted.length === 0 ? <PopoverNote>无可用 Claude 端点</PopoverNote> : (
        <>
          <AutoScheduleRow
            isCurrent={mode === 'auto'}
            onRestore={onRestoreAuto}
            loading={loading}
            meta={mode === 'auto' && lastEffectiveEndpoint ? `最近 ${lastEffectiveEndpoint}` : ''}
          />
          {sorted.map((endpoint) => (
            <RouteTargetRow
              key={endpoint.name}
              target={{
                name: endpoint.name,
                title: endpoint.detail || endpoint.name,
                priority: endpoint.priority,
                // 自动模式下「最近命中」已写在自动调度行上，这里只在手动模式下标出（可能因回落而不同于目标）
                lastHit: mode !== 'auto' && lastEffectiveEndpoint === endpoint.name,
                indicatorClass: endpoint.healthy ? 'bg-success-solid' : endpoint.neverChecked ? 'bg-fg-subtle/60' : 'bg-danger-solid'
              }}
              mode={mode}
              isCurrent={mode !== 'auto' && target === endpoint.name}
              loading={loading}
              onCommit={(patch) => onSwitch?.(patch.route_target, patch.route_mode)}
            />
          ))}
        </>
      )}
    </>
  );
};

export default ClaudeEndpointSwitcher;
