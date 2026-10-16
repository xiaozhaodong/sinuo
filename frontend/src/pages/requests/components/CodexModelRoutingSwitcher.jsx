// 请求页 Codex 浮层：模型路由开启时的绑定列表，每个模型一行写当前策略，点开切换它的账号（方案 A）
// 2026-10-07 桌面化：左右两栏改为可展开列表；增删绑定走浮层底部的「模型路由…」
import { useMemo, useState } from 'react';
import { PopoverNote, PopoverSection } from '@components/desktop/PopoverParts.jsx';
import AutoScheduleRow from './route-switcher/AutoScheduleRow.jsx';
import BindingTree from './route-switcher/BindingTree.jsx';
import RouteTargetRow from './route-switcher/RouteTargetRow.jsx';
import { codexModelBindings, isCodexSearchBinding, sortCodexBindings } from '../../codex/routing/codexRoutingView.js';

const MODE_NAMES = { manual_preferred: '优选', manual_fixed: '固定' };

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

const CodexBindingTargets = ({ binding, accountById, onPatch, loading }) => {
  const patch = (change) => onPatch?.(binding, change);
  if (!binding.enabled) return <PopoverNote nested>该模型绑定已停用，请到账号池模型路由面板启用。</PopoverNote>;
  const accountIds = binding.account_ids || [];
  const describe = binding.route_mode === 'manual_fixed'
    ? '只用指定账号，不可用时直接失败'
    : binding.route_mode === 'manual_preferred'
      ? '优先用指定账号，不可用时回落到其余账号'
      : `在 ${accountIds.length} 个账号里按编排规则挑选`;
  return (
    <>
      <AutoScheduleRow indent isCurrent={binding.route_mode === 'auto'} onRestore={() => patch({ route_mode: 'auto', route_target_account_id: 0 })} loading={loading} title={describe} />
      {accountIds.length === 0 && <PopoverNote nested>未选择任何账号</PopoverNote>}
      {accountIds.map((id) => {
        const numericId = Number(id);
        const account = accountById.get(numericId);
        const state = memberState(binding, numericId, account);
        const memberPaused = binding.member_enabled?.[numericId] === false;
        const isTarget = binding.route_mode !== 'auto' && Number(binding.route_target_account_id) === numericId;
        return (
          <RouteTargetRow
            key={numericId}
            indent
            target={{
              name: <>{accountName(account)} <span className="font-mono text-[10.5px] text-fg-subtle">#{numericId}</span></>,
              title: state.detail || state.label,
              value: numericId,
              statusLabel: state.label,
              statusClass: state.tone,
              indicatorClass: state.tone === 'text-success' ? 'bg-success-solid' : state.tone === 'text-warn' ? 'bg-warn-solid' : 'bg-danger-solid'
            }}
            mode={binding.route_mode}
            isCurrent={isTarget}
            loading={loading}
            preferredDisabled={memberPaused}
            preferredTitle={memberPaused ? '该账号已暂停本模型组调度，请先在账号池模型路由中恢复' : '优先使用此账号，不可用时回落'}
            fixedTitle="只使用此账号，不可用时直接失败；即使账号暂停自动调度也可以固定"
            onCommit={(change) => patch({ ...change, route_target_account_id: Number(change.route_target) })}
          />
        );
      })}
    </>
  );
};

const CodexModelRoutingSwitcher = ({ bindings = [], accounts = [], onPatch, loading = false, scopeLabel = '' }) => {
  const [expandedId, setExpandedId] = useState(null);
  const accountById = useMemo(() => new Map(accounts.map((account) => [accountId(account), account])), [accounts]);
  const sorted = useMemo(() => sortCodexBindings([...bindings].sort((a, b) => Number(b.enabled) - Number(a.enabled) || String(a.model).localeCompare(String(b.model)))), [bindings]);
  const modelCount = codexModelBindings(bindings).length;

  const summarizeBinding = (binding) => {
    if (!binding.enabled) return '已停用';
    if (binding.route_mode === 'auto') return '自动';
    return `${MODE_NAMES[binding.route_mode] || ''} → ${accountName(accountById.get(Number(binding.route_target_account_id)))}`;
  };

  return (
    <>
      <PopoverSection title={modelCount > 0 ? `模型路由 · ${modelCount} 个模型绑定` : '模型路由'} aside={scopeLabel} />
      {modelCount === 0 && <PopoverNote>尚无模型绑定，开启后匹配不到模型的请求会返回未分配错误；在下方「模型路由…」里添加</PopoverNote>}
      {sorted.length > 0 && <BindingTree bindings={sorted} expandedId={expandedId} onToggle={setExpandedId} isWebTools={isCodexSearchBinding} webToolsLabel="联网搜索" summaryOf={summarizeBinding} strategyKeyOf={(b) => `${b.enabled}|${b.route_mode}|${b.route_target_account_id || 0}`} renderTargets={(binding) => <CodexBindingTargets binding={binding} accountById={accountById} onPatch={onPatch} loading={loading} />} />}
    </>
  );
};

export default CodexModelRoutingSwitcher;
