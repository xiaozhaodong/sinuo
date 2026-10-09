// AccountPoolSwitcher - Codex 账号池快捷切换器
// 2026-10-01：嵌入请求页面板的全局账号池策略
// 2026-10-07 桌面化：浮层 26px 行，一行一个账号（套餐灰字跟在名字后，右侧只放一个最要紧的状态），
//            组内顺序与额度状态挪到悬停提示；单击账号即固定，单击「自动（按编排）」即恢复按编排自动调度

import { useMemo, useState } from 'react';
import { Search } from 'lucide-react';
import { CheckSlot, PopoverNote, PopoverSection } from '@components/desktop/PopoverParts.jsx';
import AutoScheduleRow from './route-switcher/AutoScheduleRow.jsx';
import useChangeFlash from './route-switcher/useChangeFlash.js';
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

// 右侧状态：只放一个，按严重程度取第一个命中的
const describeAccountStatus = ({ account, switchable, isRecentSelected, isActive }) => {
  const state = String(account.state || '').trim().toLowerCase();
  if (account.enabled === false) return { label: '已停用', tone: 'text-danger' };
  if (state === 'disabled_auth') return { label: '鉴权失效', tone: 'text-danger' };
  if (state === 'cooldown') return { label: '冷却中', tone: 'text-warn' };
  if (!switchable) return { label: '不可切', tone: 'text-fg-subtle' };
  if (isRecentSelected && !isActive) return { label: '最近命中', tone: 'text-fg-subtle' };
  return null;
};

const getIndicatorClass = (account = {}) => {
  const state = String(account.state || '').trim().toLowerCase();
  if (account.enabled === false || state === 'disabled_auth') return 'bg-danger-solid';
  if (state === 'cooldown') return 'bg-warn-solid';
  return 'bg-success-solid';
};

const AccountSearch = ({ searchTerm, setSearchTerm }) => (
  <div className="relative mx-1 mb-1">
    <Search size={13} className="pointer-events-none absolute left-2 top-1/2 -translate-y-1/2 text-fg-subtle" />
    <input
      type="text"
      aria-label="搜索账号"
      value={searchTerm}
      onChange={(event) => setSearchTerm(event.target.value)}
      placeholder="搜索账号名称 / 认证方式 / 套餐"
      className="desk-field w-full pl-7 text-xs placeholder:text-fg-subtle"
    />
  </div>
);

const AccountRow = ({ account, activeAccountId, recentSelectedAccountId, onSwitch, loading, flash }) => {
  const accountId = resolveAccountId(account);
  const isActive = isSameAccountId(accountId, activeAccountId);
  const isRecentSelected = recentSelectedAccountId !== null && recentSelectedAccountId !== undefined && isSameAccountId(accountId, recentSelectedAccountId);
  const switchable = isSwitchableAccount(account);
  const priority = normalizePriorityValue(account.priority ?? account.Priority);
  const accountName = getAccountName(account);
  const authLabel = toAccountAuthLabel(account.provider_type || account.providerType || '');
  const planLabel = toPlanTypeLabel(account.plan_type || account.planType || '');
  const quotaLabel = toQuotaStatusLabel(account.quota_status || account.quotaStatus || '');
  const status = describeAccountStatus({ account, switchable, isRecentSelected, isActive });
  const title = [authLabel, Number.isFinite(priority) ? `组内顺序 ${priority}` : '组内顺序未知', quotaLabel, !switchable ? '当前不可切换' : ''].filter(Boolean).join(' · ');
  const handleSelect = async () => {
    if (loading || accountId === null || accountId === undefined || isActive || !switchable) return;
    try { await onSwitch?.(account); } catch (error) { console.error('切换账号失败:', error); }
  };
  return (
    <button
      type="button"
      aria-pressed={isActive}
      onClick={handleSelect}
      disabled={loading || !switchable}
      title={title}
      className="flex h-[26px] w-full items-center gap-1.5 rounded-[5px] pl-1.5 pr-2 text-left hover:bg-surface-mut disabled:opacity-50"
    >
      <CheckSlot checked={isActive} />
      <span className={`h-[7px] w-[7px] shrink-0 rounded-full ${getIndicatorClass(account)}`} />
      <span className={`min-w-0 flex-1 truncate transition-colors duration-700 ${isActive ? 'font-medium' : ''} ${flash && isActive ? 'text-accent' : isActive ? 'text-fg' : 'text-fg-body'}`}>
        {accountName}
        {planLabel && <span className="ml-1.5 text-[11.5px] font-normal text-fg-subtle">{planLabel}</span>}
      </span>
      {status && <span className={`shrink-0 text-[11px] ${status.tone}`}>{status.label}</span>}
    </button>
  );
};

