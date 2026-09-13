import { useEffect, useMemo, useRef, useState } from 'react';
import { AlertCircle, Check, LockKeyhole, RotateCcw, Route, UserRound } from 'lucide-react';

const modeLabel = (binding) => {
  if (!binding.enabled) return { text: '停用', cls: 'text-fg-subtle' };
  if (binding.route_mode === 'manual_fixed') return { text: '固定', cls: 'text-rose-500' };
  if (binding.route_mode === 'manual_preferred') return { text: '优选', cls: 'text-indigo-500' };
  return { text: '自动', cls: 'text-fg-subtle' };
};

const accountName = (account) => account?.account_name || account?.accountName || `账号 ${account?.id ?? account?.ID ?? ''}`;
const accountId = (account) => Number(account?.id ?? account?.ID ?? account?.account_id ?? account?.accountId);

const accountState = (account) => {
  if (!account) return { label: '账号不存在', tone: 'text-danger' };
  if (account.enabled === false || account.state === 'disabled_auth') return { label: '已停用', tone: 'text-danger' };
  if (!account.has_credential && !account.credential_raw && !account.credentialRaw) return { label: '无凭据', tone: 'text-warn' };
  const cooldownUntil = account.cooldown_until || account.cooldownUntil;
  if (cooldownUntil && new Date(cooldownUntil).getTime() > Date.now()) return { label: '冷却中', tone: 'text-warn' };
  return { label: '可参与', tone: 'text-success' };
};

const memberState = (binding, id, account) => {
  if (binding?.member_enabled?.[id] === false) {
    return { label: '未参与自动调度', tone: 'text-warn', detail: '该账号不会参与自动或优选调度，但仍可被手动固定使用' };
  }
  return accountState(account);
};

