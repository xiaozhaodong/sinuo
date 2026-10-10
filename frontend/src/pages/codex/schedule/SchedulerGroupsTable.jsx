import { Fragment } from 'react';
import { ExternalLink } from 'lucide-react';
import { useTimezone } from '@contexts/TimezoneContext.jsx';
import { QuotaMeter, STATE_DOT_CLASS, STATE_TEXT_CLASS } from '@pages/account-pool/components/inventoryCells.jsx';
import { PLAN_BADGE_CLASS, normalizePlanLabel } from '@pages/account-pool/components/inventoryBadgeStyles.js';
import { buildGroupHeader, isLatestHit, buildAccountActionState } from './schedulerView.js';

const GROUP_STYLE = { primary: 'tone-indigo', backup: 'tone-cyan', cold: 'tone-violet' };
const TAG = 'ml-1.5 inline-flex items-center rounded border px-1 text-[10.5px] font-medium leading-4';
const ACTION_BUTTON = 'inline-flex h-5 items-center rounded-[5px] border border-line px-1.5 text-[11px] disabled:opacity-40';

const GroupHeader = ({ group, busyKey, swapGroup, onViewInInventory }) => {
  const { total, available } = buildGroupHeader(group);
  const groupActionBusy = busyKey.startsWith(`group-swap-${group.key}-`);
  return (
    <tr className="desk-group">
      <td colSpan={6}>
        <div className="flex items-center gap-2">
          <span className={`${GROUP_STYLE[group.key]} rounded border px-1.5 text-[11px] leading-[18px]`}>{group.label}</span>
          <span className="font-normal text-fg-muted">{total} 个账号 · {available} 可用</span>
          <span className="flex-1" />
          {group.actions.filter(action => action.key === 'swap-up' || action.key === 'swap-down').map(action => (
            <button key={action.key} type="button" title="与相邻组整组交换，不是单账号加入"
              disabled={Boolean(action.disabled) || groupActionBusy} onClick={() => swapGroup(group, action)}
              className="inline-flex h-6 items-center rounded-md border border-line bg-surface px-2 text-xs font-normal text-fg-body hover:bg-surface-sub disabled:opacity-40">
              {action.label}
            </button>
          ))}
          <button type="button" title="在账号池中查看本组" aria-label="在账号池中查看本组" onClick={() => onViewInInventory(group.key)}
            className="inline-flex h-6 w-6 items-center justify-center rounded text-fg-muted hover:bg-surface-mut hover:text-fg">
            <ExternalLink size={12} />
          </button>
        </div>
      </td>
    </tr>
  );
};

const AccountName = ({ account, summary }) => {
  const planText = normalizePlanLabel(account.planLabel);
  return (
    <>
      <span className={`font-medium ${account.enabled !== false ? 'text-fg' : ''}`}>{account.name || '-'}</span>
      {planText !== '-' && <span className={`${TAG} ${PLAN_BADGE_CLASS[planText] || 'tone-slate'}`}>{planText}</span>}
      {account.isActive && <span className={`${TAG} border-danger-line text-danger`}>固定</span>}
      {account.isGroupPreferred && <span className={`${TAG} border-accent-line text-accent`}>首选</span>}
      {isLatestHit(account, summary) && <span className={`${TAG} tone-indigo`}>最近命中</span>}
    </>
  );
};

const AccountActions = ({ account, group, actions }) => {
  const state = buildAccountActionState(account, group, actions.busyKey);
  return (
    <span className="inline-flex gap-1 opacity-0 transition-opacity group-hover:opacity-100 group-focus-within:opacity-100">
      <button type="button" aria-label="设为本组首选" title={state.preferredTitle} disabled={state.preferredDisabled}
        onClick={() => actions.setActiveAccount(account.rawAccount, group)}
        className={`${ACTION_BUTTON} ${account.isGroupPreferred ? 'border-accent-line bg-accent-soft text-accent' : 'text-fg-body hover:bg-surface-mut'}`}>首选</button>
      <button type="button" aria-label="设为全局固定" title={state.pinTitle} disabled={state.pinDisabled}
        onClick={() => actions.pinAccount(account.rawAccount)}
        className={`${ACTION_BUTTON} ${account.isActive ? 'border-danger-line bg-danger-soft text-danger' : 'text-fg-body hover:bg-surface-mut'}`}>固定</button>
    </span>
  );
};

const AccountRow = ({ account, group, summary, actions, onRowContextMenu }) => {
  const { formatListTimestamp } = useTimezone();
  return (
    <tr className={`group ${account.enabled === false ? 'text-fg-subtle' : ''}`} onContextMenu={event => onRowContextMenu(event, account, group)}>
      <td style={{ paddingLeft: 28 }}><AccountName account={account} summary={summary} /></td>
      <td>
        <span className={`inline-flex items-center gap-1.5 text-xs ${STATE_TEXT_CLASS[account.stateTone] || STATE_TEXT_CLASS.slate}`}>
          <span className={`h-[7px] w-[7px] shrink-0 rounded-full ${STATE_DOT_CLASS[account.stateTone] || STATE_DOT_CLASS.slate}`} />
          {account.stateLabel || '-'}
        </span>
      </td>
      <td><QuotaMeter text={account.quota5hText} percent={account.quota5hPercent} /></td>
      <td><QuotaMeter text={account.quota7dText} percent={account.quota7dPercent} /></td>
      <td className="text-xs tabular-nums text-fg-muted">{account.lastSuccessAt ? formatListTimestamp(account.lastSuccessAt) : '-'}</td>
      <td className="text-right"><AccountActions account={account} group={group} actions={actions} /></td>
    </tr>
  );
};

const SchedulerGroupsTable = ({ groups = [], summary, actions, onViewInInventory, onRowContextMenu }) => (
  <table className="desk-table desk-dense">
    <thead><tr>
      <th style={{ paddingLeft: 28 }}>账号</th><th>状态</th><th>5h 剩余</th><th>d7 剩余</th><th>最近成功</th>
      <th className="w-[120px] text-right">组内调度</th>
    </tr></thead>
    <tbody>
      {groups.map(group => (
        <Fragment key={group.key}>
          <GroupHeader group={group} busyKey={actions.busyKey} swapGroup={actions.swapGroup} onViewInInventory={onViewInInventory} />
          {group.accounts.length === 0
            ? <tr><td colSpan={6} className="text-xs text-fg-subtle">暂无账号</td></tr>
            : group.accounts.map(account => <AccountRow key={String(account.id)} account={account} group={group} summary={summary} actions={actions} onRowContextMenu={onRowContextMenu} />)}
        </Fragment>
      ))}
    </tbody>
  </table>
);

export default SchedulerGroupsTable;
