// ============================================
// Account Pool 调度组卡片（紧凑版）
// 2026-03-22
// ============================================

import { useState } from 'react';
import { ArrowUpCircle, ChevronDown, ChevronRight, ExternalLink, Pin, RotateCcw, Target } from 'lucide-react';
import { Button } from '@components/ui';
import { FormSection } from '@components/desktop/FormSection.jsx';

const DEFAULT_VISIBLE = 10;

// 三组状态沿用现有 tone 配色，分组外壳由 FormSection 统一提供。
const GROUP_STYLE = {
  primary: {
    label: '主组',
    badgeClassName: 'tone-indigo'
  },
  backup: {
    label: '备组',
    badgeClassName: 'tone-cyan'
  },
  cold: {
    label: '冷备',
    badgeClassName: 'tone-violet'
  }
};

const STATE_TONE_CLASS = {
  rose: 'tone-rose',
  red: 'tone-rose',
  amber: 'tone-amber',
  emerald: 'tone-emerald',
  green: 'tone-emerald',
  slate: 'tone-slate'
};

const normalizeGroupKey = (key = '') => {
  const normalized = String(key || '').trim().toLowerCase();
  if (normalized === 'primary' || normalized.includes('main')) return 'primary';
  if (normalized === 'backup' || normalized.includes('secondary')) return 'backup';
  return 'cold';
};

const toAccountId = (account = {}) => account?.id ?? account?.accountId ?? account?.account_id;
const toActionAccount = (account = {}) => account?.rawAccount || account;

const ACTION_ICON_BY_KEY = {
  'swap-up': ArrowUpCircle,
  'swap-down': RotateCcw
};

const ACTION_VARIANT_BY_TONE = {
  primary: 'primary',
  secondary: 'secondary',
  ghost: 'ghost'
};

const quotaStrokeColor = (text, percent) => {
  if (!text || text === '-' || text === '无' || text === '未刷新') return 'var(--color-line-strong)';
  if (text === '无限额') return 'var(--color-fg-muted)';
  if (!Number.isFinite(percent)) return 'var(--color-fg-muted)';
  if (percent > 50) return 'var(--color-success-solid)';
  if (percent > 20) return 'var(--color-warn-solid)';
  return 'var(--color-danger-solid)';
};

const quotaTextColor = (text, percent) => {
  if (!text || text === '-' || text === '无' || text === '未刷新') return 'text-fg-muted';
  if (text === '无限额') return 'text-fg-muted';
  if (!Number.isFinite(percent)) return 'text-fg-muted';
  if (percent > 50) return 'text-success';
  if (percent > 20) return 'text-warn';
  return 'text-danger';
};

const RING_SIZE = 20;
const RING_STROKE = 2.5;
const RING_RADIUS = (RING_SIZE - RING_STROKE) / 2;
const RING_CIRCUMFERENCE = 2 * Math.PI * RING_RADIUS;

const MiniQuotaRing = ({ label, text, percent }) => {
  const hasRing = Number.isFinite(percent);
  const fillPercent = hasRing ? Math.max(Math.min(percent, 100), 0) : 0;
  const dashOffset = RING_CIRCUMFERENCE - (fillPercent / 100) * RING_CIRCUMFERENCE;
  const stroke = quotaStrokeColor(text, percent);
  const isInfinite = text === '无限额';
  const isEmpty = !text || text === '-' || text === '无' || text === '未刷新';
  const tooltip = `${label}: ${text || '-'}`;

  return (
    <div className="relative shrink-0" style={{ width: RING_SIZE, height: RING_SIZE }} title={tooltip}>
      <svg width={RING_SIZE} height={RING_SIZE} className="-rotate-90">
        <circle cx={RING_SIZE / 2} cy={RING_SIZE / 2} r={RING_RADIUS} fill="none" stroke="var(--color-line-soft)" strokeWidth={RING_STROKE} />
        {hasRing && (
          <circle cx={RING_SIZE / 2} cy={RING_SIZE / 2} r={RING_RADIUS} fill="none" stroke={stroke} strokeWidth={RING_STROKE}
            strokeDasharray={RING_CIRCUMFERENCE} strokeDashoffset={dashOffset} strokeLinecap="round" />
        )}
        {!hasRing && !isEmpty && (
          <circle cx={RING_SIZE / 2} cy={RING_SIZE / 2} r={RING_RADIUS} fill="none" stroke={stroke} strokeWidth={RING_STROKE}
            strokeDasharray={RING_CIRCUMFERENCE} strokeDashoffset={0} strokeLinecap="round" opacity={isInfinite ? 0.4 : 0.2} />
        )}
      </svg>
      <div className={`absolute inset-0 flex items-center justify-center text-[7px] font-bold leading-none ${quotaTextColor(text, percent)}`}>
        {isEmpty ? '-' : (hasRing ? Math.round(percent) : (isInfinite ? '∞' : ''))}
      </div>
    </div>
  );
};

