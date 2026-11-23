// ============================================
// 桌面外壳快捷键：⌥⌘S 折叠侧边栏、⌥⌘I 折叠右侧检查器、⌘/ 快捷键速查
// ============================================

import { useEffect } from 'react';
import { isMacPlatform } from '@utils/platform.js';

const MODAL_OPEN_SELECTOR = '[aria-modal="true"], #app-scroll-container[data-scroll-lock-count]';

const useShellShortcuts = ({ onToggleSidebar, onToggleInspector, canToggleInspector = false, onToggleShortcuts }) => {
  useEffect(() => {
    const mac = isMacPlatform();
    const handleKeyDown = (event) => {
      if (event.repeat || event.shiftKey) return;
      const primary = mac ? event.metaKey && !event.ctrlKey : event.ctrlKey && !event.metaKey;
      if (!primary) return;

      const key = event.key.toLowerCase();

      // ⌘/ 或 Ctrl+/：打开/关闭快捷键速查
      if (!event.altKey && (event.key === '/' || event.code === 'Slash')) {
        if (document.querySelector(MODAL_OPEN_SELECTOR) && !document.querySelector('[data-shortcuts-dialog="true"]')) return;
        event.preventDefault();
        onToggleShortcuts?.();
        return;
      }

      if (!event.altKey) return;
      if (document.querySelector(MODAL_OPEN_SELECTOR)) return;

      // ⌥⌘S / Ctrl+Alt+S：显隐左侧边栏
      if (key === 's' || event.code === 'KeyS') {
        event.preventDefault();
        onToggleSidebar?.();
        return;
      }

      // ⌥⌘I / Ctrl+Alt+I：显隐右侧检查器
      if ((key === 'i' || event.code === 'KeyI') && canToggleInspector) {
        event.preventDefault();
        onToggleInspector?.();
      }
    };

    window.addEventListener('keydown', handleKeyDown);
    return () => window.removeEventListener('keydown', handleKeyDown);
  }, [canToggleInspector, onToggleInspector, onToggleShortcuts, onToggleSidebar]);
};

export default useShellShortcuts;
