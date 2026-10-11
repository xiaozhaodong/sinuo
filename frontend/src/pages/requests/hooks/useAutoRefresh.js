// ============================================
// useAutoRefresh Hook - 请求页实时刷新状态管理
// 正常使用 Wails Events；事件不可用或连续查询失败时启用低频降级轮询。
// ============================================

import { useState, useEffect, useRef, useCallback } from 'react';
import { initWails, isWailsEnvironment, subscribeToEvent } from '@utils/wailsApi.js';
import { createRealtimeRefreshScheduler } from '../utils/realtimeRefreshScheduler.js';

export const REALTIME_REFRESH_MODE = {
  CONNECTING: 'connecting',
  LIVE: 'live',
  FALLBACK: 'fallback'
};

const EVENT_DEBOUNCE_MS = 250;
const EVENT_MAX_WAIT_MS = 1000;
const FALLBACK_INTERVAL_SECONDS = 30;
const FAILURE_THRESHOLD = 3;

/**
 * 正常模式下由 request:update 主动通知触发刷新；并发事件会合并为一次查询。
 * 页面重新可见时主动校准一次。事件不可用或连续失败时每 30 秒降级刷新。
 *
 * @param {Function} onRefresh - 刷新回调函数
 * @param {Function} [onEvent] - 每条 request:update 到达时同步调用，拿到原始负载（计时补丁用）
 * @returns {Object}
 */
export const useAutoRefresh = (onRefresh, onEvent) => {
  const [mode, setMode] = useState(
    isWailsEnvironment() ? REALTIME_REFRESH_MODE.CONNECTING : REALTIME_REFRESH_MODE.FALLBACK
  );
  const onRefreshRef = useRef(onRefresh);
  const onEventRef = useRef(onEvent);
  const schedulerRef = useRef(null);
  const subscriptionActiveRef = useRef(false);
  const consecutiveFailuresRef = useRef(0);

  // 每次渲染时更新 ref 中的回调
  useEffect(() => {
    onRefreshRef.current = onRefresh;
    onEventRef.current = onEvent;
  }, [onRefresh, onEvent]);

  const executeRefresh = useCallback(async () => {
    if (document.hidden) {
      return;
    }
    try {
      await onRefreshRef.current?.();
      consecutiveFailuresRef.current = 0;
      if (subscriptionActiveRef.current) {
        setMode(REALTIME_REFRESH_MODE.LIVE);
      }
    } catch (error) {
      consecutiveFailuresRef.current += 1;
      console.error('❌ [请求实时刷新] 查询失败:', error);
      if (consecutiveFailuresRef.current >= FAILURE_THRESHOLD) {
        setMode(REALTIME_REFRESH_MODE.FALLBACK);
      }
    }
  }, []);

  const scheduleEventRefresh = useCallback(() => {
    schedulerRef.current?.schedule();
  }, []);

  useEffect(() => {
    schedulerRef.current = createRealtimeRefreshScheduler({
      refresh: executeRefresh,
      debounceMs: EVENT_DEBOUNCE_MS,
      maxWaitMs: EVENT_MAX_WAIT_MS
    });

    return () => {
      schedulerRef.current?.cancel();
      schedulerRef.current = null;
    };
  }, [executeRefresh]);

  useEffect(() => {
    let cancelled = false;
    let unsubscribe = null;

    const subscribe = async () => {
      if (!isWailsEnvironment()) {
        subscriptionActiveRef.current = false;
        setMode(REALTIME_REFRESH_MODE.FALLBACK);
        return;
      }

      try {
        const initialized = await initWails();
        if (cancelled) return;
        if (!initialized) {
          subscriptionActiveRef.current = false;
          setMode(REALTIME_REFRESH_MODE.FALLBACK);
          return;
        }

        unsubscribe = subscribeToEvent('request:update', (payload) => {
          onEventRef.current?.(payload);
          scheduleEventRefresh();
        });
        subscriptionActiveRef.current = true;
        setMode(REALTIME_REFRESH_MODE.LIVE);
        // 订阅建立后校准一次，覆盖初始查询与订阅之间可能发生的变化。
        scheduleEventRefresh();
      } catch (error) {
        console.error('❌ [请求实时刷新] Wails 事件订阅失败:', error);
        subscriptionActiveRef.current = false;
        if (!cancelled) {
          setMode(REALTIME_REFRESH_MODE.FALLBACK);
        }
      }
    };

    subscribe();

    return () => {
      cancelled = true;
      subscriptionActiveRef.current = false;
      if (typeof unsubscribe === 'function') {
        unsubscribe();
      }
    };
  }, [scheduleEventRefresh]);

  useEffect(() => {
    const handleVisibilityChange = () => {
      if (!document.hidden) {
        void schedulerRef.current?.triggerNow();
      }
    };

    document.addEventListener('visibilitychange', handleVisibilityChange);

    return () => {
      document.removeEventListener('visibilitychange', handleVisibilityChange);
    };
  }, []);

  useEffect(() => {
    if (mode !== REALTIME_REFRESH_MODE.FALLBACK) {
      return undefined;
    }

    const timer = window.setInterval(() => {
      void schedulerRef.current?.triggerNow();
    }, FALLBACK_INTERVAL_SECONDS * 1000);
    return () => {
      clearInterval(timer);
    };
  }, [mode]);

  return {
    mode,
    isLive: mode === REALTIME_REFRESH_MODE.LIVE,
    isFallback: mode === REALTIME_REFRESH_MODE.FALLBACK,
    fallbackInterval: FALLBACK_INTERVAL_SECONDS
  };
};

export default useAutoRefresh;