const GroupBoardCard = ({
  group = {},
  onSwapGroup,
  onSetActiveAccount,
  onPinAccountSelection,
  onViewInInventory,
  busyKey = ''
}) => {
  const [expanded, setExpanded] = useState(false);
  const [showAll, setShowAll] = useState(false);

  const groupKey = normalizeGroupKey(group?.key);
  const style = GROUP_STYLE[groupKey];
  const accounts = Array.isArray(group?.accounts) ? group.accounts : [];
  const actions = Array.isArray(group?.actions) ? group.actions : [];
  const availableCount = accounts.filter((a) => a.isAvailable).length;
  const manualAccount = accounts.find((a) => a?.isActive) || null;
  const preferredAccount = accounts.find((a) => a?.isGroupPreferred) || null;
  const activeAccount = manualAccount || preferredAccount || accounts[0] || null;
  const hasManualPinnedAccount = accounts.some((a) => a?.isActive);
  const hasGroupPreferredAccount = accounts.some((a) => a?.isGroupPreferred);
  const activeAccountName = activeAccount?.account_name || activeAccount?.name || '暂无';

  const visibleAccounts = showAll ? accounts : accounts.slice(0, DEFAULT_VISIBLE);
  const hiddenCount = accounts.length - visibleAccounts.length;
  const groupActionBusy = Boolean(busyKey) && busyKey.startsWith(`group-swap-${groupKey}-`);

  const handleGroupAction = (action) => {
    if (!action || action.disabled) {
      return;
    }
    onSwapGroup?.(group, action);
  };

  return (
    <FormSection
      title={group?.label || style.label}
      actions={<span className={`${style.badgeClassName} rounded border px-1.5 text-[11px] leading-[18px]`}>{availableCount}/{accounts.length} 可用</span>}
    >
      <button
        type="button"
        onClick={() => setExpanded((v) => !v)}
        className="flex w-full items-center gap-3 px-3.5 py-2 text-left text-xs transition-colors hover:bg-surface-sub"
      >
        <div className="flex items-center gap-2 min-w-0 flex-1">
          {expanded ? <ChevronDown size={14} className="text-fg-muted shrink-0" /> : <ChevronRight size={14} className="text-fg-muted shrink-0" />}
          <span className="text-xs text-fg-body font-medium truncate">
            {hasManualPinnedAccount ? '全局手动' : hasGroupPreferredAccount ? '本组首选' : '顺序首位'}: {activeAccountName}
          </span>
        </div>
        <span className="text-xs text-fg-muted shrink-0">{group?.healthSummary || ''}</span>
      </button>

      {expanded && (
        <div className="divide-y divide-line-soft">
          {accounts.length === 0 ? (
            <div className="px-3.5 py-3 text-xs text-fg-muted">该组暂无账号。</div>
          ) : (
            <>
              <table className="w-full text-xs">
                <thead>
                  <tr className="h-7 border-b border-line-soft text-left text-[11.5px] text-fg-muted">
                    <th className="px-3.5 py-1.5 font-medium">名称</th>
                    <th className="px-2 py-1.5 font-medium">状态</th>
                    <th className="px-2 py-1.5 font-medium">额度</th>
                    <th className="px-2 py-1.5 font-medium text-right">操作</th>
                  </tr>
                </thead>
                <tbody>
                  {visibleAccounts.map((account, index) => {
                    const accountId = toAccountId(account);
                    const isActive = account.isActive;
                    const toneCls = STATE_TONE_CLASS[account.stateTone] || STATE_TONE_CLASS.slate;

                    return (
                      <tr key={String(accountId ?? `${groupKey}-${index}`)} className="border-t border-line-soft hover:bg-surface-sub">
                        <td className="px-3.5 py-1.5 whitespace-nowrap">
                          <div className="flex items-center gap-1.5">
                            <span className="font-medium text-fg">{account.name || `候选 ${index + 1}`}</span>
                            {isActive && <span className="rounded border px-1.5 text-[11px] leading-[18px] tone-indigo">{"全局手动"}</span>}
                            {!isActive && account?.isGroupPreferred && <span className="rounded border px-1.5 text-[11px] leading-[18px] tone-emerald">{"本组首选"}</span>}
                          </div>
                        </td>
                        <td className="px-2 py-1.5 whitespace-nowrap">
                          <span className={"rounded border px-1.5 text-[11px] leading-[18px] " + (toneCls)}>{account.stateLabel || '-'}</span>
                        </td>
                        <td className="px-2 py-1.5 whitespace-nowrap">
                          <div className="flex items-center gap-1.5">
                            <MiniQuotaRing label="5h" text={account.quota5hText} percent={account.quota5hPercent} />
                            <MiniQuotaRing label="d7" text={account.quota7dText} percent={account.quota7dPercent} />
                          </div>
                        </td>
                        <td className="px-2 py-1.5 whitespace-nowrap text-right">
                          <div className="flex items-center justify-end gap-1">
                            {!isActive && !account?.isGroupPreferred && typeof onSetActiveAccount === 'function' && (
                              <button
                                type="button"
                                title="设为本组首选"
                                disabled={busyKey === `group-active-${groupKey}-${accountId}` || groupActionBusy}
                                onClick={() => onSetActiveAccount?.(toActionAccount(account), group)}
                                className="inline-flex h-6 w-6 items-center justify-center rounded text-fg-muted hover:bg-surface-mut hover:text-fg disabled:opacity-40"
                              >
                                <Target size={14} />
                              </button>
                            )}
                            {!isActive && typeof onPinAccountSelection === 'function' && (
                              <button
                                type="button"
                                title="设为全局手动"
                                disabled={Boolean(busyKey)}
                                onClick={() => onPinAccountSelection?.(toActionAccount(account))}
                                className="inline-flex h-6 w-6 items-center justify-center rounded text-fg-muted hover:bg-surface-mut hover:text-fg disabled:opacity-40"
                              >
                                <Pin size={13} />
                              </button>
                            )}
                          </div>
                        </td>
                      </tr>
                    );
                  })}
                </tbody>
              </table>

              {hiddenCount > 0 && (
                <div className="border-t border-line-soft px-3.5 py-1.5 text-center">
                  <button
                    type="button"
                    onClick={() => setShowAll(true)}
                    className="text-xs font-medium text-accent hover:text-accent-fg transition-colors"
                  >
                    展开全部 {accounts.length} 个账号
                  </button>
                </div>
              )}

              {showAll && accounts.length > DEFAULT_VISIBLE && (
                <div className="border-t border-line-soft px-3.5 py-1.5 text-center">
                  <button
                    type="button"
                    onClick={() => setShowAll(false)}
                    className="text-xs font-medium text-fg-muted hover:text-fg-body transition-colors"
                  >
                    收起，只显示前 {DEFAULT_VISIBLE} 个
                  </button>
                </div>
              )}
            </>
          )}

          {actions.length > 0 && activeAccount ? (
            <div className="border-t border-line-soft px-3.5 py-2">
              <div className="flex flex-col gap-2 sm:flex-row sm:items-center sm:justify-between">
                <div>
                  <div className="text-xs font-medium text-fg-body">当前组动作</div>
                  <div className="mt-0.5 text-[11px] text-fg-muted">与相邻组整组交换，不是单账号加入</div>
                </div>

                <div className="flex flex-wrap items-center gap-2">
                  {actions
                    .filter((action) => action.key === 'swap-up' || action.key === 'swap-down')
                    .map((action) => {
                      const Icon = ACTION_ICON_BY_KEY[action.key];
                      const variant = ACTION_VARIANT_BY_TONE[action.tone] || 'secondary';

                      return (
                        <Button
                          key={action.key}
                          type="button"
                          size="sm"
                          variant={variant}
                          disabled={Boolean(action.disabled) || groupActionBusy}
                          onClick={() => handleGroupAction(action)}
                          className="whitespace-nowrap"
                        >
                          {Icon ? <Icon size={14} className="mr-1" /> : null}
                          {action.label}
                        </Button>
                      );
                    })}
                </div>
              </div>
            </div>
          ) : null}

          {onViewInInventory && accounts.length > 0 && (
            <div className="border-t border-line-soft px-3.5 py-1.5">
              <button
                type="button"
                onClick={() => onViewInInventory?.(groupKey)}
                className="flex items-center gap-1 text-xs font-medium text-fg-muted hover:text-accent transition-colors"
              >
                <ExternalLink size={12} />
                去账号资产查看本组全部账号
              </button>
            </div>
          )}
        </div>
      )}
    </FormSection>
  );
};

export default GroupBoardCard;
