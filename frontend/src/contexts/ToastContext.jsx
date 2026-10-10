import { createContext, useContext } from 'react';

const ToastContext = createContext(null);

export const ToastProvider = ({ showToast, children }) => (
  <ToastContext.Provider value={showToast}>{children}</ToastContext.Provider>
);

// Context 配套 Hook 与 Provider 同文件是项目惯例；仅影响 HMR 精度，不影响构建。
// eslint-disable-next-line react-refresh/only-export-components
export const useShowToast = () => useContext(ToastContext);
