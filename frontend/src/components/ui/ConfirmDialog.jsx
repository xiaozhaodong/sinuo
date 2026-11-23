// ============================================
// ConfirmDialog / AlertDialog - 统一系统确认框与警告框外壳
// 2026-09-14
// ============================================
//
// Wails 的 macOS WKWebView 没有实现 runJavaScriptConfirmPanel，
// window.confirm 不弹窗且恒返回 false，所有依赖它的删除/重置操作在桌面端都会静默失效。
// 这里提供 portal + 轻遮罩的真模态替代（统一 z-[70]），配合 useConfirm 以 Promise 形式使用。

import { useId, useRef } from 'react';
import { createPortal } from 'react-dom';
import { AlertTriangle } from 'lucide-react';
import useModalLifecycle from '@hooks/useModalLifecycle.js';
import { Button } from './index.jsx';

export const AlertDialog = ({
  open = true,
  label,
  title,
  describedBy,
  width = 340,
  busy = false,
  closeOnBackdrop = true,
  role = 'alertdialog',
  initialFocusRef,
  onClose,
  children
}) => {
  const dialogRef = useRef(null);
  const handleRequestClose = () => {
    if (!busy) onClose?.();
  };

  // 焦点只由生命周期管理：先记住触发控件，再聚焦对话框，避免 autoFocus 抢先覆盖恢复目标。
  useModalLifecycle({
    open,
    onClose: handleRequestClose,
    initialFocusRef: initialFocusRef ?? dialogRef,
    focusTrapRef: dialogRef
  });

  if (!open || typeof document === 'undefined') return null;

  return createPortal(
    <div className="fixed inset-0 z-[70] flex items-start justify-center px-4 pt-[18vh]">
      {closeOnBackdrop ? (
        <button
          type="button"
          tabIndex={-1}
          aria-label="取消"
          className="motion-overlay absolute inset-0 bg-overlay"
          onClick={handleRequestClose}
        />
      ) : (
        <div aria-hidden="true" className="motion-overlay absolute inset-0 bg-overlay" />
      )}
      <div
        ref={dialogRef}
        tabIndex={-1}
        role={role}
        aria-modal="true"
        aria-busy={busy || undefined}
        aria-label={label ?? title}
        aria-describedby={describedBy}
        style={{ maxWidth: width }}
        className="motion-dialog relative max-h-[76vh] w-full overflow-y-auto rounded-xl border border-line bg-surface p-5 shadow-2xl ring-1 ring-hairline focus:outline-none"
      >
        {children}
      </div>
    </div>,
    document.body
  );
};

const ConfirmDialog = ({
  title = '请确认',
  message,
  confirmText = '确定',
  cancelText = '取消',
  danger = false,
  loading = false,
  onConfirm,
  onCancel
}) => {
  const messageId = useId();

  // 2026-10-07 桌面化：紧凑的系统提示框样式（340px 宽、图标在上、按钮右对齐）
  return (
    <AlertDialog title={title} describedBy={messageId} busy={loading} onClose={onCancel}>
      <AlertTriangle className={danger ? 'text-danger' : 'text-warn'} size={28} strokeWidth={1.75} />
      <h3 className="mt-3 text-[13px] font-semibold text-fg">{title}</h3>
      <p id={messageId} className="selectable mt-1.5 whitespace-pre-line text-xs leading-relaxed text-fg-body">{message}</p>

      <div className="mt-5 flex justify-end gap-2">
        <Button type="button" variant="secondary" onClick={onCancel} disabled={loading}>{cancelText}</Button>
        <Button type="button" variant={danger ? 'danger' : 'primary'} onClick={onConfirm} loading={loading}>{confirmText}</Button>
      </div>
    </AlertDialog>
  );
};

export default ConfirmDialog;
