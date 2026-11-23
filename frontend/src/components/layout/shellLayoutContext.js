// ============================================
// 桌面外壳布局上下文（纯 React，无 DOM / 第三方依赖）
// 供 AppToolbar 与 Inspector 共享侧边栏折叠与右侧检查器状态，
// 避免仅导入 Inspector 子组件的单元测试间接拉入 react-dom 或平台工具。
// ============================================

import { createContext, useContext } from 'react';

export const ToolbarSlotsContext = createContext(null);

export const useShellLayout = () => useContext(ToolbarSlotsContext);
