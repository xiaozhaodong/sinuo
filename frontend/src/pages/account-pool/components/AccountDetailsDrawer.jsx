// ============================================
// 账号详情抽屉
// 2026-03-21
// ============================================

import { useRef } from 'react';
import { createPortal } from 'react-dom';
import { Activity, AlertTriangle, Copy, Gauge, Settings, Shield, X } from 'lucide-react';
import { Button } from '@components/ui';
import useModalLifecycle from '@hooks/useModalLifecycle.js';
import Badge from './Badge.jsx';
import { parseCodexModelRewriteSettings } from '../utils.js';

const TONE_CLASS = {
  rose: 'tone-rose',
  red: 'tone-rose',
  amber: 'tone-amber',
  yellow: 'tone-amber',
  emerald: 'tone-emerald',
  green: 'tone-emerald',
  sky: 'tone-sky',
  blue: 'tone-sky',
  indigo: 'tone-indigo',
  slate: 'tone-slate'
};

const ERROR_TONE_CLASS = {
  amber: 'tone-amber',
  rose: 'tone-rose',
  slate: 'tone-slate'
};

const SECTION_THEME = {
  '额度': { iconBg: 'bg-warn-soft', iconColor: 'text-warn', border: 'border-warn-line' },
  '健康': { iconBg: 'bg-success-soft', iconColor: 'text-success', border: 'border-success-line' },
  '路由配置': { iconBg: 'bg-info-soft', iconColor: 'text-info', border: 'border-info-line' },
  '动作': { iconBg: 'bg-accent-soft', iconColor: 'text-accent', border: 'border-accent-line' }
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
    <div className="space-y-1.5">
      <div className="flex items-center justify-between text-sm">
        <span className="text-fg-subtle">{label}</span>
        <span className={`font-semibold ${quotaTextColor(percent)}`}>{text || '-'}</span>
      </div>
      {hasBar && (
        <div className="h-2 w-full overflow-hidden rounded-full bg-surface-mut">
          <div
            className={`h-full rounded-full transition-all ${quotaBarColor(percent)}`}
            style={{ width: `${Math.max(Math.min(percent, 100), 0)}%` }}
          />
        </div>
      )}
    </div>
  );
};

const SectionCard = ({ icon: Icon, title, children }) => {
  const theme = SECTION_THEME[title] || { iconBg: 'bg-surface-mut', iconColor: 'text-fg-muted', border: 'border-line' };

  return (
    <section className={`rounded-xl border ${theme.border} bg-surface p-4 shadow-sm`}>
      <div className="mb-3 flex items-center gap-2">
        <div className={`flex h-8 w-8 items-center justify-center rounded-lg ${theme.iconBg} ${theme.iconColor}`}>
          <Icon size={16} />
        </div>
        <h3 className="text-sm font-semibold text-fg">{title}</h3>
      </div>
      <div className="space-y-3">{children}</div>
    </section>
  );
};

const DetailRow = ({ label, value }) => (
  <div className="flex items-start justify-between gap-4 text-sm">
    <span className="text-fg-subtle">{label}</span>
    <span className="min-w-0 max-w-[70%] break-words text-right font-medium text-fg-body">{value || '-'}</span>
  </div>
);

