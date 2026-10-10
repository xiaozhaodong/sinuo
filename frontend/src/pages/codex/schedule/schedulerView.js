import { normalizeEntityId, resolveAccountId } from '../../account-pool/utils/accountPool.js';
import { normalizePriorityValue } from '../../account-pool/utils/manualFailover.js';

export const buildGroupHeader = (group) => {
  const accounts = Array.isArray(group?.accounts) ? group.accounts : [];
  return { total: accounts.length, available: accounts.filter(account => account.isAvailable).length };
};

export const isLatestHit = (account, summary) => {
  const selectedId = normalizeEntityId(summary?.selectedAccountId);
  return selectedId !== null && resolveAccountId(account) === selectedId;
};

export const buildStatusCounts = (groups = []) => groups.reduce((counts, group) => {
  const { total, available } = buildGroupHeader(group);
  if (['primary', 'backup', 'cold'].includes(group.key)) counts[group.key] += total;
  counts.available += available;
  counts.unavailable += total - available;
  return counts;
}, { primary: 0, backup: 0, cold: 0, available: 0, unavailable: 0 });

export const buildCandidateRows = (snapshot) => (
  Array.isArray(snapshot?.candidates) ? snapshot.candidates : []
).map((candidate, index) => ({
  id: resolveAccountId(candidate) ?? `candidate-${index}`,
  accountName: candidate.account_name || candidate.accountName || '-',
  groupLabel: candidate.tier_label || candidate.tierLabel || '未分组',
  priority: normalizePriorityValue(candidate.priority ?? candidate.Priority),
  decision: String(candidate.decision || '').trim().toLowerCase(),
  runtimeOutcome: String(candidate.runtime_outcome || candidate.runtimeOutcome || '').trim().toLowerCase(),
  reason: candidate.reason || '',
  reasonDetail: candidate.reason_detail || candidate.reasonDetail || '',
  quotaStatus: String(candidate.quota_status || candidate.quotaStatus || '').trim().toLowerCase() || 'pending',
  effectiveQuotaRemaining: Number.parseFloat(candidate.effective_quota_remaining ?? candidate.effectiveQuotaRemaining),
  failCount: Number.parseInt(candidate.fail_count ?? candidate.failCount, 10) || 0,
  runtimeError: candidate.runtime_error || candidate.runtimeError || ''
}));

export const countCandidates = (rows = []) => ({
  total: rows.length,
  skipped: rows.filter(row => row.decision === 'skipped').length
});

// 行内按钮与右键菜单共用禁用规则，沿用原编排动作的 busy 令牌。
export const buildAccountActionState = (account, group, busyKey = '') => ({
  preferredDisabled: Boolean(account.isGroupPreferred || account.isActive
    || busyKey === `group-active-${group.key}-${account.id}`
    || busyKey.startsWith(`group-swap-${group.key}-`)),
  pinDisabled: Boolean(account.isActive || busyKey),
  preferredTitle: account.isGroupPreferred ? '已是本组首选'
    : account.isActive ? '已全局固定，组内首选暂不生效' : '设为本组首选',
  pinTitle: account.isActive ? '已全局固定，可在右侧「启用编排」解除' : '设为全局固定'
});
