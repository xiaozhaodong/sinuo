// ============================================
// Account Pool Dashboard View-Model
// 2026-03-21
// ============================================

import {
  isAccountSchedulable,
  isAPIKeyProviderType,
  normalizePlanType,
  normalizeEntityId,
  toAccountAuthLabel,
  toAccountStateLabel,
  toPlanTypeLabel,
  toQuotaStatusLabel,
  toRemainingPercent
} from './accountPool.js';
import { buildAccountErrorDisplay } from './accountErrorDisplay.js';
import { formatTimestamp as formatConfiguredTimestamp } from '../../../utils/timezone.js';

const COLD_STANDBY_GROUP_LABEL = '冷备';
const UNKNOWN_TEXT = '-';
const STALE_REFRESH_HOURS = 72;
const STALE_SUCCESS_HOURS = 72;
const QUOTA_RISK_THRESHOLD = 20;

const DEFAULT_FILTERS = {
  auth: 'all',
  plan: 'all',
  group: 'all',
  status: 'all',
  risk: 'all',
  sort: 'recent_success_desc',
  savedView: 'all'
};

const DEFAULT_SAVED_VIEWS = [
  { key: 'all', label: '全部账号' },
  { key: 'primary-issues', label: '主组异常' },
  { key: 'oauth-pending', label: '待处理 OAuth' },
  { key: 'quota-risk', label: '额度风险' },
  { key: 'cold-free', label: '冷备 free 账号' }
];

const DEFAULT_BATCH_ACTIONS = [
  { key: 'test', label: '批量测试' },
  { key: 'refresh-profile', label: '批量刷新画像' },
  { key: 'toggle-enabled', label: '批量启用 / 停用' },
  { key: 'move-backup', label: '批量移到备组' },
  { key: 'move-cold-standby', label: '批量移到冷备' }
];

const SEVERITY_WEIGHT = {
  P1: 3,
  P2: 2,
  P3: 1
};

const getSeverityScore = (value) => SEVERITY_WEIGHT[value] || 0;

const toneByState = {
  可用: 'emerald',
  冷却中: 'amber',
  鉴权失效: 'rose',
  已禁用: 'slate'
};

const normalizeFilters = (filters = {}) => ({ ...DEFAULT_FILTERS, ...(filters || {}) });

const normalizeText = (value = '') => String(value || '').trim().toLowerCase();

const resolveAccountStateKey = (account = {}) => {
  if (account?.enabled === false) {
    return 'disabled';
  }
  return normalizeText(account?.state) || 'active';
};

const formatTimestampText = (value, timezone) => formatConfiguredTimestamp(value, timezone);

const toDate = (value) => {
  if (!value) return null;
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? null : date;
};

const hoursSince = (value, now) => {
  const date = toDate(value);
  if (!date) return Number.POSITIVE_INFINITY;
  return Math.max(0, (now.getTime() - date.getTime()) / (1000 * 60 * 60));
};

const getPriorityValue = (account = {}) => {
  const value = normalizeEntityId(account.priority ?? account.Priority);
  return Number.isFinite(value) ? Number(value) : Number.POSITIVE_INFINITY;
};

const getSortedPriorities = (accounts = []) => (
  Array.from(new Set(accounts.map((item) => getPriorityValue(item)).filter((value) => Number.isFinite(value)))).sort((left, right) => left - right)
);

const normalizeGroupKey = (value = '') => {
  const normalized = normalizeText(value);
  if (normalized === 'primary' || normalized.includes('main') || normalized.includes('主组')) return 'primary';
  if (normalized === 'backup' || normalized.includes('secondary') || normalized.includes('备组')) return 'backup';
  if (normalized === 'cold' || normalized.includes('cold-standby') || normalized.includes('冷备')) return 'cold';
  return '';
};

const getGroupMetaByPriority = (priority, sortedPriorities = []) => {
  const index = sortedPriorities.indexOf(priority);
  if (index === 0) return { key: 'primary', label: '主组', rank: 1 };
  if (index === 1) return { key: 'backup', label: '备组', rank: 2 };
  return { key: 'cold', label: COLD_STANDBY_GROUP_LABEL, rank: 3 };
};

