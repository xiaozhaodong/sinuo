// ============================================
// 账号资产表格
// 2026-03-22
// ============================================

import { ArrowRight, Pencil, RefreshCw, TestTube2, Trash2 } from 'lucide-react';
import Badge from './Badge.jsx';
import {
  AUTH_BADGE_CLASS,
  GROUP_BADGE_CLASS,
  PLAN_BADGE_CLASS,
  normalizePlanLabel,
  toBadgeToneClass
} from './inventoryBadgeStyles.js';

const quotaColor = (text, percent) => {
  if (!text || text === '-' || text === '无' || text === '未刷新') return 'text-fg-subtle';
  if (text === '无限额') return 'text-fg-muted';
  if (!Number.isFinite(percent)) return 'text-fg-muted';
  if (percent > 50) return 'text-success';
  if (percent > 20) return 'text-warn font-semibold';
  return 'text-danger font-semibold';
};

const quotaStrokeColor = (text, percent) => {
  if (!text || text === '-' || text === '无' || text === '未刷新') return '#cbd5e1';
  if (text === '无限额') return '#94a3b8';
  if (!Number.isFinite(percent)) return '#94a3b8';
  if (percent > 50) return '#34d399';
  if (percent > 20) return '#fbbf24';
  return '#f87171';
};

const RING_SIZE = 22;
const RING_STROKE = 2.5;
const RING_RADIUS = (RING_SIZE - RING_STROKE) / 2;
const RING_CIRCUMFERENCE = 2 * Math.PI * RING_RADIUS;

const QuotaRing = ({ label, text, percent }) => {
  const hasRing = Number.isFinite(percent);
  const fillPercent = hasRing ? Math.max(Math.min(percent, 100), 0) : 0;
  const dashOffset = RING_CIRCUMFERENCE - (fillPercent / 100) * RING_CIRCUMFERENCE;
  const stroke = quotaStrokeColor(text, percent);
  const isInfinite = text === '无限额';
  const isEmpty = !text || text === '-' || text === '无' || text === '未刷新';
  const tooltip = `${label}: ${text || '-'}`;

  return (
    <div className="relative" style={{ width: RING_SIZE, height: RING_SIZE }} title={tooltip}>
      <svg width={RING_SIZE} height={RING_SIZE} className="-rotate-90">
        <circle
          cx={RING_SIZE / 2}
          cy={RING_SIZE / 2}
          r={RING_RADIUS}
          fill="none"
          stroke="#f1f5f9"
          strokeWidth={RING_STROKE}
        />
        {hasRing && (
          <circle
            cx={RING_SIZE / 2}
            cy={RING_SIZE / 2}
            r={RING_RADIUS}
            fill="none"
            stroke={stroke}
            strokeWidth={RING_STROKE}
            strokeDasharray={RING_CIRCUMFERENCE}
            strokeDashoffset={dashOffset}
            strokeLinecap="round"
          />
        )}
        {!hasRing && !isEmpty && (
          <circle
            cx={RING_SIZE / 2}
            cy={RING_SIZE / 2}
            r={RING_RADIUS}
            fill="none"
            stroke={stroke}
            strokeWidth={RING_STROKE}
            strokeDasharray={RING_CIRCUMFERENCE}
            strokeDashoffset={0}
            strokeLinecap="round"
            opacity={isInfinite ? 0.4 : 0.2}
          />
        )}
      </svg>
      <div className={`absolute inset-0 flex items-center justify-center text-[8px] font-bold leading-none ${quotaColor(text, percent)}`}>
        {isEmpty ? '-' : (hasRing ? Math.round(percent) : (isInfinite ? '∞' : ''))}
      </div>
    </div>
  );
};

const stopEvent = (event) => {
  event.stopPropagation();
};

const TH = 'border-b border-line px-4 py-3 whitespace-nowrap';
const TD = 'border-b border-line-soft px-4 py-3 align-middle whitespace-nowrap';

