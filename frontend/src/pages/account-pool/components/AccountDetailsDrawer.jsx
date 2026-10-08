// ============================================
// 账号详情
// 2026-03-21
// 2026-10-07 桌面化：由右侧滑出的抽屉改为常驻右侧的检查器，选中行即显示；
//            「额度 / 健康 / 路由配置 / 动作」四块内容与动作不变
// ============================================

import { AlertTriangle, Copy, Users } from 'lucide-react';
import { Button } from '@components/ui';
import {
  Inspector,
  InspectorBody,
  InspectorEmpty,
  InspectorHeader,
  InspectorSection,
  Property,
  PropertyList
} from '@components/desktop/Inspector.jsx';
import { parseCodexModelRewriteSettings } from '../utils.js';
import { PLAN_BADGE_CLASS, normalizePlanLabel } from './inventoryBadgeStyles.js';

const STATE_TEXT_CLASS = {
  emerald: 'text-success',
  amber: 'text-warn',
  rose: 'text-danger',
  slate: 'text-fg-subtle'
};

const ERROR_TONE_CLASS = {
  amber: 'tone-amber',
  rose: 'tone-rose',
  slate: 'tone-slate'
};

const quotaBarColor = (percent) => {
  if (!Number.isFinite(percent)) return 'bg-line-strong';
  if (percent > 50) return 'bg-success-solid';
  if (percent > 20) return 'bg-warn-solid';
  return 'bg-danger-solid';
};

const quotaTextColor = (percent) => {
  if (!Number.isFinite(percent)) return 'text-fg-muted';
  if (percent > 50) return 'text-success';
  if (percent > 20) return 'text-warn';
  return 'text-danger';
};

const QuotaBar = ({ label, text, percent }) => {
  const hasBar = Number.isFinite(percent);

  return (
    <div className="grid grid-cols-[34px_minmax(0,1fr)_56px] items-center gap-2 text-[12.5px]">
      <span className="text-fg-muted">{label}</span>
      <div className="h-1.5 w-full overflow-hidden rounded-full bg-surface-emph">
        {hasBar && (
          <div
            className={`h-full rounded-full transition-all ${quotaBarColor(percent)}`}
            style={{ width: `${Math.max(Math.min(percent, 100), 0)}%` }}
          />
        )}
      </div>
      <span className={`text-right tabular-nums ${quotaTextColor(percent)}`}>{text || '-'}</span>
    </div>
  );
};

const DetailRow = ({ label, value }) => (
  <Property label={label} title={typeof value === 'string' ? value : undefined}>{value || '-'}</Property>
);

const AccountErrorDetails = ({ display }) => {
  if (!display) {
    return <div className="text-xs text-fg-subtle">最近异常：暂无错误记录</div>;
  }

  const copyRawError = () => navigator.clipboard.writeText(display.raw);

  return (
    <div className={`rounded-md border px-2.5 py-2 text-xs ${ERROR_TONE_CLASS[display.tone] || ERROR_TONE_CLASS.slate}`}>
      <div className="flex items-center gap-1.5">
        <AlertTriangle size={13} className="shrink-0" aria-hidden="true" />
        <span className="font-semibold">{display.label}</span>
        <span className="opacity-70">最近异常</span>
      </div>
      <p className="selectable mt-1.5 break-words leading-5">{display.message}</p>
      {display.requestId ? (
        <div className="mt-1.5 flex items-center justify-between gap-2 border-t border-current/10 pt-1.5">
          <span className="opacity-70">Request ID</span>
          <code className="min-w-0 truncate font-mono">{display.requestId}</code>
        </div>
      ) : null}
      <details className="mt-1.5 border-t border-current/10 pt-1.5">
        <summary className="select-none font-medium">查看原始响应</summary>
        <div className="mt-1.5 rounded bg-surface/70 p-2">
          <div className="mb-1 flex justify-end">
            <button
              type="button"
              onClick={copyRawError}
              className="inline-flex items-center gap-1 rounded px-1.5 py-0.5 font-medium opacity-70 transition hover:bg-surface hover:opacity-100"
              title="复制原始响应"
            >
              <Copy size={12} aria-hidden="true" />复制
            </button>
          </div>
          <pre className="max-h-40 overflow-auto whitespace-pre-wrap break-all font-mono leading-5">{display.raw}</pre>
        </div>
      </details>
    </div>
  );
};

const normalizeGroupKey = (value = '') => {
  const normalized = String(value || '').trim().toLowerCase();
  if (normalized === 'primary' || normalized === '主组') return 'primary';
  if (normalized === 'backup' || normalized === '备组') return 'backup';
  if (normalized === 'cold' || normalized === '冷备') return 'cold';
  return '';
};