const AccountErrorDetails = ({ display }) => {
  if (!display) {
    return <DetailRow label="最近异常" value="暂无错误记录" />;
  }

  const copyRawError = () => navigator.clipboard.writeText(display.raw);

  return (
    <div className={`rounded-lg border p-3 ${ERROR_TONE_CLASS[display.tone] || ERROR_TONE_CLASS.slate}`}>
      <div className="flex items-center gap-2">
        <AlertTriangle size={15} className="shrink-0" aria-hidden="true" />
        <span className="text-sm font-semibold">{display.label}</span>
        <span className="text-xs opacity-70">最近异常</span>
      </div>
      <p className="mt-2 break-words text-sm leading-6">{display.message}</p>
      {display.requestId ? (
        <div className="mt-2 flex items-center justify-between gap-2 border-t border-current/10 pt-2 text-xs">
          <span className="opacity-70">Request ID</span>
          <code className="min-w-0 truncate font-mono">{display.requestId}</code>
        </div>
      ) : null}
      <details className="mt-2 border-t border-current/10 pt-2 text-xs">
        <summary className="cursor-pointer select-none font-medium">查看原始响应</summary>
        <div className="mt-2 rounded-md bg-surface/70 p-2">
          <div className="mb-1 flex justify-end">
            <button
              type="button"
              onClick={copyRawError}
              className="inline-flex items-center gap-1 rounded-md px-1.5 py-1 font-medium opacity-70 transition hover:bg-surface hover:opacity-100"
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
  onClose,
  onToggleAccount,
  onClearAccountCooldown,
  onTestAccount,
  onRefreshAccountProfile,
  onMoveAccountToTier,
  onDeleteAccount,
  onEditAccount
}) => {
  const closeButtonRef = useRef(null);

  useModalLifecycle({ open: open && Boolean(row), onClose, initialFocusRef: closeButtonRef });

  if (!open || !row) {
    return null;
  }

  const detail = row.detail || {};
  const errorDisplay = row.errorDisplay || detail.errorDisplay;
  const account = detail.rawAccount || detail.account || row;
  const currentGroupKey = normalizeGroupKey(detail.groupKey || row.groupKey || row.groupLabel);
  const enabled = detail.enabled ?? account.enabled ?? true;
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

  const nav = document.querySelector('nav.sticky');
  const topOffset = nav ? nav.getBoundingClientRect().bottom : 0;

  return createPortal(
    <div className="fixed inset-0 z-[45] flex justify-end" style={{ top: topOffset }}>
      <button
        type="button"
        aria-label="关闭账号详情"
        className="absolute inset-0 bg-overlay"
        onClick={onClose}
      />

      <aside
        role="dialog"
        aria-modal="true"
        aria-label="账号详情"
        className="relative z-10 flex h-full w-full max-w-[560px] flex-col border-l border-line bg-surface-sub shadow-2xl"
      >
        <div className="flex items-start justify-between gap-4 border-b border-line bg-surface px-5 py-4">
          <div className="min-w-0 space-y-2">
            <div className="text-xs font-semibold uppercase tracking-widest text-fg-subtle">账号详情</div>
            <div className="text-xl font-semibold text-fg">{row.name || '-'}</div>
            <div className="flex flex-wrap gap-2">
              <Badge text={row.authLabel || '-'} className="tone-indigo" />
              <Badge text={row.planLabel || '-'} className="tone-violet" />
              <Badge text={row.groupLabel || '-'} className="tone-sky" />
              <Badge text={row.stateLabel || '-'} className={TONE_CLASS[row.stateTone] || TONE_CLASS.slate} />
            </div>
          </div>

          <button
            type="button"
            ref={closeButtonRef}
            aria-label="关闭抽屉"
            onClick={onClose}
            className="rounded-xl border border-line bg-surface p-2 text-fg-subtle transition-colors hover:text-fg-body"
          >
            <X size={18} />
          </button>
        </div>

        <div className="flex-1 space-y-4 overflow-y-auto px-5 py-5">
          <SectionCard icon={Gauge} title="额度">
            <QuotaBar label="5h 额度" text={row.quota5hText} percent={row.quota5hPercent} />
            <QuotaBar label="d7 额度" text={row.quota7dText} percent={row.quota7dPercent} />
            <DetailRow label="额度状态" value={detail.quotaStatusLabel || detail.quotaStatus || '-'} />
            <DetailRow label="最近刷新" value={row.refreshedAtText} />
            {detail.quota5hResetText ? <DetailRow label="5h 重置" value={detail.quota5hResetText} /> : null}
            {detail.quota7dResetText ? <DetailRow label="d7 重置" value={detail.quota7dResetText} /> : null}
            {!detail.quota5hResetText && !detail.quota7dResetText ? (
              <DetailRow label="下次重置" value={detail.nextResetText || detail.quotaResetText || '-'} />
            ) : null}
          </SectionCard>

          <SectionCard icon={Shield} title="健康">
            <DetailRow label="最近成功" value={row.lastSuccessText} />
            <DetailRow label="连通性" value={detail.healthLabel || detail.reachabilityLabel || '-'} />
            <DetailRow label="风险摘要" value={detail.riskLabel || '-'} />
            <AccountErrorDetails display={errorDisplay} />
            <DetailRow label="观察备注" value={detail.healthNote || detail.note || '-'} />
          </SectionCard>

          <SectionCard icon={Activity} title="路由配置">
            <DetailRow label="路由组别" value={row.groupLabel} />
            <DetailRow label="组内顺序" value={detail.priorityLabel || detail.priority || '-'} />
            <DetailRow label="组内角色" value={detail.groupOrderLabel || detail.groupOrder || '-'} />
            <DetailRow label="Base URL" value={detail.baseUrl || detail.baseURL || '-'} />
            <DetailRow
              label="模型兼容"
              value={modelRewriteDetailText}
            />
            <DetailRow label="路由备注" value={detail.routingNote || detail.routeNote || '-'} />
          </SectionCard>

          <SectionCard icon={Settings} title="动作">
            <div className="grid grid-cols-3 gap-2">
              {account.state === 'cooldown' && <Button variant="secondary" size="sm" disabled={actionBusy} onClick={() => onClearAccountCooldown?.(account)}>解除冷却</Button>}
              <Button
                variant={enabled ? 'secondary' : 'primary'}
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
            <div className="mt-3 border-t border-line-soft pt-3">
              <Button variant="danger" size="sm" className="w-full" disabled={actionBusy} onClick={() => onDeleteAccount?.(account)}>
                删除账号
              </Button>
            </div>
          </SectionCard>
        </div>
      </aside>
    </div>,
    document.body
  );
};

export default AccountDetailsDrawer;
