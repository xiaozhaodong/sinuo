import { isAccountSchedulable, resolveAccountId } from '../../account-pool/utils/accountPool.js';
import {
  SUBSCRIPTION_PROXY_MODE_OPTIONS,
  isSubscriptionProxyStoppedByUser,
  resolveSubscriptionProxyDisplayState
} from '../../../components/codex-access/subscriptionProxyViewModel.js';

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
// 模型路由优先于固定账号，最后才是自动调度；绑定数已知时随标签一起展示。
const resolveFallbackRouteLabel = ({
  modelRoutingEnabled = false,
  activeAccount = null,
  modelRouteBindingCount = null
} = {}) => {
  if (modelRoutingEnabled) {
    if (!Number.isInteger(modelRouteBindingCount)) {
      return '模型路由';
    }
    return modelRouteBindingCount > 0 ? `模型路由 · ${modelRouteBindingCount} 模型` : '模型路由 · 尚无绑定';
  }
  const name = String(activeAccount?.account_name ?? activeAccount?.accountName ?? '').trim();
  return name || 'Auto';
};

// 请求页 Codex 按钮的主标签始终回答「Codex 请求现在怎么路由」。
// 独立入口未初始化或已主动停用时，请求只会走原有入口：主标签直接显示原有入口策略，
// 入口状态退到 detail 交给 tooltip；过渡、失败与配置不一致仍由入口状态抢标签。
// 只有一次配置都没读到时才显示「读取中」，打开面板触发的刷新不改写已有标签。
const resolveCodexAccessLabel = ({
  loading = false,
  configError = '',
  statusError = '',
  config = null,
  status = null,
  ready = false,
  routeLabel = 'Auto'
} = {}) => {
  if (loading && !config) {
    return { label: '读取中', detail: '' };
  }
  if (configError) {
    return { label: '配置读取失败', detail: '' };
  }
  if (!config?.initialized) {
    return { label: routeLabel, detail: '独立入口未初始化' };
  }
  if (statusError) {
    return { label: '状态未知', detail: '' };
  }
  if (isSubscriptionProxyStoppedByUser({ config, status })) {
    return { label: routeLabel, detail: '独立入口已停用' };
  }
  if (status?.state !== 'running') {
    return { label: resolveSubscriptionProxyDisplayState({ config, status }).label, detail: '' };
  }
  if (!ready) {
    return { label: '状态待确认', detail: '' };
  }
  if (config.mode === 'pool') {
    return { label: `账号池 · ${routeLabel}`, detail: '' };
  }
  const modeLabel = SUBSCRIPTION_PROXY_MODE_OPTIONS.find((option) => option.value === config.mode)?.label;
  return { label: modeLabel || '未知模式', detail: '' };
};

export {
  canPinAccountSelection,
  isSamePinnedAccount,
  isRuntimeActiveSelection,
  resolveCodexAccessLabel,
  resolveDisplayedActiveAccount,
  resolveFallbackRouteLabel
};
