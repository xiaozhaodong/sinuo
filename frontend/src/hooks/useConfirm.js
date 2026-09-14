// ============================================
// useConfirm - Promise 化的确认弹窗
// 2026-09-14
// ============================================
//
// 替代 window.confirm：Wails 桌面端（WKWebView）没有实现 confirm 面板，
// 调用恒返回 false。用法：
//   const { confirm, confirmDialog } = useConfirm();
//   if (!(await confirm({ message: '...', danger: true }))) return;
//   ...
//   return <>{...}{confirmDialog}</>;

import { createElement, useCallback, useState } from 'react';
import ConfirmDialog from '@components/ui/ConfirmDialog.jsx';

const useConfirm = () => {
  // pending = { options, resolve }；resolve 随请求一起存 state，避免在渲染期读 ref
  const [pending, setPending] = useState(null);

  const confirm = useCallback((nextOptions) => {
    const options = typeof nextOptions === 'string' ? { message: nextOptions } : nextOptions;
    return new Promise((resolve) => {
      setPending((previous) => {
        // 已有未决弹窗时先按取消结算，避免 Promise 悬空
        previous?.resolve(false);
        return { options, resolve };
      });
    });
  }, []);

  const settle = useCallback((value) => {
    setPending((previous) => {
      previous?.resolve(value);
      return null;
    });
  }, []);

  const confirmDialog = pending
    ? createElement(ConfirmDialog, {
      ...pending.options,
      onConfirm: () => settle(true),
      onCancel: () => settle(false)
    })
    : null;

  return { confirm, confirmDialog };
};

export default useConfirm;