const AccountGroups = ({ accounts, activeAccountId, recentSelectedAccountId, onSwitch, loading, flashes }) => {
  const grouped = accounts.reduce((groups, account) => {
    const key = getGroupMeta(account).key;
    const list = groups.get(key) || [];
    list.push(account);
    groups.set(key, list);
    return groups;
  }, new Map());
  return (
    <>
      {accounts.length === 0 && <PopoverNote>没有匹配的账号</PopoverNote>}
      {[...grouped.entries()].map(([groupKey, groupAccounts]) => (
        <div key={groupKey}>
          <PopoverSection sub title={GROUP_META[groupKey]?.label || groupKey} />
          {groupAccounts.map((account) => (
            <AccountRow key={String(resolveAccountId(account) ?? getAccountName(account))} account={account} activeAccountId={activeAccountId} recentSelectedAccountId={recentSelectedAccountId} onSwitch={onSwitch} loading={loading} flash={flashes.has(String(resolveAccountId(account)))} />
          ))}
        </div>
      ))}
    </>
  );
};

const AccountPoolSwitcher = ({ accounts = [], activeAccount = null, recentSelectedAccountId = null, onSwitch, onSwitchAuto, loading = false, scopeLabel = '' }) => {
  const [searchTerm, setSearchTerm] = useState('');
  const sortedAccounts = useMemo(() => [...accounts].sort(compareAccountsByGroupOrder), [accounts]);
  const activeAccountId = resolveAccountId(activeAccount);
  const isAutoMode = activeAccountId === null || activeAccountId === undefined;
  const flashes = useChangeFlash([
    ['auto', isAutoMode ? 'auto' : ''],
    ...sortedAccounts.map((account) => [String(resolveAccountId(account)), isSameAccountId(resolveAccountId(account), activeAccountId) ? 'active' : ''])
  ]);
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
  const handleAutoSelect = async () => {
    if (loading || isAutoMode) return;
    try { await onSwitchAuto?.(); } catch (error) { console.error('启用编排失败:', error); }
  };
  const recentAccount = recentSelectedAccountId === null || recentSelectedAccountId === undefined
    ? null : sortedAccounts.find((account) => isSameAccountId(resolveAccountId(account), recentSelectedAccountId));
  return (
    <>
      <PopoverSection title="账号池策略" aside={scopeLabel} />
      {!sortedAccounts.length ? <PopoverNote>无可用 Codex 账号</PopoverNote> : (
        <>
          {sortedAccounts.length > SEARCH_THRESHOLD && <AccountSearch searchTerm={searchTerm} setSearchTerm={setSearchTerm} />}
          <AutoScheduleRow
            isCurrent={isAutoMode}
            flash={isAutoMode && flashes.has('auto')}
            onRestore={handleAutoSelect}
            loading={loading}
            label="自动（按编排）"
            title="按编排自动调度，不固定账号"
            meta={isAutoMode && recentAccount ? `最近 ${getAccountName(recentAccount)}` : ''}
          />
          <AccountGroups accounts={filteredAccounts} activeAccountId={activeAccountId} recentSelectedAccountId={recentSelectedAccountId} onSwitch={onSwitch} loading={loading} flashes={flashes} />
        </>
      )}
    </>
  );
};

export default AccountPoolSwitcher;
