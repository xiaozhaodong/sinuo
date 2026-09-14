import { isAccountSchedulable, resolveAccountId } from '../../account-pool/utils/accountPool.js';

const isRuntimeActiveSelection = (account = {}) => {
  return (account?.is_active_selection ?? account?.isActiveSelection) === true;
};

const isSamePinnedAccount = ({
  displayedActiveAccount = null,
  targetAccount = null
} = {}) => {
  if (!isRuntimeActiveSelection(displayedActiveAccount)) {
    return false;
  }

  return String(resolveAccountId(displayedActiveAccount) ?? '') === String(resolveAccountId(targetAccount) ?? '');
};

const canPinAccountSelection = (account = {}) => {
  if (!account || account.enabled === false) {
    return false;
  }

  const state = String(account.state || '').trim().toLowerCase();
  return state !== 'disabled_auth';
};

const resolveDisplayedActiveAccount = ({
  accounts = [],
  recentSelectedAccountId = null
} = {}) => {
  const list = Array.isArray(accounts) ? accounts : [];

  const runtimeActiveAccount = list.find(isRuntimeActiveSelection);
  if (runtimeActiveAccount) {
    return runtimeActiveAccount;
  }

  if (recentSelectedAccountId !== null && recentSelectedAccountId !== undefined) {
    const recentSelectedAccount = list.find((account) => {
      return String(resolveAccountId(account) ?? '') === String(recentSelectedAccountId);
    });
    if (recentSelectedAccount) {
      return recentSelectedAccount;
    }
  }

  return list.find(isAccountSchedulable) || list[0] || null;
};

// 原有入口的已配置策略，不代表客户端当前请求去向。
// 模型路由优先于固定账号，最后才是自动调度。
const resolveFallbackRouteLabel = ({ modelRoutingEnabled = false, activeAccount = null } = {}) => {
  if (modelRoutingEnabled) {
    return '模型路由';
  }
  const name = String(activeAccount?.account_name ?? activeAccount?.accountName ?? '').trim();
  return name || 'Auto';
};

export {
  canPinAccountSelection,
  isSamePinnedAccount,
  isRuntimeActiveSelection,
  resolveDisplayedActiveAccount,
  resolveFallbackRouteLabel
};
