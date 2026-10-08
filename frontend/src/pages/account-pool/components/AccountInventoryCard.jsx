// ============================================
// 账号资产网格卡片
// 与 AccountInventoryTable 消费同一份 dashboardViewModel row
// 2026-08-06
// ============================================

import { AlertTriangle, ArrowRight, Pencil, RefreshCw, TestTube2, Trash2 } from 'lucide-react';
import { Button } from '@components/ui';
import Switch from '@components/desktop/Switch.jsx';
import {
  AUTH_BADGE_CLASS,
  GROUP_BADGE_CLASS,
  PLAN_BADGE_CLASS,
  normalizePlanLabel,
  toBadgeToneClass,
  toStateDotClass
} from './inventoryBadgeStyles.js';
import { useTimezone } from '@contexts/TimezoneContext.jsx';

const CardBadge = ({ text, className, title }) => (
  <span title={title} className={`inline-flex items-center rounded border px-1.5 text-[11px] leading-[18px] ${className}`}>
    {text}
  </span>
);

const stopEvent = (event) => {
  event.stopPropagation();
};

const ERROR_TONE_CLASS = {
  amber: 'border-warn-line bg-warn-soft text-warn',
  rose: 'border-danger-line bg-danger-soft text-danger',
  slate: 'tone-slate'
};

// 额度进度条(剩余口径):api_key 无限额 / free 无 5h / 百分比三种形态
const QuotaBar = ({ label, text, percent, resetAt }) => {
  const { formatMonthDayTime, formatTimestamp } = useTimezone();
  const hasPercent = Number.isFinite(percent);
  const resetLabel = resetAt ? formatMonthDayTime(resetAt) : '';
  const resetTitle = resetAt ? `重置 ${formatTimestamp(resetAt)}` : '';

  return (
    <div className="flex items-center gap-2">
      <span className="w-6 shrink-0 text-[11px] text-fg-subtle">{label}</span>
      {hasPercent ? (
        <>
          <div className="h-1.5 flex-1 overflow-hidden rounded-full bg-surface-mut">
            <div
              className={`h-full rounded-full transition-all duration-500 ${percent > 50 ? 'bg-success-solid' : percent > 20 ? 'bg-warn-solid' : 'bg-danger-solid'}`}
              style={{ width: `${Math.max(Math.min(percent, 100), 0)}%` }}
            />
          </div>
          <span className="shrink-0 whitespace-nowrap text-right text-[11px] font-medium text-fg-body" title={resetTitle}>
            {Math.round(percent)}%{resetLabel ? ` · ${resetLabel} 重置` : ''}
          </span>
        </>
      ) : (
        <span className={`text-[11px] ${text === '无限额' ? 'text-fg-muted' : 'text-fg-subtle'}`}>{text || '-'}</span>
      )}
    </div>
  );
};

