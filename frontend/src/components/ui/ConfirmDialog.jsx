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

  // 2026-10-07 桌面化：紧凑的系统提示框样式（340px 宽、图标在上、按钮右对齐）
  return createPortal(
    <div className="fixed inset-0 z-[70] flex items-start justify-center px-4 pt-[18vh]">
      <button type="button" tabIndex={-1} aria-label="取消" className="motion-overlay absolute inset-0 bg-overlay" onClick={onCancel} />
      <div
        ref={dialogRef}
        tabIndex={-1}
        role="alertdialog"
        aria-modal="true"
        aria-label={title}
        aria-describedby={messageId}
        className="motion-dialog relative w-full max-w-[340px] rounded-xl border border-line bg-surface p-5 shadow-2xl ring-1 ring-hairline focus:outline-none"
      >
        <AlertTriangle className={danger ? 'text-danger' : 'text-warn'} size={28} strokeWidth={1.75} />
        <h3 className="mt-3 text-[13px] font-semibold text-fg">{title}</h3>
        <p id={messageId} className="selectable mt-1.5 whitespace-pre-line text-xs leading-relaxed text-fg-body">{message}</p>

        <div className="mt-5 flex justify-end gap-2">
          <Button type="button" variant="secondary" onClick={onCancel}>{cancelText}</Button>
          <Button type="button" variant={danger ? 'danger' : 'primary'} onClick={onConfirm}>{confirmText}</Button>
        </div>
      </div>
    </div>,
    document.body
  );
};

export default ConfirmDialog;
