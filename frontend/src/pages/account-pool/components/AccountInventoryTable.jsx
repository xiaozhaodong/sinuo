// ============================================
// 账号资产表格
// 2026-03-22
// 2026-10-07 桌面化：贴边 desk-table、32px 行；单击选中（详情在右侧检查器），
//            右键或行尾「更多」弹出操作菜单；行内只保留勾选与启用开关
// ============================================

import Switch from '@components/desktop/Switch.jsx';
import RowMoreButton from '@components/desktop/RowMoreButton.jsx';
import { useTimezone } from '@contexts/TimezoneContext.jsx';
import { GROUP_BADGE_CLASS, PLAN_BADGE_CLASS, normalizePlanLabel } from './inventoryBadgeStyles.js';

const STATE_DOT_CLASS = {
  emerald: 'bg-success-solid',
  amber: 'bg-warn-solid',
  rose: 'bg-danger-solid',
  slate: 'bg-fg-subtle'
};

const STATE_TEXT_CLASS = {
  emerald: 'text-fg-body',
  amber: 'text-warn',
  rose: 'text-danger',
  slate: 'text-fg-subtle'
};

const TAG = 'ml-1.5 inline-flex items-center rounded border px-1 text-[10.5px] font-medium leading-4';

const quotaBarClass = (percent) => {
  if (percent > 50) return 'bg-success-solid';
  if (percent > 20) return 'bg-warn-solid';
  return 'bg-danger-solid';
};

// 剩余额度：有百分比时画 40px 细条 + 数字；无限额 / 未刷新只给文字，free 账号没有 5h 窗口显示占位
const QuotaMeter = ({ text, percent, title }) => {
  if (!Number.isFinite(percent)) {
    const label = !text || text === '-' || text === '无' ? '—' : text;
    return <span className={`text-xs ${text === '无限额' ? 'text-fg-muted' : 'text-fg-subtle'}`} title={title}>{label}</span>;
  }
  const clamped = Math.max(Math.min(percent, 100), 0);
  return (
    <span className="inline-flex items-center gap-1.5 text-xs tabular-nums text-fg-body" title={title}>
      <span className="h-1 w-10 overflow-hidden rounded-full bg-surface-emph">
        <span className={`block h-full rounded-full ${quotaBarClass(clamped)}`} style={{ width: `${clamped}%` }} />
      </span>
      {Math.round(clamped)}%
    </span>
  );
};

const stopEvent = (event) => {
  event.stopPropagation();
};

const isPinned = (row) => (row.raw?.is_active_selection ?? row.raw?.isActiveSelection) === true;
const isGroupPreferred = (row) => (row.raw?.is_group_preferred ?? row.raw?.isGroupPreferred) === true;

const AccountInventoryTable = ({
  rows = [],
  busyKey = '',
  activeRowId = null,
  selectedRowIds = [],
  allVisibleSelected = false,
  onToggleAllRows,
  onToggleRow,
  onRowClick,
  onRowContextMenu,
  onToggleAccount
}) => {
  const selectedIdSet = new Set(selectedRowIds.map((item) => String(item)));
  const actionBusy = Boolean(busyKey);
  // 最近成功：当天只显示时分秒（完整时间在悬停提示与检查器里）
  const { formatListTimestamp } = useTimezone();

  return (
    <table className="desk-table desk-dense">
      <thead>
        <tr>
          <th className="w-[30px]">
            <input
              type="checkbox"
              checked={allVisibleSelected}
              aria-label="选择全部账号"
              onChange={() => onToggleAllRows?.()}
              className="app-checkbox"
            />
          </th>
          <th>账号</th>
          <th>类型</th>
          <th>组别</th>
          <th>状态</th>
          <th>5h 剩余</th>
          <th>d7 剩余</th>
          <th>最近成功</th>
          <th className="text-center">启用</th>
          <th className="w-[36px]" aria-label="操作" />
        </tr>
      </thead>
      <tbody>
        {rows.map((row) => {
          const rowId = String(row.id);
          const planText = normalizePlanLabel(row.planLabel);
          const enabled = row.enabled !== false;
          const quotaTitle = `最近刷新 ${row.refreshedAtText || '-'}`;
          const lastSuccessAt = row.raw?.last_success_at ?? row.raw?.lastSuccessAt;

          return (
            <tr
              key={rowId}
              data-row-id={rowId}
              aria-selected={activeRowId === rowId}
              onClick={() => onRowClick?.(row)}
              onContextMenu={(event) => onRowContextMenu?.(event, row)}
              className={enabled ? '' : 'text-fg-subtle'}
            >
              <td onClick={stopEvent}>
                <input
                  type="checkbox"
                  checked={selectedIdSet.has(rowId)}
                  aria-label={`选择账号 ${row.name || row.id}`}
                  onChange={() => onToggleRow?.(row.id)}
                  className="app-checkbox"
                />
              </td>
              <td className={`font-medium ${enabled ? 'text-fg' : ''}`}>
                <span className="inline-block max-w-[160px] truncate align-middle" title={row.name}>{row.name || '-'}</span>
                {planText !== '-' && <span className={`${TAG} ${PLAN_BADGE_CLASS[planText] || 'tone-slate'}`}>{planText}</span>}
                {isPinned(row) && <span className={`${TAG} border-danger-line text-danger`} title="全局固定使用此账号">固定</span>}
                {isGroupPreferred(row) && <span className={`${TAG} border-accent-line text-accent`} title="组内首选">首选</span>}
              </td>
              <td className="text-xs text-fg-muted">{row.authLabel || '-'}</td>
              <td>
                <span className={`inline-flex items-center rounded border px-1.5 text-[11px] font-medium leading-4 ${GROUP_BADGE_CLASS[row.groupLabel] || 'tone-slate'}`}>
                  {row.groupLabel || '-'}
                </span>
              </td>
              <td>
                <span className={`inline-flex items-center gap-1.5 text-xs ${STATE_TEXT_CLASS[row.stateTone] || STATE_TEXT_CLASS.slate}`}>
                  <span className={`h-[7px] w-[7px] shrink-0 rounded-full ${STATE_DOT_CLASS[row.stateTone] || STATE_DOT_CLASS.slate}`} />
                  {row.stateLabel || '-'}
                </span>
              </td>
              <td><QuotaMeter text={row.quota5hText} percent={row.quota5hPercent} title={quotaTitle} /></td>
              <td><QuotaMeter text={row.quota7dText} percent={row.quota7dPercent} title={quotaTitle} /></td>
              <td className="text-xs tabular-nums text-fg-muted" title={row.lastSuccessText}>
                {lastSuccessAt ? formatListTimestamp(lastSuccessAt) : '-'}
              </td>
              <td className="text-center" onClick={stopEvent}>
                <span className="inline-flex align-middle">
                  <Switch
                    checked={enabled}
                    label={enabled ? '停用账号' : '启用账号'}
                    disabled={actionBusy}
                    onChange={() => onToggleAccount?.(row.raw || row.detail?.rawAccount || row)}
                  />
                </span>
              </td>
              <td onClick={stopEvent}>
                <RowMoreButton label="更多操作" onOpenMenu={(event) => onRowContextMenu?.(event, row)} />
              </td>
            </tr>
          );
        })}
      </tbody>
    </table>
  );
};

export default AccountInventoryTable;
