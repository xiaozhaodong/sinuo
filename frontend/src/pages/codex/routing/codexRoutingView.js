import { isAccountSchedulable, toAccountStateLabel, toScheduleOutcomeLabel } from '../../account-pool/utils/accountPool.js';

// 与账号池的状态和结果文案共用一套规则；暂停成员仍可被手动固定。
export const codexMemberState = (binding, id, account) => {
  if (!account) return { label: '账号不存在', tone: 'tone-rose' };
  if (account.enabled === false || account.state === 'disabled_auth') {
    return { label: toAccountStateLabel(account.enabled === false ? 'disabled' : account.state), tone: 'tone-rose' };
  }
  if (!account.has_credential && !account.hasCredential && !account.credential_raw && !account.credentialRaw) {
    return { label: '无凭据', tone: 'tone-amber' };
  }
  if (!isAccountSchedulable(account)) {
    return { label: account.state === 'cooldown' ? '冷却中' : '额度已耗尽', tone: 'tone-amber' };
  }
  if (binding.member_enabled?.[id] === false) {
    return { label: '未参与自动调度', tone: 'tone-amber', detail: '不参与自动或优选调度，仍可被手动固定使用' };
  }
  return { label: '可参与', tone: 'tone-emerald' };
};

export const codexOutcomeLabel = (outcome) => {
  if (!outcome) return '—';
  const label = toScheduleOutcomeLabel(outcome);
  return label === '未知' ? outcome : label;
};

// 后端整条覆盖写：顺序、成员开关、备注都必须随行内修改保留。
export const codexBindingPayload = (binding, patch) => {
  const next = { ...binding, ...patch };
  return {
    model: next.model,
    account_ids: next.account_ids,
    member_enabled: next.member_enabled,
    route_mode: next.route_mode,
    route_target_account_id: next.route_mode === 'auto' ? 0 : Number(next.route_target_account_id) || Number(next.account_ids?.[0]) || 0,
    enabled: next.enabled,
    note: next.note
  };
};
