import { useEffect, useMemo, useRef, useState } from 'react';
import { AlertCircle, Check, LockKeyhole, RotateCcw, Route } from 'lucide-react';
import ModelRoutingSwitcher from './ModelRoutingSwitcher.jsx';
import { resolveClaudeRouteLabel } from '../utils/claudeRouteLabel.js';

// Claude 端点策略：全局路由的自动 / 优选 / 固定；模型路由启用时换成按模型的策略面板。
// embedded：嵌在 Claude 接入面板里，不渲染触发按钮，直接展开内容
const ClaudeEndpointSwitcher = ({ endpoints = [], routingState = {}, onSwitch, onRestoreAuto, loading = false, modelRoutingEnabled = false, modelRouteBindings = [], onModelRoutePatch, modelRoutePatching = false, embedded = false }) => {
  const [open, setOpen] = useState(false);
  const ref = useRef(null);
  const sorted = useMemo(() => [...endpoints].filter((endpoint) => endpoint.availabilityEnabled !== false).sort((left, right) => (left.priority ?? 1) - (right.priority ?? 1) || String(left.name).localeCompare(String(right.name))), [endpoints]);
  const mode = routingState.mode || 'auto';
  const target = routingState.endpointName || routingState.endpoint_name || '';

  useEffect(() => {
    const close = (event) => { if (ref.current && !ref.current.contains(event.target)) setOpen(false); };
    document.addEventListener('mousedown', close);
    return () => document.removeEventListener('mousedown', close);
  }, []);

  if (sorted.length === 0) return <div className="flex h-9 items-center gap-2 rounded-lg border border-line bg-surface-sub px-3 text-sm text-fg-subtle"><AlertCircle className="h-3.5 w-3.5" />无可用 Claude 端点</div>;

  if (modelRoutingEnabled) return <ModelRoutingSwitcher embedded={embedded} bindings={modelRouteBindings} onPatch={onModelRoutePatch} loading={modelRoutePatching} />;

  const choose = async (action) => {
    await action?.();
    setOpen(false);
  };
  const options = (
    <>
      <button type="button" onClick={() => choose(onRestoreAuto)} className={`flex w-full items-center gap-2 border-b border-line-soft px-4 py-3 text-left text-sm ${mode === 'auto' ? 'bg-accent-soft text-accent-fg' : 'text-fg-body hover:bg-surface-sub'}`}><RotateCcw size={14} />自动调度{mode === 'auto' && <Check size={14} className="ml-auto" />}</button>
      <div className="max-h-[340px] overflow-y-auto p-2">
        {sorted.map((endpoint) => (
          <div key={endpoint.name} className="rounded-lg px-2 py-2 hover:bg-surface-sub">
            <div className="flex items-center gap-2"><span className={`h-2 w-2 rounded-full ${endpoint.healthy ? 'bg-success-solid' : endpoint.neverChecked ? 'bg-fg-subtle/60' : 'bg-danger-solid'}`} /><span className="min-w-0 flex-1 truncate text-sm font-medium text-fg-body">{endpoint.name}</span><span className="font-mono text-[10px] text-fg-subtle">P{endpoint.priority}</span></div>
            <div className="mt-2 flex gap-1.5"><button type="button" onClick={() => choose(() => onSwitch?.(endpoint.name, 'manual_preferred'))} className={`inline-flex flex-1 items-center justify-center gap-1 rounded-md border px-2 py-1.5 text-[11px] ${mode === 'manual_preferred' && target === endpoint.name ? 'tone-indigo' : 'border-line text-fg-muted'}`}><Route size={12} />优选</button><button type="button" onClick={() => choose(() => onSwitch?.(endpoint.name, 'manual_fixed'))} className={`inline-flex flex-1 items-center justify-center gap-1 rounded-md border px-2 py-1.5 text-[11px] ${mode === 'manual_fixed' && target === endpoint.name ? 'tone-rose' : 'border-line text-fg-muted'}`}><LockKeyhole size={12} />固定</button></div>
          </div>
        ))}
      </div>
    </>
  );

  if (embedded) return <div>{options}</div>;

  return (
    <div ref={ref} className="relative">
      <button type="button" disabled={loading} onClick={() => setOpen((value) => !value)} className="flex h-9 w-[170px] shrink-0 items-center gap-2 rounded-lg border border-line bg-surface px-3 text-sm font-medium text-fg-body shadow-sm hover:border-accent-line hover:text-accent disabled:opacity-50 lg:w-[190px] xl:w-[220px]"><Route size={14} className="shrink-0 text-accent" /><span className="min-w-0 flex-1 truncate text-left">Claude: {resolveClaudeRouteLabel({ routingState })}</span><span className="shrink-0 text-[10px] text-fg-subtle">{mode === 'manual_fixed' ? '固定' : mode === 'manual_preferred' ? '优选' : '自动'}</span></button>
      {open && (
        <div className="absolute left-0 top-full z-50 mt-2 w-[310px] overflow-hidden rounded-xl border border-line bg-surface shadow-xl ring-1 ring-hairline">
          {options}
        </div>
      )}
    </div>
  );
};

export default ClaudeEndpointSwitcher;
