// ============================================
// 前端错误归一化
// 2026-09-08
// ============================================

// Wails 绑定的 reject 值有三种形态：Error 实例（前端本地抛出的）、
// { message } 对象（main.go 的 ErrorFormatter 回传）、字符串
// （参数解析失败等 Wails 内部路径直接回传 err.Error()）。
// 统一在这里取可读文案，避免 `err.message || '操作失败'` 在字符串形态下
// 静默退化成兜底文案，把后端说明的原因丢掉。
export const resolveErrorMessage = (err, fallback = '操作失败') => {
  if (typeof err === 'string') return err.trim() || fallback;
  const message = err?.message;
  if (typeof message === 'string' && message.trim()) return message;
  return fallback;
};

export const toError = (err, fallback = '操作失败') => (
  err instanceof Error ? err : new Error(resolveErrorMessage(err, fallback))
);
