import { LockKeyhole, Route } from 'lucide-react';
import RouteModeTag from './RouteModeTag.jsx';

const ACTIONS = {
  manual_preferred: { label: '优选', Icon: Route, hover: 'hover:border-accent-line hover:text-accent', tone: 'tone-indigo' },
  manual_fixed: { label: '固定', Icon: LockKeyhole, hover: 'hover:border-rose-300 hover:text-rose-500', tone: 'tone-rose' }
};

const RouteTargetRow = ({
  target,
  mode = 'auto',
  isCurrent = false,
  onCommit,
  loading = false,
  preferredDisabled = false,
  preferredTitle = '优先用此目标，不可用时回落',
  fixedTitle = '只用此目标，不可用时直接失败'
}) => {
  const currentMode = isCurrent ? mode : '';
  return (
    <div className={`group flex h-9 items-center gap-2 rounded-lg px-2 transition-colors ${isCurrent ? 'bg-surface-sub' : 'hover:bg-surface-sub has-[:focus-visible]:bg-surface-sub'}`}>
      <span className={`h-2 w-2 shrink-0 rounded-full ${target.indicatorClass || 'bg-fg-subtle/60'}`} />
      <span className={`min-w-0 flex-1 truncate text-xs ${isCurrent ? 'font-medium text-fg' : 'text-fg-body'}`} title={target.title || target.name}>{target.name}</span>
      {target.lastHit && <span className="shrink-0 rounded bg-surface-mut px-1 text-[10px] text-fg-muted">最近命中</span>}
      {target.statusLabel && <span className={`shrink-0 text-[10px] ${target.statusClass || 'text-fg-subtle'}`}>{target.statusLabel}</span>}
      {target.priority !== undefined && target.priority !== null && !target.statusLabel && <span className="shrink-0 font-mono text-[10px] text-fg-subtle">P{target.priority}</span>}
      <div className="relative flex h-6 w-[108px] shrink-0 items-center justify-end">
        {currentMode && <span className="absolute right-0 transition-opacity group-hover:opacity-0 group-has-[:focus-visible]:opacity-0"><RouteModeTag mode={currentMode} /></span>}
        <div className="absolute right-0 flex gap-1 opacity-0 transition-opacity group-hover:opacity-100 group-has-[:focus-visible]:opacity-100">
          {Object.entries(ACTIONS).map(([actionMode, action]) => {
            const { Icon } = action;
            const disabled = loading || (actionMode === 'manual_preferred' && preferredDisabled);
            return (
              <button
                key={actionMode}
                type="button"
                disabled={disabled}
                title={actionMode === 'manual_preferred' ? preferredTitle : fixedTitle}
                onClick={() => onCommit?.({ route_mode: actionMode, route_target: target.value ?? target.name })}
                className={`inline-flex h-6 items-center gap-1 rounded-md border px-1.5 text-[11px] disabled:opacity-50 ${actionMode === currentMode ? action.tone : `border-line bg-surface text-fg-muted ${action.hover}`}`}
              >
                <Icon size={11} />{action.label}
              </button>
            );
          })}
        </div>
      </div>
    </div>
  );
};

export default RouteTargetRow;