const getGroupMeta = (account = {}, sortedPriorities = []) => {
  const explicitGroupKey = normalizeGroupKey(account.group_key ?? account.groupKey);
  if (explicitGroupKey === 'primary') return { key: 'primary', label: '主组', rank: 1 };
  if (explicitGroupKey === 'backup') return { key: 'backup', label: '备组', rank: 2 };
  if (explicitGroupKey === 'cold') return { key: 'cold', label: COLD_STANDBY_GROUP_LABEL, rank: 3 };
  return getGroupMetaByPriority(getPriorityValue(account), sortedPriorities);
};

const getQuotaRemaining = (account = {}) => {
  const quota5hRemaining = toRemainingPercent(account.quota_5h_used_percent ?? account.quota5hUsedPercent);
  const quota7dRemaining = toRemainingPercent(account.quota_weekly_used_percent ?? account.quotaWeeklyUsedPercent);

  return {
    quota5hRemaining,
    quota7dRemaining
  };
};

const getQuotaText = (account = {}, key = '5h') => {
  const providerType = normalizeText(account.provider_type ?? account.providerType);
  const planType = normalizePlanType(account.plan_type ?? account.planType);
  const { quota5hRemaining, quota7dRemaining } = getQuotaRemaining(account);

  if (providerType === 'api_key') {
    return '无限额';
  }

  if (key === '5h' && planType === 'free') {
    return '无';
  }

  const remaining = key === '5h' ? quota5hRemaining : quota7dRemaining;
  if (!Number.isFinite(remaining)) {
    return '未刷新';
  }

  return `${remaining.toFixed(0)}%`;
};

const getQuotaResetTexts = (account = {}, timezone) => {
  const quota5hResetAt = account.quota_5h_reset_at ?? account.quota5hResetAt;
  const quota7dResetAt = account.quota_weekly_reset_at ?? account.quotaWeeklyResetAt;
  const providerType = normalizeText(account.provider_type ?? account.providerType);
  const planType = normalizePlanType(account.plan_type ?? account.planType);

  if (providerType === 'api_key') {
    return {
      nextResetText: '无限额',
      quota5hResetText: '',
      quota7dResetText: ''
    };
  }

  if (planType === 'free') {
    const quota7dResetText = quota7dResetAt ? formatTimestampText(quota7dResetAt, timezone) : '未设置';
    return {
      nextResetText: quota7dResetText,
      quota5hResetText: '',
      quota7dResetText
    };
  }

  const quota5hResetText = quota5hResetAt ? formatTimestampText(quota5hResetAt, timezone) : '';
  const quota7dResetText = quota7dResetAt ? formatTimestampText(quota7dResetAt, timezone) : '';
  return {
    nextResetText: quota5hResetText || quota7dResetText || '未设置',
    quota5hResetText,
    quota7dResetText
  };
};

const getHealthLabel = (account = {}, riskLevel = 'healthy') => {
  const state = normalizeText(account.state);
  const quotaStatus = normalizeText(account.quota_status ?? account.quotaStatus);

  if (state === 'disabled_auth' || quotaStatus === 'auth_invalid') {
    return '异常';
  }
  if (riskLevel === 'P1' || riskLevel === 'P2') {
    return '待观察';
  }
  return '正常';
};

const getRiskLabel = (account = {}, riskLevel = 'healthy') => {
  if (riskLevel === 'P1') return '高风险';
  if (riskLevel === 'P2') return '中风险';
  if (riskLevel === 'P3') return '保养提醒';

  const lastError = String(account.last_error ?? account.lastError ?? '').trim();
  return lastError || '当前未发现显著风险';
};

const getGroupOrderLabel = (account = {}) => {
  if ((account.is_active_selection ?? account.isActiveSelection) === true) {
    return '当前活跃账号';
  }
  return '组内候选';
};

