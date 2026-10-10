import { useCallback, useEffect, useMemo, useState } from 'react';

export const MAX_VISIBLE_TOASTS = 4;
const DEFAULT_DURATION = 6500;
let toastSequence = 0;

const asObject = (value) => (
  value && typeof value === 'object' && !Array.isArray(value) ? value : null
);

export const normalizeNotification = (input) => {
  const candidate = asObject(input);
  if (!candidate) return null;

  // useWailsEvents 会同时提供 data 和展开后的字段；优先使用展开后的完整对象。
  const payload = candidate.kind || candidate.message || candidate.title
    ? candidate
    : asObject(candidate.data);
  if (!payload || typeof payload.message !== 'string' || payload.message.trim() === '') {
    return null;
  }

  const kind = typeof payload.kind === 'string' ? payload.kind : 'notification';
  const lane = typeof payload.lane === 'string' ? payload.lane : '';
  const from = typeof payload.from === 'string' ? payload.from : '';
  const to = typeof payload.to === 'string' ? payload.to : '';
  const requestId = typeof payload.request_id === 'string' ? payload.request_id : '';
  const reasonCode = typeof payload.reason_code === 'string' ? payload.reason_code : '';
  const attempt = Number.isFinite(Number(payload.attempt)) ? Number(payload.attempt) : 0;
  const explicitId = typeof payload.id === 'string' || typeof payload.id === 'number'
    ? String(payload.id)
    : '';
  const dedupeKey = explicitId || (kind === 'failover'
    ? [kind, lane, from, to, reasonCode, requestId, attempt].join('|')
    : [kind, payload.level, payload.title, payload.message].join('|'));

  return {
    id: `toast-${Date.now()}-${toastSequence += 1}`,
    dedupeKey,
    kind,
    level: ['info', 'success', 'warning', 'error'].includes(payload.level) ? payload.level : 'info',
    title: typeof payload.title === 'string' && payload.title.trim() ? payload.title : '',
    message: payload.message.trim(),
    lane,
    from,
    to,
    requestId,
    requestPath: typeof payload.request_path === 'string' ? payload.request_path : '',
    reasonCode,
    reasonLabel: typeof payload.reason_label === 'string' ? payload.reason_label : '',
    reasonDetail: typeof payload.reason_detail === 'string' ? payload.reason_detail : '',
    attempt,
    duration: Number.isFinite(payload.duration) && payload.duration > 0
      ? payload.duration
      : kind === 'failover' ? 7500 : DEFAULT_DURATION
  };
};

export const getNotificationDedupeKey = (notification) => {
  const normalized = normalizeNotification(notification);
  return normalized?.dedupeKey || '';
};

// 仅给当前可见的 Toast 分配绝对到期时间；排队中的 Toast 在真正显示时才开始计时。
export const activateVisibleToasts = (queue, now = Date.now()) => {
  if (!Array.isArray(queue) || queue.length === 0) return [];

  let changed = false;
  const activated = queue.map((toast, index) => {
    if (index >= MAX_VISIBLE_TOASTS || Number.isFinite(toast.expiresAt)) {
      return toast;
    }
    changed = true;
    return {
      ...toast,
      expiresAt: now + toast.duration
    };
  });
  return changed ? activated : queue;
};

export const enqueueNotification = (queue, notification, now = Date.now()) => {
  const current = Array.isArray(queue) ? queue : [];
  const normalized = normalizeNotification(notification);
  if (!normalized) return current;

  const existingIndex = current.findIndex((toast) => toast.dedupeKey === normalized.dedupeKey);
  const nextQueue = [...current];
  if (existingIndex >= 0) {
    const existing = current[existingIndex];
    nextQueue[existingIndex] = {
      ...normalized,
      id: existing.id,
      // 重复事件只刷新自己的展示时间，不影响其他 Toast 的绝对到期时间。
      expiresAt: existingIndex < MAX_VISIBLE_TOASTS ? now + normalized.duration : null
    };
  } else {
    nextQueue.push({
      ...normalized,
      expiresAt: null
    });
  }

  return activateVisibleToasts(nextQueue, now);
};

export const dismissNotification = (queue, toastId, now = Date.now()) => {
  const current = Array.isArray(queue) ? queue : [];
  const nextQueue = current.filter((toast) => toast.id !== toastId);
  if (nextQueue.length === current.length) return current;
  return activateVisibleToasts(nextQueue, now);
};

const useGlobalToasts = () => {
  const [toastQueue, setToastQueue] = useState([]);
  const toasts = useMemo(
    () => toastQueue.slice(0, MAX_VISIBLE_TOASTS),
    [toastQueue]
  );
  const pendingCount = Math.max(toastQueue.length - MAX_VISIBLE_TOASTS, 0);

  const dismissToast = useCallback((toastId) => {
    setToastQueue((current) => dismissNotification(current, toastId));
  }, []);

  const showToast = useCallback((notification) => {
    setToastQueue((current) => enqueueNotification(current, notification));
  }, []);

  const expireToast = useCallback((toastId, expectedExpiresAt) => {
    setToastQueue((current) => {
      const toast = current.find((item) => item.id === toastId);
      if (!toast || !Number.isFinite(expectedExpiresAt) || toast.expiresAt !== expectedExpiresAt || toast.expiresAt > Date.now()) {
        return current;
      }
      return dismissNotification(current, toastId);
    });
  }, []);

  useEffect(() => {
    const timers = toasts.map((toast) => (
      window.setTimeout(() => {
        expireToast(toast.id, toast.expiresAt);
      }, Math.max((Number.isFinite(toast.expiresAt) ? toast.expiresAt : Date.now()) - Date.now(), 0))
    ));
    return () => timers.forEach((timer) => window.clearTimeout(timer));
  }, [expireToast, toasts]);

  const clearToasts = useCallback(() => setToastQueue([]), []);

  return {
    toasts,
    pendingCount,
    showToast,
    dismissToast,
    clearToasts
  };
};

export default useGlobalToasts;
