// AccountPoolSwitcher - Codex 账号池快捷切换器
// 2026-10-01：嵌入请求页面板的全局账号池策略

import { useMemo, useState } from 'react';
import { AlertCircle, Check, Search } from 'lucide-react';
import AutoScheduleRow from './route-switcher/AutoScheduleRow.jsx';
import { canPinAccountSelection } from '../utils/accountSwitcherState.js';
import {
  normalizePriorityValue,
  resolveAccountId,
  toAccountAuthLabel,
  toPlanTypeLabel,
  toQuotaStatusLabel
} from '@pages/account-pool/utils.js';

const SEARCH_THRESHOLD = 8;

const getAccountName = (account = {}) => account.account_name || account.accountName || '-';
const isSameAccountId = (left, right) => String(left ?? '') === String(right ?? '');
const isSwitchableAccount = (account = {}) => canPinAccountSelection(account);

const normalizeGroupKey = (value = '') => {
  const normalized = String(value || '').trim().toLowerCase();
  if (normalized === 'primary' || normalized === '主组') return 'primary';
  if (normalized === 'backup' || normalized === '备组') return 'backup';
  if (normalized === 'cold' || normalized === '冷备') return 'cold';
  return '';
};

const inferGroupKey = (account = {}) => {
  const explicitGroupKey = normalizeGroupKey(account.group_key || account.groupKey);
  if (explicitGroupKey) return explicitGroupKey;
  const priority = normalizePriorityValue(account.priority ?? account.Priority);
  if (!Number.isFinite(priority)) return 'primary';
  if (priority <= 10) return 'primary';
  if (priority <= 20) return 'backup';
  return 'cold';
};

const GROUP_META = {
  primary: { label: '主组', rank: 0 },
  backup: { label: '备组', rank: 1 },
  cold: { label: '冷备', rank: 2 }
};

const getGroupMeta = (account = {}) => {
  const key = inferGroupKey(account);
  return { key, ...(GROUP_META[key] || GROUP_META.primary) };
};

const compareAccountsByGroupOrder = (left = {}, right = {}) => {
  const leftGroup = getGroupMeta(left);
  const rightGroup = getGroupMeta(right);
  if (leftGroup.rank !== rightGroup.rank) return leftGroup.rank - rightGroup.rank;
  const leftPriority = normalizePriorityValue(left.priority ?? left.Priority);
  const rightPriority = normalizePriorityValue(right.priority ?? right.Priority);
  if (Number.isFinite(leftPriority) && Number.isFinite(rightPriority) && leftPriority !== rightPriority) return leftPriority - rightPriority;
  if (Number.isFinite(leftPriority) && !Number.isFinite(rightPriority)) return -1;
  if (!Number.isFinite(leftPriority) && Number.isFinite(rightPriority)) return 1;
  return String(resolveAccountId(left) ?? '').localeCompare(String(resolveAccountId(right) ?? ''));
};

const getIndicatorClass = (account = {}) => {
  const state = String(account.state || '').trim().toLowerCase();
  if (account.enabled === false || state === 'disabled_auth') return 'bg-danger-solid';
  if (state === 'cooldown') return 'bg-warn-solid';
  return 'bg-success-solid';
};

