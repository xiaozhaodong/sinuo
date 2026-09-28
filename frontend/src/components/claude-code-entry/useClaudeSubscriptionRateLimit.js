// 订阅直通额度：挂载时读取当前状态，之后跟随 claude-code-entry:rate-limit 推送更新；手动刷新受后端冷却限制。
// 请求页 Claude 按钮持有这份状态，角标与浮层额度区共用，避免各自订阅一遍。

import { useCallback, useEffect, useState } from 'react';
import { resolveErrorMessage } from '@utils/errors.js';
import {
  getClaudeSubscriptionRateLimit,
  isWailsEnvironment,
  refreshClaudeSubscriptionRateLimit,
  subscribeToEvent
} from '@utils/wailsApi.js';
import {
  nextSubscriptionRecheckDelay,
  normalizeSubscriptionQuota,
  pickNewerSubscriptionQuota
} from './subscriptionRateLimit.js';

export const CLAUDE_SUBSCRIPTION_RATE_LIMIT_EVENT = 'claude-code-entry:rate-limit';

const useClaudeSubscriptionRateLimit = () => {
  const available = isWailsEnvironment();
  const [quota, setQuota] = useState(null);
  const [error, setError] = useState('');
  const [refreshing, setRefreshing] = useState(false);
  // 判断窗口是否已过重置时间、刷新是否已冷却的基准；收到新状态或到点时更新
  const [nowMs, setNowMs] = useState(() => Date.now());

  const accept = useCallback((raw) => {
    setQuota((current) => pickNewerSubscriptionQuota(current, normalizeSubscriptionQuota(raw)));
    setNowMs(Date.now());
  }, []);

  useEffect(() => {
    if (!available) return undefined;
    let active = true;
    getClaudeSubscriptionRateLimit()
      .then((raw) => active && accept(raw))
      .catch((err) => active && setError(resolveErrorMessage(err, '读取订阅额度失败')));
    const unsubscribe = subscribeToEvent(CLAUDE_SUBSCRIPTION_RATE_LIMIT_EVENT, accept);
    return () => {
      active = false;
      unsubscribe();
    };
  }, [available, accept]);

  const refresh = useCallback(async () => {
    setRefreshing(true);
    setError('');
    try {
      accept(await refreshClaudeSubscriptionRateLimit());
    } catch (err) {
      setError(resolveErrorMessage(err, '刷新订阅额度失败'));
      // 被冷却或凭据过期拒绝时，同步一次后端状态，让按钮回到正确的可用时间
      getClaudeSubscriptionRateLimit().then(accept).catch(() => {});
    } finally {
      setRefreshing(false);
    }
  }, [accept]);

  // 浮层一直开着时，到下一个重置或冷却结束时间重算一次，过期读数与置灰按钮不会一直挂着
  useEffect(() => {
    const delay = nextSubscriptionRecheckDelay(quota, nowMs);
    if (delay === null) return undefined;
    const timer = setTimeout(() => setNowMs(Date.now()), delay);
    return () => clearTimeout(timer);
  }, [quota, nowMs]);

  return { available, quota, error, refreshing, nowMs, refresh };
};

export default useClaudeSubscriptionRateLimit;
