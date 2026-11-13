import { normalizePrivacyNotification, PRIVACY_WARNINGS } from '../pages/requests/utils/privacyWarnings.js';

export const PRIVACY_RECEIPT_LIMIT = 4096;
export const PRIVACY_MERGE_WINDOW_MS = 10000;

// 后端按请求生命周期去重；前端再保存有界收据，关闭通知后重复事件也不重弹。
// 这里在 setState updater 外调用，避免 React 重放 updater 消耗收据。
export const createPrivacyNotificationGate = () => {
  const seen = new Map();
  return {
    accept(input) {
      const payload = input?.kind ? input : input?.data;
      if (payload?.kind !== 'privacy_warning') return input;
      const normalized = normalizePrivacyNotification(payload);
      if (!normalized) return null;
      const previous = seen.get(normalized.request_id) || [];
      const fresh = normalized.warningCodes.filter((code) => !previous.includes(code));
      if (!fresh.length) return null;
      seen.delete(normalized.request_id);
      seen.set(normalized.request_id, [...previous, ...fresh]);
      if (seen.size > PRIVACY_RECEIPT_LIMIT) seen.delete(seen.keys().next().value);
      return { ...normalized, warning_codes: fresh };
    },
    get size() { return seen.size; }
  };
};

// 不刷新展示截止时间：连续失败合并计数，但不会让警告一直挂在窗口上。
export const enqueuePrivacyNotification = (queue, notification, now) => {
  const index = queue.findIndex((toast) => toast.kind === 'privacy_warning'
    && (!Number.isFinite(toast.expiresAt) || toast.expiresAt > now)
    && (toast.expiresAt === null || now - toast.createdAt <= PRIVACY_MERGE_WINDOW_MS));
  const existing = index >= 0 ? queue[index] : null;
  const receipts = Object.assign(Object.create(null), existing?.privacyReceipts);
  const previous = receipts[notification.requestId] || [];
  const fresh = notification.warningCodes.filter((code) => !previous.includes(code));
  if (!fresh.length) return queue;
  // 使用无原型对象存储任意 requestID，避免 __proto__ 等标识影响计数。
  const nextReceipts = Object.assign(Object.create(null), receipts, { [notification.requestId]: [...previous, ...fresh] });
  const ids = Object.keys(nextReceipts);
  const counts = Object.fromEntries(Object.keys(PRIVACY_WARNINGS).map((code) => [code, ids.filter((id) => nextReceipts[id].includes(code)).length]));
  const codes = Object.keys(counts).filter((code) => counts[code]);
  const single = ids.length === 1 && codes.length === 1;
  const next = {
    ...notification, id: existing?.id || notification.id,
    title: single ? PRIVACY_WARNINGS[codes[0]].title : `${ids.length} 个请求出现隐私处理异常`,
    message: single ? PRIVACY_WARNINGS[codes[0]].message
      : codes.map((code) => `${PRIVACY_WARNINGS[code].short} ${counts[code]} 个`).join('；') + '。详情见请求列表。',
    privacyReceipts: nextReceipts, createdAt: existing?.createdAt ?? now,
    expiresAt: existing?.expiresAt ?? null
  };
  const result = [...queue];
  if (index < 0) result.push(next); else result[index] = next;
  return result;
};
