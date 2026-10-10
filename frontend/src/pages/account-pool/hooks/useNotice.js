// ============================================
// 页面操作反馈统一投递到全局通知队列
// 2026-10-10
// ============================================

import { useCallback, useLayoutEffect, useRef } from 'react';
import { useShowToast } from '@contexts/ToastContext.jsx';

const useNotice = ({ enabled = true } = {}) => {
  const showToast = useShowToast();
  const enabledRef = useRef(enabled);

  // 可见性变化只更新开关，避免改变业务回调依赖的 showNotice 引用。
  useLayoutEffect(() => {
    enabledRef.current = enabled;
  }, [enabled]);

  const showNotice = useCallback((type, text) => {
    if (!enabledRef.current || !showToast) return;
    const message = String(text ?? '').trim();
    if (!message) return;
    showToast({ level: type, message, duration: 4000 });
  }, [showToast]);

  return { showNotice };
};

export default useNotice;