const CodexModelRoutingSwitcher = ({ bindings = [], accounts = [], onPatch, loading = false, embedded = false }) => {
  const [open, setOpen] = useState(false);
  const [selectedId, setSelectedId] = useState(null);
  const ref = useRef(null);
  const accountById = useMemo(() => new Map(accounts.map((account) => [accountId(account), account])), [accounts]);
  const sorted = useMemo(
    () => [...bindings].sort((a, b) => Number(b.enabled) - Number(a.enabled) || String(a.model).localeCompare(String(b.model))),
    [bindings]
  );
  const selected = sorted.find((binding) => binding.assignment_id === selectedId) || sorted[0] || null;
  const manualCount = sorted.filter((binding) => binding.enabled && binding.route_mode !== 'auto').length;

  useEffect(() => {
    const close = (event) => { if (ref.current && !ref.current.contains(event.target)) setOpen(false); };
    document.addEventListener('mousedown', close);
    return () => document.removeEventListener('mousedown', close);
  }, []);

  if (!bindings.length) {
    return (
      <div className="flex items-center gap-2 rounded-lg border border-line bg-surface-sub px-3 py-1.5 text-sm text-fg-subtle" title="请到账号池模型路由面板添加模型绑定">
        <AlertCircle className="h-3.5 w-3.5" />
        <span>Codex: 尚无模型绑定</span>
      </div>
    );
  }

  const patch = async (binding, change) => {
    await onPatch?.(binding, change);
  };

  return (
    <div ref={ref} className="relative">
      {!embedded && <button
        type="button"
        disabled={loading}
        onClick={() => setOpen((value) => !value)}
        title="Codex 模型路由启用中，按请求模型选择账号；点击可调整每个模型的策略"
        className="flex w-[220px] items-center gap-2 rounded-lg border border-line bg-surface px-3 py-1.5 text-sm font-medium text-fg-body shadow-sm hover:border-accent-line hover:text-accent disabled:opacity-50"
      >
        <UserRound className="h-3.5 w-3.5 shrink-0 text-accent" />
        <span className="min-w-0 flex-1 truncate text-left">Codex: 模型路由 · {sorted.length} 模型</span>
        {manualCount > 0 && <span className="shrink-0 text-[10px] text-fg-subtle">{manualCount} 手动</span>}
      </button>}

      {(embedded || open) && (
        <div className={embedded ? 'overflow-x-auto' : 'absolute left-0 top-full z-50 mt-2 w-[540px] overflow-hidden rounded-xl border border-line bg-surface shadow-xl ring-1 ring-hairline'}>
          <div className={`flex max-h-[380px] ${embedded ? 'min-w-[520px]' : ''}`}>
            <div className="w-[220px] shrink-0 overflow-y-auto border-r border-line-soft py-1">
              {sorted.map((binding) => {
                const badge = modeLabel(binding);
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
                        {binding.enabled ? (binding.route_mode === 'manual_fixed' ? `固定 · #${binding.route_target_account_id}` : binding.route_mode === 'manual_preferred' ? `优选 · #${binding.route_target_account_id}` : '自动调度') : '已停用'}
                      </div>
                    </div>
                    <span className={`shrink-0 text-[10px] ${badge.cls}`}>{badge.text}</span>
                  </button>
                );
              })}
            </div>

            <div className="min-w-0 flex-1 overflow-y-auto py-1">
              {selected && !selected.enabled && <div className="px-3 py-4 text-xs text-fg-subtle">该模型绑定已停用，请到账号池模型路由面板启用。</div>}
              {selected && selected.enabled && (
                <>
                  <button
                    type="button"
                    disabled={loading}
                    onClick={() => patch(selected, { route_mode: 'auto', route_target_account_id: 0 })}
                    className={`mx-1 flex w-[calc(100%-8px)] items-center gap-2 rounded-lg px-2 py-2 text-left text-xs disabled:opacity-50 ${selected.route_mode === 'auto' ? 'bg-accent-soft text-accent-fg' : 'text-fg-body hover:bg-surface-sub'}`}
                  >
                    <RotateCcw size={13} className="shrink-0" />
                    <span className="flex-1">自动调度</span>
                    {selected.route_mode === 'auto' && <Check size={13} />}
                  </button>
                  <div className="mx-3 my-1 border-t border-line-soft" />
                  {(selected.account_ids || []).length === 0 && <div className="px-3 py-3 text-xs text-fg-subtle">未选择任何账号</div>}
                  {(selected.account_ids || []).map((id) => {
                    const account = accountById.get(Number(id));
                    const state = memberState(selected, Number(id), account);
                    const memberPaused = selected.member_enabled?.[Number(id)] === false;
                    const preferred = selected.route_mode === 'manual_preferred' && Number(selected.route_target_account_id) === Number(id);
                    const fixed = selected.route_mode === 'manual_fixed' && Number(selected.route_target_account_id) === Number(id);
                    return (
                      <div key={id} className={`mx-1 flex items-center gap-2 rounded-lg px-2 py-1.5 ${preferred || fixed ? 'bg-surface-sub' : 'hover:bg-surface-sub'}`}>
                        <span className={`h-2 w-2 shrink-0 rounded-full ${state.tone === 'text-success' ? 'bg-success-solid' : state.tone === 'text-warn' ? 'bg-warn-solid' : 'bg-danger-solid'}`} />
                        <span className="min-w-0 flex-1 truncate text-xs text-fg-body" title={state.detail || state.label}>{accountName(account)} <span className="font-mono text-[10px] text-fg-subtle">#{id}</span></span>
                        <span className={`shrink-0 text-[10px] ${state.tone}`} title={state.detail}>{state.label}</span>
                        <div className="flex shrink-0 gap-1">
                          <button type="button" disabled={loading || memberPaused} title={memberPaused ? '该账号已暂停本模型组调度，请先在账号池模型路由中恢复' : '优先使用此账号，不可用时回落'} onClick={() => patch(selected, { route_mode: 'manual_preferred', route_target_account_id: Number(id) })} className={`inline-flex h-6 items-center gap-1 rounded-md border px-1.5 text-[11px] disabled:opacity-50 ${preferred ? 'tone-indigo' : 'border-line text-fg-muted hover:border-accent-line hover:text-accent'}`}><Route size={11} />优选</button>
                          <button type="button" disabled={loading} title="只使用此账号，不可用时直接失败；即使账号暂停自动调度也可以固定" onClick={() => patch(selected, { route_mode: 'manual_fixed', route_target_account_id: Number(id) })} className={`inline-flex h-6 items-center gap-1 rounded-md border px-1.5 text-[11px] disabled:opacity-50 ${fixed ? 'tone-rose' : 'border-line text-fg-muted hover:border-rose-300 hover:text-rose-500'}`}><LockKeyhole size={11} />固定</button>
                        </div>
                      </div>
                    );
                  })}
                </>
              )}
            </div>
          </div>
          <div className="border-t border-line-soft px-4 py-2 text-[11px] text-fg-subtle">新增、删除模型绑定或调整账号成员，请前往「账号池 → Codex 模型路由」</div>
        </div>
      )}
    </div>
  );
};

export default CodexModelRoutingSwitcher;