const AccountDetailsDrawer = ({
  open = false,
  row = null,
  busyKey = '',
  onToggleAccount,
  onClearAccountCooldown,
  onTestAccount,
  onRefreshAccountProfile,
  onMoveAccountToTier,
  onDeleteAccount,
  onEditAccount
}) => {
  if (!open || !row) {
    return (
      <Inspector label="账号详情">
        <InspectorEmpty icon={Users}>选择一个账号查看详情</InspectorEmpty>
      </Inspector>
    );
  }

  const detail = row.detail || {};
  const errorDisplay = row.errorDisplay || detail.errorDisplay;
  const account = detail.rawAccount || detail.account || row;
  const currentGroupKey = normalizeGroupKey(detail.groupKey || row.groupKey || row.groupLabel);
  const enabled = detail.enabled ?? account.enabled ?? true;
  const planText = normalizePlanLabel(row.planLabel);
  const modelRewriteSettings = parseCodexModelRewriteSettings(account.model_rewrite_rules || account.modelRewriteRules || '');
  const modelRewriteDetailText = modelRewriteSettings.enabled
    ? (modelRewriteSettings.rules || [])
      .map((rule) => `${rule.source} -> ${rule.target}`)
      .join(' / ')
    : '未启用';
  const actionBusy = Boolean(busyKey);
  const moveBusy = actionBusy;
  const groupActions = [
    { key: 'primary', label: '主组' },
    { key: 'backup', label: '备组' },
    { key: 'cold', label: '冷备' }
  ];

  return (
    <Inspector label="账号详情">
      <InspectorHeader
        title={row.name || '-'}
        subtitle={`${row.authLabel || '-'} · ${row.groupLabel || '-'}`}
        badges={(
          <>
            {planText !== '-' && (
              <span className={`shrink-0 rounded border px-1 text-[10.5px] font-medium leading-4 ${PLAN_BADGE_CLASS[planText] || 'tone-slate'}`}>{planText}</span>
            )}
            <span className={`shrink-0 text-xs ${STATE_TEXT_CLASS[row.stateTone] || STATE_TEXT_CLASS.slate}`}>{row.stateLabel || '-'}</span>
          </>
        )}
      />

      <InspectorBody>
        <InspectorSection title="额度">
          <div className="mb-2.5 space-y-2">
            <QuotaBar label="5h" text={row.quota5hText} percent={row.quota5hPercent} />
            <QuotaBar label="d7" text={row.quota7dText} percent={row.quota7dPercent} />
          </div>
          <PropertyList labelWidth={64}>
            <DetailRow label="额度状态" value={detail.quotaStatusLabel || detail.quotaStatus || '-'} />
            <DetailRow label="最近刷新" value={row.refreshedAtText} />
            {detail.quota5hResetText ? <DetailRow label="5h 重置" value={detail.quota5hResetText} /> : null}
            {detail.quota7dResetText ? <DetailRow label="d7 重置" value={detail.quota7dResetText} /> : null}
            {!detail.quota5hResetText && !detail.quota7dResetText ? (
              <DetailRow label="下次重置" value={detail.nextResetText || detail.quotaResetText || '-'} />
            ) : null}
          </PropertyList>
        </InspectorSection>

        <InspectorSection title="健康">
          <PropertyList labelWidth={64}>
            <DetailRow label="最近成功" value={row.lastSuccessText} />
            <DetailRow label="连通性" value={detail.healthLabel || detail.reachabilityLabel || '-'} />
            <DetailRow label="风险摘要" value={detail.riskLabel || '-'} />
            <DetailRow label="观察备注" value={detail.healthNote || detail.note || '-'} />
          </PropertyList>
          <div className="mt-2.5">
            <AccountErrorDetails display={errorDisplay} />
          </div>
        </InspectorSection>

        <InspectorSection title="路由配置">
          <PropertyList labelWidth={64}>
            <DetailRow label="路由组别" value={row.groupLabel} />
            <DetailRow label="组内顺序" value={detail.priorityLabel || detail.priority || '-'} />
            <DetailRow label="组内角色" value={detail.groupOrderLabel || detail.groupOrder || '-'} />
            <DetailRow label="Base URL" value={detail.baseUrl || detail.baseURL || '-'} />
            <DetailRow label="模型兼容" value={modelRewriteDetailText} />
            <DetailRow label="路由备注" value={detail.routingNote || detail.routeNote || '-'} />
          </PropertyList>
        </InspectorSection>

        <InspectorSection title="动作">
          <div className="grid grid-cols-2 gap-1.5">
            {account.state === 'cooldown' && <Button variant="secondary" size="sm" disabled={actionBusy} onClick={() => onClearAccountCooldown?.(account)}>解除冷却</Button>}
            <Button
              variant="secondary"
              size="sm"
              disabled={actionBusy}
              onClick={() => onToggleAccount?.(account)}
            >
              {enabled ? '停用账号' : '启用账号'}
            </Button>
            <Button variant="secondary" size="sm" disabled={actionBusy} onClick={() => onTestAccount?.(account)}>
              测试账号
            </Button>
            <Button variant="secondary" size="sm" disabled={actionBusy} onClick={() => onRefreshAccountProfile?.(account)}>
              刷新画像
            </Button>
            <Button variant="secondary" size="sm" disabled={actionBusy} onClick={() => onEditAccount?.(account)}>
              编辑账号
            </Button>
          </div>
          <div className="mt-1.5 grid grid-cols-3 gap-1.5">
            {groupActions.map((action) => {
              const isCurrentGroup = currentGroupKey === action.key;
              const disabled = moveBusy || isCurrentGroup;
              const buttonText = isCurrentGroup ? `已在${action.label}` : `移到${action.label}`;
              const title = moveBusy
                ? '正在更新组别，请稍候'
                : isCurrentGroup
                  ? `当前账号已在${action.label}，无需移动`
                  : `将当前账号移动到${action.label}`;

              return (
                <Button
                  key={action.key}
                  variant="secondary"
                  size="sm"
                  disabled={disabled}
                  title={title}
                  onClick={() => onMoveAccountToTier?.(account, action.key)}
                >
                  {buttonText}
                </Button>
              );
            })}
          </div>
          <div className="mt-2.5 border-t border-line-soft pt-2.5">
            <button
              type="button"
              disabled={actionBusy}
              onClick={() => onDeleteAccount?.(account)}
              className="inline-flex h-6 w-full items-center justify-center rounded-md border border-line bg-surface text-xs font-medium text-danger shadow-xs transition-colors hover:bg-danger-soft disabled:opacity-50"
            >
              删除账号…
            </button>
          </div>
        </InspectorSection>
      </InspectorBody>
    </Inspector>
  );
};

export default AccountDetailsDrawer;
