import { CustomSelect } from '@components/ui';
import Switch from '@components/desktop/Switch.jsx';
import { useTimezone } from '@contexts/TimezoneContext.jsx';
import { MODES, accountName } from '../../account-pool/components/CodexBindingEditor.jsx';
import { codexMemberState, codexOutcomeLabel } from './codexRoutingView.js';

const AccountChips = ({ binding, accountById }) => (
  <td className="max-w-[260px]">
    <div className="flex flex-wrap gap-1 py-1.5">
      {binding.account_ids.map((id, index) => {
        const account = accountById.get(Number(id));
        const state = codexMemberState(binding, id, account);
        const target = binding.route_mode !== 'auto' && Number(id) === Number(binding.route_target_account_id);
        return (
          <span key={id} title={`${account ? accountName(account) : `账号 ${id}`} (#${id}) · ${state.detail || state.label}`}
            className={`max-w-[180px] truncate rounded border px-1.5 text-[11px] leading-[18px] ${target ? 'border-accent-line bg-accent-soft font-semibold text-accent-fg' : state.tone}`}>
            {index + 1}. {account ? accountName(account) : `账号 ${id}`}{target ? ' · 目标' : ''}
          </span>
        );
      })}
      {binding.account_ids.length === 0 && <span className="text-danger">未选择任何账号</span>}
    </div>
  </td>
);

const RoutingRow = ({ binding, accountById, selectedId, busy, onSelect, onPatch, onRowContextMenu, formatTimestamp }) => (
  <tr data-row-id={binding.assignment_id} aria-selected={selectedId === binding.assignment_id}
    className={binding.enabled ? undefined : 'text-fg-subtle'} onClick={() => onSelect(binding.assignment_id)}
    onContextMenu={(event) => onRowContextMenu(event, binding)}>
    <td className="max-w-[240px]">
      <div className="truncate font-mono text-[12.5px]" title={binding.model}>{binding.model}</div>
      {binding.note && <div className="truncate text-[11px] text-fg-muted" title={binding.note}>{binding.note}</div>}
    </td>
    <AccountChips binding={binding} accountById={accountById} />
    <td onClick={(event) => event.stopPropagation()} onKeyDown={(event) => event.stopPropagation()}>
      <CustomSelect options={MODES} value={binding.route_mode} disabled={busy} size="sm" className="w-[100px]"
        onChange={(route_mode) => onPatch(binding, { route_mode })} />
    </td>
    <td onClick={(event) => event.stopPropagation()} onKeyDown={(event) => event.stopPropagation()}>
      <CustomSelect options={binding.account_ids.map((id) => ({ value: Number(id), label: `${accountById.has(Number(id)) ? accountName(accountById.get(Number(id))) : `账号 ${id}`} (#${id})` }))}
        value={Number(binding.route_target_account_id) || ''} disabled={busy || binding.route_mode === 'auto'} size="sm" className="w-[150px]"
        placeholder={binding.route_mode === 'auto' ? '自动挑选' : '选择目标账号'}
        onChange={(target) => onPatch(binding, { route_target_account_id: Number(target) })} />
    </td>
    <td className="max-w-[160px] text-[11.5px] text-fg-muted">
      <div className="truncate">{binding.last_account_id ? (accountById.has(Number(binding.last_account_id)) ? accountName(accountById.get(Number(binding.last_account_id))) : `账号 ${binding.last_account_id}`) : '—'}</div>
      {binding.last_decision_at && <div className="truncate" title={formatTimestamp(binding.last_decision_at)}>{formatTimestamp(binding.last_decision_at)}</div>}
      {binding.last_outcome && <div className="truncate">{codexOutcomeLabel(binding.last_outcome)}</div>}
    </td>
    <td onClick={(event) => event.stopPropagation()} onKeyDown={(event) => event.stopPropagation()}>
      <Switch checked={binding.enabled} disabled={busy} label={`启用映射 ${binding.model}`} onChange={(enabled) => onPatch(binding, { enabled })} />
    </td>
  </tr>
);

const CodexRoutingTable = (props) => {
  const { formatTimestamp } = useTimezone();
  return (
    <table className="desk-table min-w-[820px]">
      <thead><tr><th>模型</th><th>账号</th><th className="w-[116px]">策略</th><th className="w-[166px]">目标</th><th>最近命中</th><th className="w-14">启用</th></tr></thead>
      <tbody>{props.bindings.map((binding) => <RoutingRow key={binding.assignment_id} {...props} binding={binding} formatTimestamp={formatTimestamp} />)}</tbody>
    </table>
  );
};

export default CodexRoutingTable;
