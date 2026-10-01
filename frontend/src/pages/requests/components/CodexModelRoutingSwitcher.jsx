import { useMemo, useState } from 'react';
import { AlertCircle } from 'lucide-react';
import BindingList from './route-switcher/BindingList.jsx';
import AutoScheduleRow from './route-switcher/AutoScheduleRow.jsx';
import RouteTargetRow from './route-switcher/RouteTargetRow.jsx';
import ManageFooter from './route-switcher/ManageFooter.jsx';

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
  if (binding?.member_enabled?.[id] === false) return { label: '未参与自动调度', tone: 'text-warn', detail: '该账号不会参与自动或优选调度，但仍可被手动固定使用' };
  return accountState(account);
};

const CodexModelRoutingSwitcher = ({ bindings = [], accounts = [], onPatch, loading = false, onOpenModelRouting }) => {
  const [selectedId, setSelectedId] = useState(null);
  const accountById = useMemo(() => new Map(accounts.map((account) => [accountId(account), account])), [accounts]);
  const sorted = useMemo(() => [...bindings].sort((a, b) => Number(b.enabled) - Number(a.enabled) || String(a.model).localeCompare(String(b.model))), [bindings]);
  const selected = sorted.find((binding) => binding.assignment_id === selectedId) || sorted[0] || null;
  const patch = async (binding, change) => onPatch?.(binding, change);

  if (!sorted.length) {
    return <div><div className="flex items-center gap-2 px-4 py-6 text-sm text-fg-subtle"><AlertCircle className="h-4 w-4" />尚无模型绑定，开启后匹配不到模型的请求会返回未分配错误</div><ManageFooter description="增删绑定、调整账号成员在「Codex 管理 → 账号池」" label="管理绑定" onClick={onOpenModelRouting} /></div>;
  }

  return (
    <div>
      <div className="flex max-h-[380px]">
        <BindingList
          bindings={sorted}
          selectedId={selected?.assignment_id}
          onSelect={setSelectedId}
          displayName={(binding) => binding.model}
          modeOf={(binding) => binding.route_mode}
          getSubtitle={(binding) => {
            if (!binding.enabled) return '已停用';
            if (binding.route_mode !== 'auto') {
              const account = accountById.get(Number(binding.route_target_account_id));
              return `→ ${accountName(account)}`;
            }
            return '自动';
          }}
        />
        <div className="min-w-0 flex-1 max-h-[380px] overflow-y-auto">
          {selected && !selected.enabled && <div className="px-4 py-4 text-xs text-fg-subtle">该模型绑定已停用，请到账号池模型路由面板启用。</div>}
          {selected && selected.enabled && (
            <>
              <div className="px-4 pb-2 pt-3"><h3 className="font-mono text-sm font-semibold text-fg">{selected.model}</h3><p className="mt-0.5 text-[11px] text-fg-muted">{selected.route_mode === 'manual_fixed' ? `只用指定账号，不可用时直接失败` : selected.route_mode === 'manual_preferred' ? `优先用指定账号，不可用时回落到其余账号` : `在 ${(selected.account_ids || []).length} 个账号里按编排规则挑选`}</p></div>
              <div className="px-2 pb-2">
                <AutoScheduleRow isCurrent={selected.route_mode === 'auto'} onRestore={() => patch(selected, { route_mode: 'auto', route_target_account_id: 0 })} loading={loading} />
                {(selected.account_ids || []).length === 0 && <div className="px-2 py-3 text-xs text-fg-subtle">未选择任何账号</div>}
                {(selected.account_ids || []).map((id) => {
                  const numericId = Number(id);
                  const account = accountById.get(numericId);
                  const state = memberState(selected, numericId, account);
                  const memberPaused = selected.member_enabled?.[numericId] === false;
                  const isTarget = selected.route_mode !== 'auto' && Number(selected.route_target_account_id) === numericId;
                  return (
                    <RouteTargetRow
                      key={numericId}
                      target={{
                        name: <>{accountName(account)} <span className="font-mono text-[10px] text-fg-subtle">#{numericId}</span></>,
                        title: state.detail || state.label,
                        value: numericId,
                        statusLabel: state.label,
                        statusClass: state.tone,
                        indicatorClass: state.tone === 'text-success' ? 'bg-success-solid' : state.tone === 'text-warn' ? 'bg-warn-solid' : 'bg-danger-solid'
                      }}
                      mode={selected.route_mode}
                      isCurrent={isTarget}
                      loading={loading}
                      preferredDisabled={memberPaused}
                      preferredTitle={memberPaused ? '该账号已暂停本模型组调度，请先在账号池模型路由中恢复' : '优先使用此账号，不可用时回落'}
                      fixedTitle="只使用此账号，不可用时直接失败；即使账号暂停自动调度也可以固定"
                      onCommit={(change) => patch(selected, { ...change, route_target_account_id: Number(change.route_target) })}
                    />
                  );
                })}
              </div>
            </>
          )}
        </div>
      </div>
      <ManageFooter description="增删绑定、调整账号成员在「Codex 管理 → 账号池」" label="管理绑定" onClick={onOpenModelRouting} />
    </div>
  );
};

export default CodexModelRoutingSwitcher;