const AccountInventoryCard = ({
  row,
  busyKey = '',
  selected = false,
  onToggleRow,
  onRowClick,
  onToggleAccount,
  onClearAccountCooldown,
  onTestAccount,
  onRefreshAccountProfile,
  onEditAccount,
  onDeleteAccount
}) => {
  const actionBusy = Boolean(busyKey);
  const planText = normalizePlanLabel(row.planLabel);
  const account = row.raw || row.detail?.rawAccount || row;
  const errorDisplay = row.errorDisplay;
  const statusText = `${row.lastSuccessAtMs > 0 ? `连通 ${row.lastSuccessText}` : '未连通'} · ${row.refreshedAtMs > 0 ? `刷新 ${row.refreshedAtText}` : '未刷新'}`;

  return (
    <div
      onClick={() => onRowClick?.(row)}
      className={`flex flex-col rounded-lg border bg-surface p-3 transition-colors hover:border-line-strong ${
        selected ? 'border-accent-line ring-2 ring-inset ring-accent-soft' : 'border-line'
      }`}
    >
      <div className="flex items-start justify-between gap-2">
        <div className="flex min-w-0 items-center gap-2">
          <span onClick={stopEvent} className="shrink-0">
            <input
              type="checkbox"
              checked={selected}
              aria-label={`选择账号 ${row.name || row.id}`}
              onChange={() => onToggleRow?.(row.id)}
              className="app-checkbox"
            />
          </span>
          <span className={`h-2 w-2 shrink-0 rounded-full ${toStateDotClass(row.stateTone)}`} />
          <span className="truncate text-[13px] font-semibold text-fg" title={row.name}>{row.name || '-'}</span>
        </div>
        <CardBadge text={row.stateLabel || '-'} className={toBadgeToneClass(row.stateTone)} />
      </div>

      <div className="mt-2 flex flex-wrap items-center gap-1.5">
        <CardBadge
          text={row.authLabel || '-'}
          className={AUTH_BADGE_CLASS[row.authLabel] || 'tone-slate'}
        />
        {planText !== '-' && (
          <CardBadge
            text={planText}
            className={PLAN_BADGE_CLASS[planText] || 'tone-slate'}
          />
        )}
        <CardBadge
          text={row.groupLabel || '-'}
          className={GROUP_BADGE_CLASS[row.groupLabel] || 'tone-slate'}
        />
        {Number.isFinite(row.priority) && (
          <CardBadge text={`顺序 ${row.priority}`} className="bg-warn-soft text-warn border-warn-line" />
        )}
      </div>

      <div className="mt-3 space-y-1.5">
        <QuotaBar label="5h" text={row.quota5hText} percent={row.quota5hPercent} resetAt={row.quota5hResetAt} />
        <QuotaBar label="d7" text={row.quota7dText} percent={row.quota7dPercent} resetAt={row.quota7dResetAt} />
      </div>

      <div className="mt-2 min-w-0 truncate whitespace-nowrap text-[11px] text-fg-muted" title={statusText} onClick={stopEvent}>
        {statusText}
      </div>

      <div className="flex min-h-7 items-center">
        {errorDisplay ? (
          <div
            className={`flex w-full min-w-0 items-center gap-1.5 rounded-md border px-2 py-1 text-[11px] ${ERROR_TONE_CLASS[errorDisplay.tone] || ERROR_TONE_CLASS.slate}`}
            title={errorDisplay.message}
          >
            <AlertTriangle size={12} className="shrink-0" aria-hidden="true" />
            <span className="shrink-0 font-semibold">{errorDisplay.label}</span>
            <span className="shrink-0 opacity-50" aria-hidden="true">·</span>
            <span className="truncate">{errorDisplay.summary}</span>
          </div>
        ) : null}
      </div>

      <div className="mt-auto flex items-center justify-between gap-2 border-t border-line-soft pt-2" onClick={stopEvent}>
        <div className="flex min-w-0 flex-1 items-center gap-2.5">
          <Switch
            checked={row.enabled !== false}
            label={row.enabled === false ? '启用账号' : '停用账号'}
            disabled={actionBusy}
            onChange={() => onToggleAccount?.(account)}
          />
        </div>
        <div className="flex shrink-0 items-center gap-0.5">
          {row.state === 'cooldown' && (
            <Button type="button" variant="ghost" size="sm" disabled={actionBusy} onClick={() => onClearAccountCooldown?.(account)} className="text-warn">解除冷却</Button>
          )}
          <button
            type="button"
            disabled={actionBusy}
            onClick={() => onTestAccount?.(account)}
            className="inline-flex h-6 w-6 items-center justify-center rounded text-fg-muted transition-colors hover:bg-surface-mut hover:text-info"
            title="测试连通性"
          >
            <TestTube2 size={14} />
          </button>
          <button
            type="button"
            disabled={actionBusy}
            onClick={() => onRefreshAccountProfile?.(account)}
            className="inline-flex h-6 w-6 items-center justify-center rounded text-fg-muted transition-colors hover:bg-surface-mut hover:text-success"
            title="刷新画像"
          >
            <RefreshCw size={14} />
          </button>
          <button
            type="button"
            disabled={actionBusy}
            onClick={() => onEditAccount?.(account)}
            className="inline-flex h-6 w-6 items-center justify-center rounded text-fg-muted transition-colors hover:bg-surface-mut hover:text-accent"
            title="编辑"
          >
            <Pencil size={14} />
          </button>
          <button
            type="button"
            disabled={actionBusy}
            onClick={() => onDeleteAccount?.(account)}
            className="inline-flex h-6 w-6 items-center justify-center rounded text-fg-muted transition-colors hover:bg-surface-mut hover:text-danger"
            title="删除"
          >
            <Trash2 size={14} />
          </button>
          <button
            type="button"
            onClick={() => onRowClick?.(row)}
            className="inline-flex h-6 w-6 items-center justify-center rounded text-fg-muted transition-colors hover:bg-surface-mut hover:text-fg"
            title="查看详情"
          >
            <ArrowRight size={14} />
          </button>
        </div>
      </div>
    </div>
  );
};

export default AccountInventoryCard;
