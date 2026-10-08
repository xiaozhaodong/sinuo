// ============================================
// 账号池账号行
// 2026-03-07
// ============================================

import { Edit3, RefreshCw, ShieldCheck, Trash2 } from 'lucide-react';
import { useTimezone } from '@contexts/TimezoneContext.jsx';
import Badge from './Badge.jsx';
import {
  ACCOUNT_STATE_STYLE,
  QUOTA_STATUS_STYLE,
  normalizePlanType,
  normalizePriorityValue,
  parseCodexModelRewriteSettings,
  resolveAccountId,
  toAccountAuthLabel,
  toAccountStateLabel,
  toPlanTypeLabel,
  toQuotaProgressClass,
  toQuotaStatusLabel,
  toRemainingPercent
} from '../utils.js';

const AccountRow = ({
  account,
  busyKey,
  priorityTierMetaMap,
  onEdit,
  onRefreshProfile,
  onTest,
  onDelete,
  onToggle
}) => {
  const { formatTimestamp, formatMonthDayTime } = useTimezone();
  const toDisplayTime = (value) => (value ? formatTimestamp(value) : '-');
  const formatQuotaResetTime = (value) => (value ? formatMonthDayTime(value) : '');
  const accountId = resolveAccountId(account) ?? account.account_name ?? account.accountName;
  const accountName = account.account_name || account.accountName || '-';
  const state = account.state || 'active';
  const stateClass = ACCOUNT_STATE_STYLE[state] || 'tone-slate';
  const quotaStatus = String(account.quota_status || account.quotaStatus || '').trim().toLowerCase() || 'pending';
  const quotaStatusClass = QUOTA_STATUS_STYLE[quotaStatus] || QUOTA_STATUS_STYLE.pending;
  const planType = account.plan_type || account.planType || '';
  const normalizedPlanType = normalizePlanType(planType);
  const normalizedProviderType = String(account.provider_type || account.providerType || '').trim().toLowerCase();
  const isAPIKeyAccount = normalizedProviderType === 'api_key';
  const modelRewriteSettings = parseCodexModelRewriteSettings(account.model_rewrite_rules || account.modelRewriteRules || '');
  const modelRewriteRuleLabels = (modelRewriteSettings.rules || [])
    .map((rule) => `${rule.source}→${rule.target}`);
  const modelRewriteBadgeText = modelRewriteRuleLabels.length > 1
    ? `${modelRewriteRuleLabels[0]} +${modelRewriteRuleLabels.length - 1}`
    : modelRewriteRuleLabels[0];
  const formatMultiplier = (value) => {
    const parsed = Number.parseFloat(value);
    return Number.isFinite(parsed) ? parsed.toFixed(1) : '1.0';
  };
  const multiplierSummary = isAPIKeyAccount
    ? `倍率 总×${formatMultiplier(account.cost_multiplier ?? account.costMultiplier)} / 入×${formatMultiplier(account.input_cost_multiplier ?? account.inputCostMultiplier)} / 出×${formatMultiplier(account.output_cost_multiplier ?? account.outputCostMultiplier)}`
    : '倍率固定 ×1.0';
  const planTypeLabel = toPlanTypeLabel(planType);
  const priority = normalizePriorityValue(account.priority ?? account.Priority);
  const tierMeta = Number.isFinite(priority) ? priorityTierMetaMap.get(priority) : null;
  const refreshedAt = account.quota_refreshed_at || account.quotaRefreshedAt;
  const normalizedBusyKey = String(busyKey || '');
  const rowBusy = normalizedBusyKey.startsWith('account-') && normalizedBusyKey.endsWith(`-${String(accountId)}`);

  const quota5hUsed = Number.parseFloat(account.quota_5h_used_percent ?? account.quota5hUsedPercent);
  const quotaWeeklyUsed = Number.parseFloat(account.quota_weekly_used_percent ?? account.quotaWeeklyUsedPercent);
  const quota5hRemaining = toRemainingPercent(quota5hUsed);
  const quotaWeeklyRemaining = toRemainingPercent(quotaWeeklyUsed);
  const quota5hResetAt = account.quota_5h_reset_at || account.quota5hResetAt;
  const quotaWeeklyResetAt = account.quota_weekly_reset_at || account.quotaWeeklyResetAt;
  const quota5hResetLabel = formatQuotaResetTime(quota5hResetAt);
  const quotaWeeklyResetLabel = formatQuotaResetTime(quotaWeeklyResetAt);

  return (
    <div className={`px-5 py-4 hover:bg-surface-sub transition-colors ${!account.enabled ? 'opacity-60' : ''}`}>
      <div className="flex flex-wrap items-center gap-x-3 gap-y-2">
        <button
          type="button"
          role="switch"
          aria-checked={account.enabled}
          onClick={() => onToggle(account)}
          disabled={rowBusy}
          title={account.enabled ? '停用' : '启用'}
          className={`relative inline-flex h-5 w-9 shrink-0 items-center rounded-full transition-colors duration-200 focus:outline-none focus-visible:ring-2 focus-visible:ring-accent-ring focus-visible:ring-offset-2 ${
            account.enabled ? 'bg-indigo-500' : 'bg-line-strong'
          } ${rowBusy ? 'opacity-50 cursor-not-allowed' : ''}`}
        >
          <span
            aria-hidden="true"
            className={`pointer-events-none inline-block h-3.5 w-3.5 transform rounded-full bg-surface shadow-sm ring-0 transition duration-200 ${
              account.enabled ? 'translate-x-[18px]' : 'translate-x-[2px]'
            }`}
          />
        </button>

        <span className="min-w-0 max-w-full truncate text-sm font-semibold text-fg sm:max-w-[220px]" title={accountName}>
          {accountName}
        </span>

        <Badge
          text={toAccountAuthLabel(account.provider_type || account.providerType || '')}
          className="bg-accent-soft text-accent border-accent-line"
        />

        {tierMeta && (
          <Badge
            text={tierMeta.label}
            className={tierMeta.className}
            title={`${tierMeta.description}${tierMeta.count > 1 ? `（当前同组共 ${tierMeta.count} 个账号）` : ''}`}
          />
        )}

        <Badge
          text={`组内顺序 ${Number.isFinite(priority) ? priority : '-'}`}
          className="bg-warn-soft text-warn border-warn-line"
        />

        {tierMeta?.count > 1 && (
          <Badge
            text={`同组 ${tierMeta.count} 个`}
            className="bg-surface text-fg-muted border-line"
            title="同组账号会先按组别命中，再在组内按顺序和健康度择优"
          />
        )}

        {planTypeLabel && (
          <Badge text={planTypeLabel} className="tone-violet" />
        )}

        {isAPIKeyAccount && modelRewriteSettings.enabled && (
          <Badge
            text={modelRewriteBadgeText}
            className="tone-cyan"
            title={modelRewriteRuleLabels.join(' / ')}
          />
        )}

        <div className="flex items-center gap-1.5 md:ml-auto">
          <Badge text={toQuotaStatusLabel(quotaStatus)} className={quotaStatusClass} />
          <Badge text={toAccountStateLabel(state)} className={stateClass} />
        </div>

        <div className="hidden h-5 w-px shrink-0 bg-surface-emph md:block" />

        <div className="flex items-center gap-0.5 shrink-0">
          <button
            type="button"
            className="p-1.5 text-fg-subtle hover:bg-accent-soft hover:text-accent rounded-md transition-colors"
            onClick={() => onEdit(account)}
            disabled={rowBusy}
            title="编辑"
          >
            <Edit3 size={14} />
          </button>
          <button
            type="button"
            className="p-1.5 text-fg-subtle hover:bg-accent-soft hover:text-accent rounded-md transition-colors"
            onClick={() => onRefreshProfile(account)}
            disabled={rowBusy}
            title="刷新账号信息"
          >
            <RefreshCw size={14} />
          </button>
          <button
            type="button"
            className="p-1.5 text-fg-subtle hover:bg-accent-soft hover:text-accent rounded-md transition-colors"
            onClick={() => onTest(account)}
            disabled={rowBusy}
            title="测试连通性"
          >
            <ShieldCheck size={14} />
          </button>
          <button
            type="button"
            className="p-1.5 text-fg-subtle hover:bg-danger-soft hover:text-danger rounded-md transition-colors"
            onClick={() => onDelete(account)}
            disabled={rowBusy}
            title="删除"
          >
            <Trash2 size={14} />
          </button>
        </div>
      </div>

      <div className="mt-3 flex flex-col gap-3 md:ml-12 md:gap-4 lg:flex-row lg:items-center lg:gap-6">
        <div className="flex min-w-0 w-full items-center gap-2 lg:max-w-[360px]">
          <span className="text-[11px] text-fg-subtle shrink-0 w-10">5h</span>
          {isAPIKeyAccount ? (
            <span className="text-[11px] text-fg-muted">无限额</span>
          ) : normalizedPlanType === 'free' ? (
            <span className="text-[11px] text-fg-subtle">无额度</span>
          ) : (
            <>
              <div className="flex-1 h-1.5 bg-surface-mut rounded-full overflow-hidden">
                <div
                  className={`h-full rounded-full transition-all duration-500 ${toQuotaProgressClass(quota5hRemaining)}`}
                  style={{ width: `${Number.isFinite(quota5hRemaining) ? quota5hRemaining : 0}%` }}
                />
              </div>
              <span className="text-[11px] font-medium text-fg-body shrink-0 text-right whitespace-nowrap" title={quota5hResetAt ? `重置 ${formatTimestamp(quota5hResetAt)}` : ''}>
                {Number.isFinite(quota5hRemaining) ? `${quota5hRemaining.toFixed(0)}%` : '-'}
                {quota5hResetLabel ? ` · ${quota5hResetLabel} 重置` : ''}
              </span>
            </>
          )}
        </div>

        <div className="flex min-w-0 w-full items-center gap-2 lg:max-w-[360px]">
          <span className="text-[11px] text-fg-subtle shrink-0 w-10">d7</span>
          {isAPIKeyAccount ? (
            <span className="text-[11px] text-fg-muted">无限额</span>
          ) : (
            <>
              <div className="flex-1 h-1.5 bg-surface-mut rounded-full overflow-hidden">
                <div
                  className={`h-full rounded-full transition-all duration-500 ${toQuotaProgressClass(quotaWeeklyRemaining)}`}
                  style={{ width: `${Number.isFinite(quotaWeeklyRemaining) ? quotaWeeklyRemaining : 0}%` }}
                />
              </div>
              <span className="text-[11px] font-medium text-fg-body shrink-0 text-right whitespace-nowrap" title={quotaWeeklyResetAt ? `重置 ${formatTimestamp(quotaWeeklyResetAt)}` : ''}>
                {Number.isFinite(quotaWeeklyRemaining) ? `${quotaWeeklyRemaining.toFixed(0)}%` : '-'}
                {quotaWeeklyResetLabel ? ` · ${quotaWeeklyResetLabel} 重置` : ''}
              </span>
            </>
          )}
        </div>

        <div className="hidden flex-1 lg:block" />

        <div className="flex flex-wrap items-center gap-x-3 gap-y-1 text-[11px] text-fg-subtle lg:shrink-0">
          <span>{multiplierSummary}</span>
          <span>刷新 {toDisplayTime(refreshedAt)}</span>
          {(account.last_success_at || account.lastSuccessAt) && (
            <span className="text-success">
              连通 {formatTimestamp(account.last_success_at || account.lastSuccessAt)}
            </span>
          )}
        </div>
      </div>
    </div>
  );
};

export default AccountRow;