const AccountInventoryTable = ({
  rows = [],
  busyKey = '',
  selectedRowIds = [],
  allVisibleSelected = false,
  onToggleAllRows,
  onToggleRow,
  onRowClick,
  onToggleAccount,
  onClearAccountCooldown,
  onTestAccount,
  onRefreshAccountProfile,
  onEditAccount,
  onDeleteAccount
}) => {
  const selectedIdSet = new Set(selectedRowIds.map((item) => String(item)));

  return (
    <div className="relative">
      <div className="overflow-x-auto">
        <table className="min-w-[960px] w-full border-separate border-spacing-0">
          <thead>
            <tr className="bg-surface-sub text-left text-xs uppercase tracking-widest text-fg-subtle">
              <th className={`w-10 ${TH}`}>
                <input
                  type="checkbox"
                  checked={allVisibleSelected}
                  aria-label="选择全部账号"
                  onChange={() => onToggleAllRows?.()}
                  className="app-checkbox"
                />
              </th>
              <th className={TH}>账号名</th>
              <th className={TH}>类型</th>
              <th className={TH}>组别</th>
              <th className={TH}>状态</th>
              <th className={TH}>额度</th>
              <th className={TH}>最近成功</th>
              <th className={TH}>最近刷新</th>
              <th className={`${TH} text-right`}>操作</th>
            </tr>
          </thead>
          <tbody>
            {rows.map((row) => {
              const selected = selectedIdSet.has(String(row.id));
              const planText = normalizePlanLabel(row.planLabel);
              const actionBusy = Boolean(busyKey);

              return (
                <tr
                  key={String(row.id)}
                  onClick={() => onRowClick?.(row)}
                  tabIndex={0}
                  role="button"
                  className={`cursor-pointer bg-surface transition-colors hover:bg-surface-sub ${selected ? 'ring-2 ring-inset ring-accent-soft' : ''}`}
                >
                  <td className={TD} onClick={stopEvent}>
                    <input
                      type="checkbox"
                      checked={selected}
                      aria-label={`选择账号 ${row.name || row.id}`}
                      onChange={() => onToggleRow?.(row.id)}
                      className="app-checkbox"
                    />
                  </td>
                  <td className={TD}>
                    <div className="text-sm font-semibold text-fg">{row.name || '-'}</div>
                  </td>
                  <td className={TD}>
                    <div className="flex items-center gap-1.5">
                      <Badge
                        text={row.authLabel || '-'}
                        className={AUTH_BADGE_CLASS[row.authLabel] || 'tone-slate'}
                      />
                      {planText !== '-' && (
                        <Badge
                          text={planText}
                          className={PLAN_BADGE_CLASS[planText] || 'tone-slate'}
                        />
                      )}
                    </div>
                  </td>
                  <td className={TD}>
                    <Badge
                      text={row.groupLabel || '-'}
                      className={GROUP_BADGE_CLASS[row.groupLabel] || 'tone-slate'}
                    />
                  </td>
                  <td className={TD}>
                    <Badge
                      text={row.stateLabel || '-'}
                      className={toBadgeToneClass(row.stateTone)}
                    />
                  </td>
                  <td className={TD}>
                    <div className="flex items-center gap-2">
                      <QuotaRing label="5h" text={row.quota5hText} percent={row.quota5hPercent} />
                      <QuotaRing label="d7" text={row.quota7dText} percent={row.quota7dPercent} />
                    </div>
                  </td>
                  <td className={`${TD} text-sm font-medium text-success`}>{row.lastSuccessText || '-'}</td>
                  <td className={`${TD} text-sm font-medium text-success`}>{row.refreshedAtText || '-'}</td>
                  <td className={`${TD} text-right`}>
                    <div className="flex items-center justify-end gap-0.5" onClick={stopEvent}>
                      {row.state === 'cooldown' && <button type="button" disabled={actionBusy} onClick={() => onClearAccountCooldown?.(row.raw || row.detail?.rawAccount || row)} className="rounded-md px-1.5 py-1 text-[11px] text-warn hover:bg-warn-soft">解除冷却</button>}
                      <button
                        type="button"
                        role="switch"
                        aria-checked={row.enabled !== false}
                        aria-label={row.enabled === false ? '启用账号' : '停用账号'}
                        disabled={actionBusy}
                        onClick={() => onToggleAccount?.(row.raw || row.detail?.rawAccount || row)}
                        className={`relative inline-flex h-5 w-9 shrink-0 cursor-pointer rounded-full border-2 border-transparent transition-colors ${row.enabled === false ? 'bg-line-strong' : 'bg-success-solid'}`}
                      >
                        <span className={`pointer-events-none inline-block h-4 w-4 rounded-full bg-surface shadow-sm transition-transform ${row.enabled === false ? 'translate-x-0' : 'translate-x-4'}`} />
                      </button>
                      <button
                        type="button"
                        disabled={actionBusy}
                        onClick={() => onTestAccount?.(row.raw || row.detail?.rawAccount || row)}
                        className="p-1.5 text-fg-subtle hover:bg-info-soft hover:text-info rounded-md transition-colors"
                        title="测试连通性"
                      >
                        <TestTube2 size={14} />
                      </button>
                      <button
                        type="button"
                        disabled={actionBusy}
                        onClick={() => onRefreshAccountProfile?.(row.raw || row.detail?.rawAccount || row)}
                        className="p-1.5 text-fg-subtle hover:bg-success-soft hover:text-success rounded-md transition-colors"
                        title="刷新画像"
                      >
                        <RefreshCw size={14} />
                      </button>
                      <button
                        type="button"
                        disabled={actionBusy}
                        onClick={() => onEditAccount?.(row.raw || row.detail?.rawAccount || row)}
                        className="p-1.5 text-fg-subtle hover:bg-surface-mut hover:text-accent rounded-md transition-colors"
                        title="编辑"
                      >
                        <Pencil size={14} />
                      </button>
                      <button
                        type="button"
                        disabled={actionBusy}
                        onClick={() => onDeleteAccount?.(row.raw || row.detail?.rawAccount || row)}
                        className="p-1.5 text-fg-subtle hover:bg-danger-soft hover:text-danger rounded-md transition-colors"
                        title="删除"
                      >
                        <Trash2 size={14} />
                      </button>
                      <button
                        type="button"
                        onClick={() => onRowClick?.(row)}
                        className="p-1.5 text-fg-subtle hover:bg-surface-mut hover:text-fg-body rounded-md transition-colors"
                        title="查看详情"
                      >
                        <ArrowRight size={14} />
                      </button>
                    </div>
                  </td>
                </tr>
              );
            })}
          </tbody>
        </table>
      </div>
    </div>
  );
};

export default AccountInventoryTable;
