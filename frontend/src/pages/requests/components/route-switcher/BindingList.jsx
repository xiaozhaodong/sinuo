import { Globe } from 'lucide-react';
import RouteModeTag from './RouteModeTag.jsx';

const BindingList = ({ bindings = [], selectedId, onSelect, displayName, isWebTools, getSubtitle, modeOf }) => (
  <div className="w-[210px] shrink-0 max-h-[380px] overflow-y-auto border-r border-line-soft py-1">
    {bindings.map((binding) => {
      const active = binding.assignment_id === selectedId;
      const webTools = isWebTools?.(binding);
      const name = displayName(binding);
      const mode = modeOf?.(binding) || binding.route_mode;
      return (
        <button
          key={binding.assignment_id}
          type="button"
          onClick={() => onSelect?.(binding.assignment_id)}
          className={`relative flex w-full items-center gap-2 px-3 py-2 text-left ${active ? 'bg-surface-sub' : 'hover:bg-surface-sub'}`}
        >
          {active && <span className="absolute inset-y-0 left-0 w-[3px] bg-accent" />}
          <div className="min-w-0 flex-1 pl-0.5">
            <div className={`flex min-w-0 items-center gap-1.5 truncate text-xs ${webTools ? '' : 'font-mono'} ${binding.enabled ? 'text-fg-body' : 'text-fg-subtle line-through'}`} title={webTools ? name : binding.model}>
              {webTools && <Globe size={12} className="shrink-0 text-fg-subtle" />}
              <span className="truncate">{webTools ? '联网工具' : binding.model}</span>
            </div>
            <div className="mt-0.5 truncate text-[10px] text-fg-subtle">{getSubtitle?.(binding)}</div>
          </div>
          {binding.enabled && mode !== 'auto' && <RouteModeTag mode={mode} />}
        </button>
      );
    })}
  </div>
);

export default BindingList;
