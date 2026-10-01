import { useMemo } from 'react';
import { AlertCircle } from 'lucide-react';
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
  onOpenModelRouting,
  embedded = false
}) => {
  const sorted = useMemo(() => [...endpoints].filter((endpoint) => endpoint.availabilityEnabled !== false).sort((left, right) => (left.priority ?? 1) - (right.priority ?? 1) || String(left.name).localeCompare(String(right.name))), [endpoints]);
  const mode = routingState.mode || 'auto';
  const target = routingState.endpointName || routingState.endpoint_name || '';
  const lastEffectiveEndpoint = routingState.lastEffectiveEndpoint || routingState.last_effective_endpoint || '';

  if (modelRoutingEnabled) return <ModelRoutingSwitcher bindings={modelRouteBindings} onPatch={onModelRoutePatch} loading={modelRoutePatching} onOpenModelRouting={onOpenModelRouting} />;
  if (sorted.length === 0) return <div className="flex items-center gap-2 px-4 py-6 text-sm text-fg-subtle"><AlertCircle className="h-4 w-4" />无可用 Claude 端点</div>;

  return (
    <div className={embedded ? '' : 'w-[310px]'}>
      <div className="max-h-[380px] overflow-y-auto p-2">
        <AutoScheduleRow isCurrent={mode === 'auto'} onRestore={onRestoreAuto} loading={loading} />
        {sorted.map((endpoint) => (
          <RouteTargetRow
            key={endpoint.name}
            target={{
              name: endpoint.name,
              title: endpoint.detail || endpoint.name,
              priority: endpoint.priority,
              lastHit: lastEffectiveEndpoint === endpoint.name,
              indicatorClass: endpoint.healthy ? 'bg-success-solid' : endpoint.neverChecked ? 'bg-fg-subtle/60' : 'bg-danger-solid'
            }}
            mode={mode}
            isCurrent={mode !== 'auto' && target === endpoint.name}
            loading={loading}
            onCommit={(patch) => onSwitch?.(patch.route_target, patch.route_mode)}
          />
        ))}
      </div>
    </div>
  );
};

export default ClaudeEndpointSwitcher;
