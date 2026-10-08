// ============================================
// 窗口是否处于激活状态：失焦时侧边栏选中色与图标退成灰色（macOS 惯例）
// 2026-10-07
// ============================================

import { useEffect, useState } from 'react';

const useWindowFocus = () => {
  const [focused, setFocused] = useState(() => (typeof document === 'undefined' ? true : document.hasFocus()));

  useEffect(() => {
    const handleFocus = () => setFocused(true);
    const handleBlur = () => setFocused(false);
    window.addEventListener('focus', handleFocus);
    window.addEventListener('blur', handleBlur);
    return () => {
      window.removeEventListener('focus', handleFocus);
      window.removeEventListener('blur', handleBlur);
    };
  }, []);

  return focused;
};

export default useWindowFocus;