const getQueueEntries = (account = {}, groupMeta, _latestScheduleSnapshot = {}, now = new Date()) => {
  const normalizedState = normalizeText(account.state);
  const quotaStatus = normalizeText(account.quota_status ?? account.quotaStatus);
  const failCount = Number.parseInt(account.fail_count ?? account.failCount, 10) || 0;
  const lastError = String(account.last_error ?? account.lastError ?? '').trim();
  const lastSuccessHours = hoursSince(account.last_success_at ?? account.lastSuccessAt, now);
  const refreshedHours = hoursSince(account.quota_refreshed_at ?? account.quotaRefreshedAt, now);
  const { quota5hRemaining, quota7dRemaining } = getQuotaRemaining(account);
  const entries = [];
  const impactsScheduling = groupMeta.rank === 1;

  if (normalizedState === 'disabled_auth' || quotaStatus === 'auth_invalid') {
    entries.push({
      type: 'firefight',
      queueKey: 'auth-invalid',
      queueLabel: '鉴权失效',
      severity: impactsScheduling ? 'P1' : 'P2',
      impactsScheduling,
      reason: lastError || '账号鉴权失效，需要重新处理授权信息'
    });
  }

  if (impactsScheduling && !isAccountSchedulable(account)) {
    entries.push({
      type: 'firefight',
      queueKey: 'primary-risk',
      queueLabel: '主组风险',
      severity: 'P1',
      impactsScheduling: true,
      reason: '主组账号当前不可调度'
    });
  }

  if ((Number.isFinite(quota5hRemaining) && quota5hRemaining <= QUOTA_RISK_THRESHOLD)
    || (Number.isFinite(quota7dRemaining) && quota7dRemaining <= QUOTA_RISK_THRESHOLD)) {
    entries.push({
      type: 'firefight',
      queueKey: 'quota-risk',
      queueLabel: '额度将尽',
      severity: impactsScheduling ? 'P1' : 'P2',
      impactsScheduling,
      reason: `账号剩余额度偏低（5h ${getQuotaText(account, '5h')} / d7 ${getQuotaText(account, '7d')}）`
    });
  }

  if (quotaStatus === 'exhausted') {
    entries.push({
      type: 'firefight',
      queueKey: 'quota-exhausted',
      queueLabel: '已耗尽待重置',
      severity: impactsScheduling ? 'P1' : 'P2',
      impactsScheduling,
      reason: '额度窗口已耗尽，等待后续刷新或重置'
    });
  }

  if (failCount > 0 || lastError) {
    entries.push({
      type: 'maintenance',
      queueKey: 'recent-test-failure',
      queueLabel: '最近测试失败',
      severity: 'P3',
      impactsScheduling: false,
      reason: lastError || `累计失败 ${failCount} 次`
    });
  }

  if (lastSuccessHours >= STALE_SUCCESS_HOURS) {
    entries.push({
      type: 'maintenance',
      queueKey: 'stale-success',
      queueLabel: '长时间未成功',
      severity: 'P3',
      impactsScheduling: false,
      reason: Number.isFinite(lastSuccessHours)
        ? `最近成功距今约 ${Math.floor(lastSuccessHours)} 小时`
        : '还没有成功记录'
    });
  }

  if (refreshedHours >= STALE_REFRESH_HOURS) {
    entries.push({
      type: 'maintenance',
      queueKey: 'stale-refresh',
      queueLabel: '长时间未刷新',
      severity: 'P3',
      impactsScheduling: false,
      reason: Number.isFinite(refreshedHours)
        ? `画像或额度距今约 ${Math.floor(refreshedHours)} 小时未刷新`
        : '还没有刷新记录'
    });
  }

  return entries;
};

const buildFilterOptions = (accounts, sortedPriorities) => {
  const authOptions = [{ value: 'all', label: '全部授权' }];
  const planOptions = [{ value: 'all', label: '全部计划' }];
  const groupOptions = [{ value: 'all', label: '全部组别' }];
  const statusOptions = [{ value: 'all', label: '全部状态' }];
  const riskOptions = [{ value: 'all', label: '全部风险' }];
  // 第一项即页面默认排序
  const sortOptions = [
    { value: 'recent_success_desc', label: '最近成功' },
    { value: 'risk_desc', label: '风险优先' },
    { value: 'group_asc', label: '组别优先' },
    { value: 'name_asc', label: '账号名' }
  ];

  const authSeen = new Set();
  const planSeen = new Set();
  const groupSeen = new Set();
  const statusSeen = new Set();

  accounts.forEach((account) => {
    const authKey = normalizeText(account.provider_type ?? account.providerType) || 'unknown';
    const authLabel = toAccountAuthLabel(account.provider_type ?? account.providerType);
    if (!authSeen.has(authKey)) {
      authSeen.add(authKey);
      authOptions.push({ value: authKey, label: authLabel });
    }

    const rawPlanKey = normalizePlanType(account.plan_type ?? account.planType);
    const rawPlanLbl = toPlanTypeLabel(account.plan_type ?? account.planType);
    const isApiKeyAcct = isAPIKeyProviderType(account.provider_type ?? account.providerType);
    const planKey = (rawPlanKey && rawPlanKey !== 'unknown') ? rawPlanKey : (isApiKeyAcct ? 'prepaid' : 'unknown');
    const planLabel = (rawPlanLbl && rawPlanLbl !== 'Unknown') ? rawPlanLbl : (isApiKeyAcct ? 'Prepaid' : 'Unknown');
    if (!planSeen.has(planKey)) {
      planSeen.add(planKey);
      planOptions.push({ value: planKey, label: planLabel });
    }

    const groupMeta = getGroupMeta(account, sortedPriorities);
    if (!groupSeen.has(groupMeta.key)) {
      groupSeen.add(groupMeta.key);
      groupOptions.push({ value: groupMeta.key, label: groupMeta.label });
    }

    const statusKey = resolveAccountStateKey(account);
    const statusLabel = toAccountStateLabel(statusKey);
    if (!statusSeen.has(statusKey)) {
      statusSeen.add(statusKey);
      statusOptions.push({ value: statusKey, label: statusLabel });
    }
  });

  riskOptions.push(
    { value: 'healthy', label: '健康' },
    { value: 'P1', label: 'P1' },
    { value: 'P2', label: 'P2' },
    { value: 'P3', label: 'P3' }
  );

  return {
    authOptions,
    planOptions,
    groupOptions,
    statusOptions,
    riskOptions,
    sortOptions
  };
};

