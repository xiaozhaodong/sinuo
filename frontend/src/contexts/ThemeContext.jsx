import { createContext, useCallback, useContext, useEffect, useMemo, useState, useSyncExternalStore } from 'react';

const STORAGE_KEY = 'ai-switchboard-theme';
const DARK_QUERY = '(prefers-color-scheme: dark)';
const PREFERENCES = ['system', 'light', 'dark'];

const ThemeContext = createContext(null);

const readStoredPreference = () => {
  try {
    const stored = window.localStorage.getItem(STORAGE_KEY);
    return PREFERENCES.includes(stored) ? stored : 'system';
  } catch {
    // 隐私模式 / 存储被禁用时退回跟随系统
    return 'system';
  }
};

// useSyncExternalStore 的 getSnapshot：每次渲染现读，快照不会过期
const matchSystemDark = () => {
  try {
    return window.matchMedia(DARK_QUERY).matches;
  } catch {
    return false;
  }
};

const subscribeSystemDark = (onChange) => {
  let mediaQuery;
  try {
    mediaQuery = window.matchMedia(DARK_QUERY);
  } catch {
    return () => {};
  }
  mediaQuery.addEventListener('change', onChange);
  return () => mediaQuery.removeEventListener('change', onChange);
};

export const ThemeProvider = ({ children }) => {
  // 初值同步可得：index.html 的内联脚本已按同一口径定妥 <html class>，此处不会造成首屏跳变
  const [preference, setPreferenceState] = useState(readStoredPreference);
  // 显式浅色 / 深色时也订阅：主题菜单要实时展示「跟随系统」会得到哪一种
  const systemDark = useSyncExternalStore(subscribeSystemDark, matchSystemDark);

  const system = systemDark ? 'dark' : 'light';
  const resolved = preference === 'system' ? system : preference;

  useEffect(() => {
    const root = document.documentElement;
    const shouldBeDark = resolved === 'dark';
    // 首次挂载时 class 已由 index.html 定妥；StrictMode 双调用也走这里直接早退
    if (root.classList.contains('dark') === shouldBeDark) return undefined;

    root.classList.add('theme-switching');
    root.classList.toggle('dark', shouldBeDark);
    const frame = window.requestAnimationFrame(() => {
      root.classList.remove('theme-switching');
    });
    return () => {
      window.cancelAnimationFrame(frame);
      root.classList.remove('theme-switching');
    };
  }, [resolved]);

  const setPreference = useCallback((next) => {
    const value = PREFERENCES.includes(next) ? next : 'system';
    setPreferenceState(value);
    try {
      window.localStorage.setItem(STORAGE_KEY, value);
    } catch {
      // 存储不可用时仅本次会话生效
    }
  }, []);

  const value = useMemo(
    () => ({ preference, resolved, system, setPreference }),
    [preference, resolved, system, setPreference]
  );

  return <ThemeContext.Provider value={value}>{children}</ThemeContext.Provider>;
};

// Context 配套 Hook 与 Provider 同文件是项目惯例；仅影响 HMR 精度，不影响构建。
// eslint-disable-next-line react-refresh/only-export-components
export const useTheme = () => {
  const context = useContext(ThemeContext);
  if (!context) throw new Error('useTheme 必须在 ThemeProvider 内使用');
  return context;
};
