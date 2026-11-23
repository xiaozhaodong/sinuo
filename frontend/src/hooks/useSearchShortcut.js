// ============================================
// 通用搜索快捷键 Hook（⌘F / Ctrl+F 聚焦可见搜索框）
// ============================================
// 隐藏页签（hidden）内的输入框 offsetParent 为 null，不会截走当前可见页签的 ⌘F。

import { useEffect, useRef } from 'react';
import { isMacPlatform } from '@utils/platform.js';

const isElementVisible = (element) => Boolean(element && element.offsetParent !== null && !element.closest('[hidden]'));

const useSearchShortcut = (inputRef, { enabled = true, onFocusRequest } = {}) => {
  const onFocusRequestRef = useRef(onFocusRequest);

  useEffect(() => {
    onFocusRequestRef.current = onFocusRequest;
  }, [onFocusRequest]);

  useEffect(() => {
    if (!enabled) return undefined;
    const mac = isMacPlatform();
    const handleKeyDown = (event) => {
      const primary = mac ? event.metaKey : event.ctrlKey;
      if (!primary || event.altKey || event.shiftKey || event.key.toLowerCase() !== 'f') return;
      if (document.querySelector('[aria-modal="true"]')) return;

      if (onFocusRequestRef.current) {
        const handled = onFocusRequestRef.current(event);
        if (handled) event.preventDefault();
        return;
      }

      const input = inputRef?.current;
      if (!isElementVisible(input)) return;
      event.preventDefault();
      input.focus();
      input.select();
    };
    window.addEventListener('keydown', handleKeyDown);
    return () => window.removeEventListener('keydown', handleKeyDown);
  }, [enabled, inputRef]);
};

export default useSearchShortcut;