const buildSchedulerActions = (groupKey, hasActiveAccount = false) => {
  const actions = [];

  if (groupKey === 'primary') {
    actions.push({
      key: 'swap-down',
      label: '下放到备组',
      targetGroupKey: 'backup',
      disabled: !hasActiveAccount,
      tone: 'secondary'
    });
  } else if (groupKey === 'backup') {
    actions.push({
      key: 'swap-up',
      label: '提升到主组',
      targetGroupKey: 'primary',
      disabled: !hasActiveAccount,
      tone: 'primary'
    });
    actions.push({
      key: 'swap-down',
      label: '下放到冷备',
      targetGroupKey: 'cold',
      disabled: !hasActiveAccount,
      tone: 'secondary'
    });
  } else {
    actions.push({
      key: 'swap-up',
      label: '提升到备组',
      targetGroupKey: 'backup',
      disabled: !hasActiveAccount,
      tone: 'secondary'
    });
  }

  actions.push(
    {
      key: 'reorder',
      label: '组内顺序调整',
      disabled: true,
      soon: true,
      tone: 'ghost'
    },
    {
      key: 'set-active',
      label: '指定当前活跃账号',
      disabled: !hasActiveAccount,
      soon: !hasActiveAccount,
      tone: 'ghost'
    }
  );

  return actions;
};

