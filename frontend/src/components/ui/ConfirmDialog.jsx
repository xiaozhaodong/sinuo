// ============================================
// ConfirmDialog - 通用确认弹窗
// 2026-09-14
// ============================================
//
// Wails 的 macOS WKWebView 没有实现 runJavaScriptConfirmPanel，
// window.confirm 不弹窗且恒返回 false，所有依赖它的删除/重置操作在桌面端都会静默失效。
// 这里提供 portal + 轻遮罩的真模态替代，配合 useConfirm 以 Promise 形式使用。

import { useId, useRef } from 'react';
import { createPortal } from 'react-dom';
import { AlertTriangle } from 'lucide-react';
import useModalLifecycle from '@hooks/useModalLifecycle.js';
import { Button } from './index.jsx';

const ConfirmDialog = ({
  title = '请确认',
  message,
  confirmText = '确定',
  cancelText = '取消',
  danger = false,
  onConfirm,
  onCancel
}) => {
  // 挂载即打开（父组件条件渲染），卸载时由 hook 清理
  const dialogRef = useRef(null);
  const messageId = useId();
  // 焦点只由生命周期管理：先记住触发控件，再聚焦对话框，避免 autoFocus 抢先覆盖恢复目标。
  useModalLifecycle({ open: true, onClose: onCancel, initialFocusRef: dialogRef, focusTrapRef: dialogRef });

  return createPortal(
    <div className="fixed inset-0 z-[70] flex items-start justify-center px-4 pt-[20vh] animate-fade-in">
      <button type="button" tabIndex={-1} aria-label="取消" className="absolute inset-0 bg-overlay" onClick={onCancel} />
      <div
        ref={dialogRef}
        tabIndex={-1}
        role="alertdialog"
        aria-modal="true"
        aria-label={title}
        aria-describedby={messageId}
        className="relative bg-surface rounded-2xl border border-line shadow-2xl w-full max-w-md p-6 focus:outline-none"
      >
        <div className="flex items-center gap-3 mb-4">
          <div className={`p-3 rounded-full ${danger ? 'bg-danger-soft' : 'bg-warn-soft'}`}>
            <AlertTriangle className={danger ? 'text-danger' : 'text-warn'} size={24} />
          </div>
          <h3 className="text-lg font-semibold text-fg">{title}</h3>
        </div>

        <p id={messageId} className="text-fg-body mb-6 whitespace-pre-line">{message}</p>

        <div className="flex justify-end gap-3">
          <Button type="button" variant="ghost" onClick={onCancel}>{cancelText}</Button>
          <Button type="button" variant={danger ? 'danger' : 'primary'} onClick={onConfirm}>{confirmText}</Button>
        </div>
      </div>
    </div>,
    document.body
  );
};

export default ConfirmDialog;
