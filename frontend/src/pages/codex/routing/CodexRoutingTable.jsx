import { CustomSelect } from '@components/ui';
import Switch from '@components/desktop/Switch.jsx';
import { MODES, accountName } from '../../account-pool/components/CodexBindingEditor.jsx';
import { codexMemberState, codexOutcomeLabel } from './codexRoutingView.js';
import { routingOutcomeTone } from '@components/model-routing/routingView.js';
import { MemberChipsCell, RecentHitCell } from '@components/model-routing/RoutingTableCells.jsx';

const AccountChips = ({ binding, accountById }) => (
  <MemberChipsCell
    members={binding.account_ids.map((id, index) => {
        const account = accountById.get(Number(id));
        const state = codexMemberState(binding, id, account);
        const target = binding.route_mode !== 'auto' && Number(id) === Number(binding.route_target_account_id);
        const name = account ? accountName(account) : `账号 ${id}`;
        return {
          key: id,
          label: `${index + 1}. ${name}`,
          tone: state.tone,
          title: `${name} (#${id}) · ${state.detail || state.label}`,
          className: target ? 'border-accent-line bg-accent-soft font-semibold text-accent-fg' : ''
        };
      })}
    emptyText="未选择任何账号"
  />
);

const RoutingRow = ({ binding, accountById, selectedId, busy, onSelect, onPatch, onRowContextMenu }) => (
  <tr data-row-id={binding.assignment_id} aria-selected={selectedId === binding.assignment_id}
    className={binding.enabled ? undefined : 'text-fg-subtle'} onClick={() => onSelect(binding.assignment_id)}
    onContextMenu={(event) => onRowContextMenu(event, binding)}>
    <td className="max-w-[200px]">
      <div className="flex min-w-0 items-center gap-1.5">
        <span className={`min-w-0 truncate font-mono text-[12.5px] ${binding.note ? 'max-w-[70%] shrink-0' : 'flex-1'}`} title={binding.model}>{binding.model}</span>
        {binding.note && <span className="min-w-0 flex-1 truncate text-[11px] text-fg-muted" title={binding.note}>{binding.note}</span>}
      </div>
    </td>
    <AccountChips binding={binding} accountById={accountById} />
    <td onClick={(event) => event.stopPropagation()} onKeyDown={(event) => event.stopPropagation()}>
      <CustomSelect options={MODES} value={binding.route_mode} disabled={busy} size="xs" className="w-[80px]"
        onChange={(route_mode) => onPatch(binding, { route_mode })} />
    </td>
    <td onClick={(event) => event.stopPropagation()} onKeyDown={(event) => event.stopPropagation()}>
      <CustomSelect options={binding.account_ids.map((id) => ({ value: Number(id), label: `${accountById.has(Number(id)) ? accountName(accountById.get(Number(id))) : `账号 ${id}`} (#${id})` }))}
        value={Number(binding.route_target_account_id) || ''} disabled={busy || binding.route_mode === 'auto'} size="sm" className="w-[130px]"
        placeholder={binding.route_mode === 'auto' ? '自动挑选' : '选择目标账号'}
        onChange={(target) => onPatch(binding, { route_target_account_id: Number(target) })} />
    </td>
    <RecentHitCell
      name={binding.last_account_id ? (accountById.has(Number(binding.last_account_id)) ? accountName(accountById.get(Number(binding.last_account_id))) : `账号 ${binding.last_account_id}`) : ''}
      at={binding.last_decision_at}
      outcomeLabel={binding.last_outcome ? codexOutcomeLabel(binding.last_outcome) : undefined}
      outcomeTone={routingOutcomeTone(binding.last_outcome)}
    />
    <td onClick={(event) => event.stopPropagation()} onKeyDown={(event) => event.stopPropagation()}>
      <Switch checked={binding.enabled} disabled={busy} label={`启用映射 ${binding.model}`} onChange={(enabled) => onPatch(binding, { enabled })} />
    </td>
  </tr>
);

const CodexRoutingTable = (props) => {
  return (
    <table className="desk-table min-w-[700px]">
      <thead><tr><th>模型</th><th>账号</th><th className="w-[96px]">策略</th><th className="w-[146px]">目标</th><th className="@max-[900px]:hidden">最近命中</th><th className="w-14">启用</th></tr></thead>
      <tbody>{props.bindings.map((binding) => <RoutingRow key={binding.assignment_id} {...props} binding={binding} />)}</tbody>
    </table>
  );
};

export default CodexRoutingTable;