const buildInventoryRows = (accounts, sortedPriorities, latestScheduleSnapshot, now, timezone) => accounts.map((account) => {
  const priority = getPriorityValue(account);
  const groupMeta = getGroupMeta(account, sortedPriorities);
  const stateKey = resolveAccountStateKey(account);
  const stateLabel = toAccountStateLabel(stateKey);
  const authLabel = toAccountAuthLabel(account.provider_type ?? account.providerType);
  const rawPlanKey = normalizePlanType(account.plan_type ?? account.planType);
  const rawPlanLabel = toPlanTypeLabel(account.plan_type ?? account.planType);
  const isApiKey = isAPIKeyProviderType(account.provider_type ?? account.providerType);
  const planKey = (rawPlanKey && rawPlanKey !== 'unknown') ? rawPlanKey : (isApiKey ? 'prepaid' : 'unknown');
  const planLabel = (rawPlanLabel && rawPlanLabel !== 'Unknown') ? rawPlanLabel : (isApiKey ? 'Prepaid' : 'Unknown');
  const providerType = account.provider_type ?? account.providerType;
  const quotaStatusLabel = toQuotaStatusLabel(account.quota_status ?? account.quotaStatus);
  const queueEntries = getQueueEntries(account, groupMeta, latestScheduleSnapshot, now);
  const riskLevel = queueEntries.reduce((highest, entry) => (
    getSeverityScore(entry.severity) > getSeverityScore(highest) ? entry.severity : highest
  ), 'healthy');
  const accountId = normalizeEntityId(account.id ?? account.account_id ?? account.accountId);
  const lastSuccessAt = account.last_success_at ?? account.lastSuccessAt;
  const refreshedAt = account.quota_refreshed_at ?? account.quotaRefreshedAt;
  const {
    nextResetText,
    quota5hResetText,
    quota7dResetText
  } = getQuotaResetTexts(account, timezone);
  const healthLabel = getHealthLabel(account, riskLevel);
  const riskLabel = getRiskLabel(account, riskLevel);
  const rawLastError = String(account.last_error ?? account.lastError ?? '').trim();
  const errorDisplay = buildAccountErrorDisplay(rawLastError);
  const lastErrorText = errorDisplay?.message || '暂无错误记录';
  const { quota5hRemaining, quota7dRemaining } = getQuotaRemaining(account);

  return {
    id: accountId,
    raw: account,
    name: account.account_name || account.accountName || `账号 ${accountId ?? UNKNOWN_TEXT}`,
    authKey: normalizeText(account.provider_type ?? account.providerType) || 'unknown',
    authLabel,
    planKey,
    planLabel,
    groupKey: groupMeta.key,
    groupLabel: groupMeta.label,
    groupRank: groupMeta.rank,
    priority,
    stateKey,
    stateLabel,
    stateTone: toneByState[stateLabel] || 'slate',
    quotaStatusKey: normalizeText(account.quota_status ?? account.quotaStatus) || 'pending',
    quotaStatusLabel,
    quota5hText: getQuotaText(account, '5h'),
    quota7dText: getQuotaText(account, '7d'),
    quota5hPercent: quota5hRemaining,
    quota7dPercent: quota7dRemaining,
    lastSuccessText: formatTimestampText(lastSuccessAt, timezone),
    refreshedAtText: formatTimestampText(refreshedAt, timezone),
    lastSuccessAtMs: toDate(lastSuccessAt)?.getTime() || 0,
    refreshedAtMs: toDate(refreshedAt)?.getTime() || 0,
    isApiKey,
    providerType,
    quota5hResetAt: account.quota_5h_reset_at ?? account.quota5hResetAt,
    quota7dResetAt: account.quota_weekly_reset_at ?? account.quotaWeeklyResetAt,
    riskLevel,
    queueKeys: queueEntries.map((item) => item.queueKey),
    queueEntries,
    lastError: rawLastError,
    errorDisplay,
    baseURL: account.base_url || account.baseUrl || '',
    enabled: account.enabled !== false,
    detail: {
      rawAccount: account,
      authLabel,
      planLabel,
      groupLabel: groupMeta.label,
      groupKey: groupMeta.key,
      stateLabel,
      quotaStatusLabel,
      quota5hText: getQuotaText(account, '5h'),
      quota7dText: getQuotaText(account, '7d'),
      lastSuccessText: formatTimestampText(lastSuccessAt, timezone),
      lastSuccessAt,
      refreshedAtText: formatTimestampText(refreshedAt, timezone),
      refreshedAt,
      lastError: lastErrorText,
      lastErrorText,
      errorDisplay,
      baseURL: account.base_url || account.baseUrl || UNKNOWN_TEXT,
      baseUrl: account.base_url || account.baseUrl || UNKNOWN_TEXT,
      priority: Number.isFinite(priority) ? priority : UNKNOWN_TEXT,
      priorityLabel: Number.isFinite(priority) ? String(priority) : UNKNOWN_TEXT,
      nextResetText,
      quota5hResetText,
      quota7dResetText,
      quotaResetText: nextResetText,
      healthLabel,
      riskLabel,
      healthNote: Number.parseInt(account.fail_count ?? account.failCount, 10) > 0
        ? `累计失败 ${Number.parseInt(account.fail_count ?? account.failCount, 10)} 次`
        : '最近状态平稳',
      groupOrderLabel: getGroupOrderLabel(account),
      routingNote: '系统先按组别（主组 -> 备组 -> 冷备）选择，再在组内按顺序、额度和健康度择优',
      actionCountText: `影响范围 1 个账号`
    }
  };
});

