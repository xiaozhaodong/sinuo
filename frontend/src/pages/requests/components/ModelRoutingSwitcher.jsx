// ============================================
// 请求追踪：Claude 模型路由快捷切换器（左模型列表 / 右端点策略）
// 2026-09-04
// ============================================

import { useEffect, useMemo, useRef, useState } from 'react';
import { AlertCircle, Check, LockKeyhole, RotateCcw, Route } from 'lucide-react';
import { ENDPOINT_STATUS_LABELS, routeModeLabel } from '../../endpoints/utils/modelRouting.js';

const dotTone = (endpoint) => {
  if (endpoint.status === 'ok') return 'bg-success-solid';
  if (['cooldown', 'auto_schedule_disabled'].includes(endpoint.status)) return 'bg-warn';
  return 'bg-danger-solid';
};

const modeBadge = (binding) => {
  if (!binding.enabled) return { text: '停用', cls: 'text-fg-subtle' };
  if (binding.route_mode === 'manual_fixed') return { text: '固定', cls: 'text-rose-500' };
  if (binding.route_mode === 'manual_preferred') return { text: '优选', cls: 'text-indigo-500' };
  return { text: '自动', cls: 'text-fg-subtle' };
};

const ModelRoutingSwitcher = ({ bindings = [], onPatch, loading = false }) => {
  const [open, setOpen] = useState(false);
  const [selectedId, setSelectedId] = useState(null);
  const ref = useRef(null);

  const sorted = useMemo(
    () => [...bindings].sort((a, b) => Number(b.enabled) - Number(a.enabled) || a.model.localeCompare(b.model)),
    [bindings]
  );
  const manualCount = sorted.filter((b) => b.enabled && b.route_mode !== 'auto').length;
  const selected = sorted.find((b) => b.assignment_id === selectedId) || sorted[0] || null;

  useEffect(() => {
    const close = (event) => { if (ref.current && !ref.current.contains(event.target)) setOpen(false); };
    document.addEventListener('mousedown', close);
    return () => document.removeEventListener('mousedown', close);
  }, []);

  const patch = async (binding, change) => {
    await onPatch?.(binding, change);
  };

  return (
    <div ref={ref} className="relative">
      <button
        type="button"
        disabled={loading}
        onClick={() => setOpen((v) => !v)}
        title="模型路由启用中，按请求模型选择上游端点；点击可调整每个模型的策略"
        className="flex w-[220px] items-center gap-2 rounded-lg border border-line bg-surface px-3 py-1.5 text-sm font-medium text-fg-body shadow-sm hover:border-accent-line hover:text-accent disabled:opacity-50"
      >
        <Route className="h-3.5 w-3.5 shrink-0 text-accent" />
        <span className="min-w-0 flex-1 truncate text-left">Claude: 模型路由 · {sorted.length} 模型</span>
        {manualCount > 0 && <span className="shrink-0 text-[10px] text-fg-subtle">{manualCount} 手动</span>}
      </button>

      {open && (
        <div className="absolute left-0 top-full z-50 mt-2 w-[540px] overflow-hidden rounded-xl border border-line bg-surface shadow-xl ring-1 ring-hairline">
          {sorted.length === 0 ? (
            <div className="flex items-center gap-2 px-4 py-6 text-sm text-fg-subtle">
              <AlertCircle className="h-4 w-4" />尚无模型映射，所有 Claude 请求都会返回未分配错误
            </div>
          ) : (
            <div className="flex max-h-[380px]">
              {/* 左栏：模型列表 */}
              <div className="w-[220px] shrink-0 overflow-y-auto border-r border-line-soft py-1">
                {sorted.map((binding) => {
                  const badge = modeBadge(binding);
                  const active = selected?.assignment_id === binding.assignment_id;
                  return (
                    <button
                      key={binding.assignment_id}
                      type="button"
                      onClick={() => setSelectedId(binding.assignment_id)}
                      className={`flex w-full items-center gap-2 px-3 py-2 text-left ${active ? 'bg-accent-soft' : 'hover:bg-surface-sub'}`}
                    >
                      <div className="min-w-0 flex-1">
                        <div className={`truncate font-mono text-xs ${binding.enabled ? 'text-fg-body' : 'text-fg-subtle line-through'}`} title={binding.model}>{binding.model}</div>
                        <div className="truncate text-[10px] text-fg-subtle">
                          {binding.enabled ? routeModeLabel(binding.route_mode, binding.route_target) : '已停用'}
                          {binding.enabled && binding.last_effective_endpoint ? ` · 最近 ${binding.last_effective_endpoint}` : ''}
                        </div>
                      </div>
                      <span className={`shrink-0 text-[10px] ${badge.cls}`}>{badge.text}</span>
                    </button>
                  );
                })}
              </div>

              {/* 右栏：所选模型的端点与策略 */}
              <div className="min-w-0 flex-1 overflow-y-auto py-1">
                {selected && !selected.enabled && (
                  <div className="px-3 py-4 text-xs text-fg-subtle">该映射已停用，请到端点页启用后再切换策略</div>
                )}
                {selected && selected.enabled && (
                  <>
                    <button
                      type="button"
                      disabled={loading}
                      onClick={() => patch(selected, { route_mode: 'auto' })}
                      className={`mx-1 flex w-[calc(100%-8px)] items-center gap-2 rounded-lg px-2 py-2 text-left text-xs disabled:opacity-50 ${selected.route_mode === 'auto' ? 'bg-accent-soft text-accent-fg' : 'text-fg-body hover:bg-surface-sub'}`}
                    >
                      <RotateCcw size={13} className="shrink-0" />
                      <span className="flex-1">自动调度</span>
                      {selected.route_mode === 'auto' && <Check size={13} />}
                    </button>
                    <div className="mx-3 my-1 border-t border-line-soft" />
                    {selected.endpoints.length === 0 && <div className="px-3 py-3 text-xs text-fg-subtle">未选择任何端点</div>}
                    {selected.endpoints.map((endpoint) => {
                      const preferred = selected.route_mode === 'manual_preferred' && selected.route_target === endpoint.name;
                      const fixed = selected.route_mode === 'manual_fixed' && selected.route_target === endpoint.name;
                      return (
                        <div key={endpoint.name} className={`mx-1 flex items-center gap-2 rounded-lg px-2 py-1.5 ${preferred || fixed ? 'bg-surface-sub' : 'hover:bg-surface-sub'}`}>
                          <span className={`h-2 w-2 shrink-0 rounded-full ${dotTone(endpoint)}`} />
                          <span className="min-w-0 flex-1 truncate text-xs text-fg-body" title={endpoint.detail || endpoint.name}>{endpoint.name}</span>
                          {endpoint.status !== 'ok'
                            ? <span className="shrink-0 text-[10px] text-warn">{ENDPOINT_STATUS_LABELS[endpoint.status] || endpoint.status}</span>
                            : <span className="shrink-0 font-mono text-[10px] text-fg-subtle">P{endpoint.priority}</span>}
                          <div className="flex shrink-0 gap-1">
                            <button type="button" disabled={loading} title="优先用此端点，不可用时回落" onClick={() => patch(selected, { route_mode: 'manual_preferred', route_target: endpoint.name })} className={`inline-flex h-6 items-center gap-1 rounded-md border px-1.5 text-[11px] disabled:opacity-50 ${preferred ? 'tone-indigo' : 'border-line text-fg-muted hover:border-accent-line hover:text-accent'}`}><Route size={11} />优选</button>
                            <button type="button" disabled={loading} title="只用此端点，不可用时直接失败" onClick={() => patch(selected, { route_mode: 'manual_fixed', route_target: endpoint.name })} className={`inline-flex h-6 items-center gap-1 rounded-md border px-1.5 text-[11px] disabled:opacity-50 ${fixed ? 'tone-rose' : 'border-line text-fg-muted hover:border-rose-300 hover:text-rose-500'}`}><LockKeyhole size={11} />固定</button>
                          </div>
                        </div>
                      );
                    })}
                  </>
                )}
              </div>
            </div>
          )}
          <div className="border-t border-line-soft px-4 py-2 text-[11px] text-fg-subtle">新增、删除映射或调整端点集合，请前往「端点管理 → 模型路由」</div>
        </div>
      )}
    </div>
  );
};

export default ModelRoutingSwitcher;