const AccountPoolSwitcher = ({
  accounts = [],
  activeAccount = null,
  recentSelectedAccountId = null,
  onSwitch,
  onSwitchAuto,
  loading = false
}) => {
  const [searchTerm, setSearchTerm] = useState('');
  const sortedAccounts = useMemo(() => [...accounts].sort(compareAccountsByGroupOrder), [accounts]);
  const activeAccountId = resolveAccountId(activeAccount);
  const isAutoMode = activeAccountId === null || activeAccountId === undefined;
  const showSearch = sortedAccounts.length > SEARCH_THRESHOLD;
  const filteredAccounts = useMemo(() => {
    const keyword = searchTerm.trim().toLowerCase();
    if (!keyword) return sortedAccounts;
    return sortedAccounts.filter((account) => {
      const accountName = getAccountName(account).toLowerCase();
      const authLabel = toAccountAuthLabel(account.provider_type || account.providerType || '').toLowerCase();
      const planLabel = toPlanTypeLabel(account.plan_type || account.planType || '').toLowerCase();
      const groupLabel = getGroupMeta(account).label.toLowerCase();
      return accountName.includes(keyword) || authLabel.includes(keyword) || planLabel.includes(keyword) || groupLabel.includes(keyword);
    });
  }, [searchTerm, sortedAccounts]);

  const handleAccountSelect = async (account) => {
    const accountId = resolveAccountId(account);
    if (loading || accountId === null || accountId === undefined || isSameAccountId(accountId, activeAccountId) || !isSwitchableAccount(account)) return;
    try { await onSwitch?.(account); } catch (error) { console.error('切换账号失败:', error); }
  };

  const handleAutoSelect = async () => {
    if (loading || isAutoMode) return;
    try { await onSwitchAuto?.(); } catch (error) { console.error('启用编排失败:', error); }
  };

  if (!sortedAccounts.length) return <div className="flex items-center gap-2 px-4 py-6 text-sm text-fg-subtle"><AlertCircle className="h-4 w-4" />无可用 Codex 账号</div>;

  const groupedAccounts = filteredAccounts.reduce((groups, account) => {
    const key = getGroupMeta(account).key;
    const list = groups.get(key) || [];
    list.push(account);
    groups.set(key, list);
    return groups;
  }, new Map());

  return (
    <div className="min-w-0">
      {showSearch && <div className="border-b border-line-soft px-3 py-2"><div className="flex items-center gap-2 rounded-lg border border-line bg-surface px-3 py-2 focus-within:border-accent-line focus-within:ring-2 focus-within:ring-accent-soft"><Search className="h-3.5 w-3.5 text-fg-subtle" /><input type="text" value={searchTerm} onChange={(event) => setSearchTerm(event.target.value)} placeholder="搜索账号名称 / 认证方式 / 套餐" className="w-full bg-transparent text-sm text-fg-body placeholder:text-fg-subtle outline-none" /></div></div>}
      <div className="max-h-[380px] overflow-y-auto p-2">
        <AutoScheduleRow isCurrent={isAutoMode} onRestore={handleAutoSelect} loading={loading} label="Auto / 按编排" />
        {filteredAccounts.length === 0 && <div className="px-3 py-8 text-center text-sm text-fg-subtle">没有匹配的账号</div>}
        {[...groupedAccounts.entries()].map(([groupKey, groupAccounts]) => (
          <section key={groupKey} className="mb-2 last:mb-0">
            <h3 className="px-2 pb-1 pt-2 text-[11px] font-semibold text-fg-muted">{GROUP_META[groupKey]?.label || groupKey}</h3>
            {groupAccounts.map((account) => {
              const accountId = resolveAccountId(account);
              const isActive = isSameAccountId(accountId, activeAccountId);
              const isRecentSelected = recentSelectedAccountId !== null && recentSelectedAccountId !== undefined && isSameAccountId(accountId, recentSelectedAccountId);
              const switchable = isSwitchableAccount(account);
              const priority = normalizePriorityValue(account.priority ?? account.Priority);
              const accountName = getAccountName(account);
              const authLabel = toAccountAuthLabel(account.provider_type || account.providerType || '');
              const planLabel = toPlanTypeLabel(account.plan_type || account.planType || '');
              const quotaLabel = toQuotaStatusLabel(account.quota_status || account.quotaStatus || '');
              const state = String(account.state || '').trim().toLowerCase();
              return (
                <button
                  key={String(accountId ?? accountName)}
                  type="button"
                  onClick={() => handleAccountSelect(account)}
                  disabled={loading || !switchable}
                  title={`${authLabel}${!switchable ? '；当前不可切换' : ''}`}
                  className={`relative flex w-full items-start justify-between gap-3 rounded-lg px-3 py-2 text-left text-sm transition-colors ${isActive ? 'bg-surface-sub' : 'hover:bg-surface-sub'} ${(!switchable || loading) ? 'cursor-not-allowed opacity-60' : ''}`}
                >
                  {isActive && <span className="absolute inset-y-1 left-0 w-[3px] rounded-r bg-accent" />}
                  <div className="flex min-w-0 flex-1 items-start gap-2">
                    <span className={`mt-1.5 h-2 w-2 shrink-0 rounded-full ${getIndicatorClass(account)}`} />
                    <div className="min-w-0 flex-1">
                      <div className="flex min-w-0 items-center gap-2"><span className={`truncate font-medium ${isActive ? 'text-fg' : 'text-fg-body'}`} title={accountName}>{accountName}</span>{planLabel && <span className="shrink-0 text-[10px] text-fg-muted">{planLabel}</span>}</div>
                      <div className="mt-0.5 truncate text-[10px] text-fg-muted">{Number.isFinite(priority) ? `组内顺序 ${priority}` : '组内顺序未知'} · {quotaLabel}{account.enabled === false ? ' · 已停用' : ''}{state === 'disabled_auth' ? ' · 鉴权失效' : ''}</div>
                    </div>
                  </div>
                  <div className="flex shrink-0 items-center gap-2 pt-0.5">{isRecentSelected && !isActive && <span className="tone-emerald rounded px-1.5 py-0.5 text-[10px]">最近命中</span>}{!switchable && !isActive && <span className="tone-slate rounded px-1.5 py-0.5 text-[10px]">不可切</span>}{isActive && <Check className="h-4 w-4 text-accent" />}</div>
                </button>
              );
            })}
          </section>
        ))}
      </div>
    </div>
  );
};

export default AccountPoolSwitcher;