const buildSchedulerGroups = (rows) => {
  const groups = [
    { key: 'primary', label: '主组', rank: 1, accounts: [] },
    { key: 'backup', label: '备组', rank: 2, accounts: [] },
    { key: 'cold', label: COLD_STANDBY_GROUP_LABEL, rank: 3, accounts: [] }
  ];

  rows.forEach((row) => {
    const target = groups.find((item) => item.key === row.groupKey) || groups[2];
    target.accounts.push({
      id: row.id,
      rawAccount: row.raw,
      account_name: row.name,
      name: row.name,
      planLabel: row.planLabel,
      lastSuccessAt: row.raw?.last_success_at ?? row.raw?.lastSuccessAt,
      stateLabel: row.stateLabel,
      stateTone: row.stateTone,
      enabled: row.enabled,
      quota5hText: row.quota5hText,
      quota5hPercent: row.quota5hPercent,
      quota7dText: row.quota7dText,
      quota7dPercent: row.quota7dPercent,
      riskLevel: row.riskLevel,
      isActive: (row.raw?.is_active_selection ?? row.raw?.isActiveSelection) === true,
      isGroupPreferred: (row.raw?.is_group_preferred ?? row.raw?.isGroupPreferred) === true,
      isAvailable: isAccountSchedulable(row.raw),
      summary: `${row.stateLabel} · 5h ${row.quota5hText} · d7 ${row.quota7dText}`
    });
  });

  return groups.map((group) => ({
    ...group,
    actions: buildSchedulerActions(group.key, group.accounts.length > 0),
    healthSummary: group.accounts.length === 0
      ? '暂无账号'
      : `${group.accounts.filter((item) => item.isAvailable).length}/${group.accounts.length} 可用`
  }));
};

const buildSchedulerSummary = (snapshot = {}, rows = [], timezone) => {
  const hasSnapshot = snapshot?.hasSnapshot === true || snapshot?.has_snapshot === true;
  const pinnedAccount = (Array.isArray(rows) ? rows : []).find((row) => (row?.raw?.is_active_selection ?? row?.raw?.isActiveSelection) === true) || null;
  return {
    hasSnapshot,
    selectedAccountId: snapshot?.selectedAccountId ?? snapshot?.selected_account_id ?? null,
    currentGroupLabel: snapshot?.selectedTierLabel || snapshot?.selected_tier_label || '暂无调度',
    activeAccountName: snapshot?.selectedAccountName || snapshot?.selected_account_name || '暂无命中账号',
    degraded: Boolean(snapshot?.degradedToLowerPriority || snapshot?.degraded_to_lower_priority),
    updatedAtText: formatTimestampText(snapshot?.updatedAt || snapshot?.updated_at || snapshot?.capturedAt || snapshot?.captured_at, timezone),
    finalOutcomeLabel: snapshot?.finalOutcome || snapshot?.final_outcome || 'pending',
    selectionMode: pinnedAccount ? 'manual' : 'auto',
    selectionModeLabel: pinnedAccount ? '手动模式' : 'Auto 模式',
    pinnedAccountId: pinnedAccount?.id ?? null,
    pinnedAccountName: pinnedAccount?.name || ''
  };
};

const buildSelectionState = (rows, selectedIds = []) => {
  const rowIds = new Set(rows.map((row) => row.id));
  const activeSelectedIds = selectedIds.filter((id) => rowIds.has(id));
  return {
    selectedIds: activeSelectedIds,
    selectedCount: activeSelectedIds.length,
    singleSelectedAccountId: activeSelectedIds.length === 1 ? activeSelectedIds[0] : null
  };
};

const buildAccountPoolDashboardModel = ({
  accounts = [],
  latestScheduleSnapshot = null,
  searchTerm = '',
  filters = {},
  selectedIds = [],
  timezone
} = {}) => {
  if (!timezone) throw new Error('buildAccountPoolDashboardModel requires an injected timezone');
  const normalizedFilters = normalizeFilters(filters);
  const now = new Date();
  const sortedPriorities = getSortedPriorities(accounts);
  const allRows = buildInventoryRows(accounts, sortedPriorities, latestScheduleSnapshot, now, timezone);
  const schedulerSummary = buildSchedulerSummary(latestScheduleSnapshot, allRows, timezone);

  return {
    inventory: {
      rows: allRows,
      savedViews: DEFAULT_SAVED_VIEWS,
      batchActions: DEFAULT_BATCH_ACTIONS,
      filters: {
        ...normalizedFilters,
        searchTerm,
        ...buildFilterOptions(accounts, sortedPriorities)
      },
      selection: buildSelectionState(allRows, selectedIds)
    },
    scheduler: {
      summary: schedulerSummary,
      groups: buildSchedulerGroups(allRows),
      latestSnapshot: latestScheduleSnapshot || { hasSnapshot: false, candidates: [] }
    }
  };
};

export {
  buildAccountPoolDashboardModel,
  COLD_STANDBY_GROUP_LABEL,
  DEFAULT_BATCH_ACTIONS,
  DEFAULT_FILTERS,
  DEFAULT_SAVED_VIEWS
};
