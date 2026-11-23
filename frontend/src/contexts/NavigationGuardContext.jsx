// ============================================
// 跨页导航未保存更改保护上下文
// ============================================
// 页面注册未保存状态后，点击侧边栏或按 ⌘1–⌘7 / ⌘, 切页前会先弹出确认框，
// 防止正在编辑的设置、定价或模型目录配置在切页卸载时静默丢失。

import { createContext, useCallback, useContext, useEffect, useMemo, useRef } from 'react';
import useConfirm from '@hooks/useConfirm.js';

const NavigationGuardContext = createContext(null);

const DEFAULT_GUARD_OPTIONS = {
  title: '放弃未保存的修改？',
  message: '当前页面有未保存的修改，切换页面后将丢失。',
  confirmText: '放弃并离开',
  danger: true
};

export const NavigationGuardProvider = ({ children }) => {
  const guardsRef = useRef(new Set());
  const { confirm, confirmDialog } = useConfirm();

  const registerGuard = useCallback((guard) => {
    guardsRef.current.add(guard);
    return () => {
      guardsRef.current.delete(guard);
    };
  }, []);

  const canNavigate = useCallback(async () => {
    for (const guard of Array.from(guardsRef.current)) {
      const decision = await guard(confirm);
      if (decision === false) return false;
    }
    return true;
  }, [confirm]);

  const value = useMemo(
    () => ({ registerGuard, canNavigate }),
    [registerGuard, canNavigate]
  );

  return (
    <NavigationGuardContext.Provider value={value}>
      {children}
      {confirmDialog}
    </NavigationGuardContext.Provider>
  );
};

// eslint-disable-next-line react-refresh/only-export-components
export const useBeforeNavigate = () => {
  const context = useContext(NavigationGuardContext);
  return useCallback(async () => {
    if (!context) return true;
    return context.canNavigate();
  }, [context]);
};

// 页面声明存在未保存更改：支持传入布尔值或惰性读取函数（如 () => dirtyRef.current）
// eslint-disable-next-line react-refresh/only-export-components
export const useNavigationGuard = (dirtyOrGetter, options = DEFAULT_GUARD_OPTIONS) => {
  const context = useContext(NavigationGuardContext);
  const dirtyRef = useRef(dirtyOrGetter);
  const optionsRef = useRef(options);

  useEffect(() => {
    dirtyRef.current = dirtyOrGetter;
  }, [dirtyOrGetter]);

  useEffect(() => {
    optionsRef.current = options;
  }, [options]);

  useEffect(() => {
    if (!context) return undefined;
    return context.registerGuard(async (confirm) => {
      const check = dirtyRef.current;
      const isDirty = typeof check === 'function' ? check() : Boolean(check);
      if (!isDirty) return true;
      return confirm({ ...DEFAULT_GUARD_OPTIONS, ...optionsRef.current });
    });
  }, [context]);
};
