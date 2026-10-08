// ============================================
// 页面快捷键：⌘1–⌘7 切页、⌘, 打开设置（Windows / Linux 为 Ctrl）
// 2026-10-07
// ============================================

import { useEffect } from 'react';
import { pageForShortcut } from '@components/layout/navigation.js';
import { isMacPlatform } from '@utils/platform.js';

// 有模态弹窗时不切页：切走会卸载当前页，弹窗里未保存的编辑会丢。
// 两个信号任一成立即视为模态打开：显式 aria-modal，或 modalLifecycle 锁住了主滚动容器。
const MODAL_OPEN_SELECTOR = '[aria-modal="true"], #app-scroll-container[data-scroll-lock-count]';

const isPrimaryModifierOnly = (event, mac) => {
  if (event.altKey || event.shiftKey) return false;
  return mac ? event.metaKey && !event.ctrlKey : event.ctrlKey && !event.metaKey;
};

const useNavigationShortcuts = (onNavigate) => {
  useEffect(() => {
    const mac = isMacPlatform();
    const handleKeyDown = (event) => {
      if (event.repeat || !isPrimaryModifierOnly(event, mac)) return;
      const page = pageForShortcut(event.key);
      if (!page || document.querySelector(MODAL_OPEN_SELECTOR)) return;
      event.preventDefault();
      onNavigate(page);
    };
    window.addEventListener('keydown', handleKeyDown);
    return () => window.removeEventListener('keydown', handleKeyDown);
  }, [onNavigate]);
};

export default useNavigationShortcuts;
