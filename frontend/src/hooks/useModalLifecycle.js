// ============================================
// 模态生命周期共享 Hook
// 2026-07-24
// ============================================
//
// 为抽屉/弹窗统一补齐模态交互生命周期：
// 1. 打开时锁定主滚动区（复用 lockAppScroll，含引用计数）
// 2. Escape 关闭：多层模态叠加时仅最顶层响应，避免一次按键关闭全部
// 3. 焦点管理：打开时移入 initialFocusRef，关闭时恢复到之前聚焦的元素
// 4. 传入 focusTrapRef 时，最顶层模态限制 Tab 和游离焦点，其他调用方保持原行为

import { useEffect, useRef } from 'react';
import { modalLifecycleManager } from './modalLifecycle.js';

const useModalLifecycle = ({ open, onClose, initialFocusRef, focusTrapRef }) => {
  // onClose 常为内联箭头函数，用 ref 持有最新引用，
  // 避免其每次渲染变化导致 Escape effect 反复挂卸、打乱模态栈顺序
  const onCloseRef = useRef(onClose);
  useEffect(() => {
    onCloseRef.current = onClose;
  }, [onClose]);

  useEffect(() => {
    if (!open) return undefined;
    return modalLifecycleManager.activate({
      getOnClose: () => onCloseRef.current,
      initialFocusElement: initialFocusRef?.current,
      focusTrapElement: focusTrapRef?.current
    });
  }, [open, initialFocusRef, focusTrapRef]);
};

export default useModalLifecycle;
