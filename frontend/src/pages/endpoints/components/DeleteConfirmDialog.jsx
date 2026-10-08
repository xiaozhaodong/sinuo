// ============================================
// DeleteConfirmDialog - 删除确认对话框
// ============================================

import { useRef } from 'react';
import { createPortal } from 'react-dom';
import { AlertTriangle, Trash2 } from 'lucide-react';
import { Button } from '@components/ui';
import useModalLifecycle from '@hooks/useModalLifecycle.js';

const DeleteConfirmDialog = ({ endpoint, onConfirm, onCancel, loading }) => {
  // 挂载即打开（父组件条件渲染），卸载时由 hook 清理
  const dialogRef = useRef(null);
  useModalLifecycle({
    open: true,
    onClose: () => {
      if (!loading) onCancel();
    },
    initialFocusRef: dialogRef,
    focusTrapRef: dialogRef
  });

  return createPortal(
    <div className="fixed inset-0 z-[60] flex items-start justify-center px-4 pt-[18vh]">
      <div className="motion-overlay absolute inset-0 bg-overlay" />
      <div
        ref={dialogRef}
        tabIndex={-1}
        role="dialog"
        aria-modal="true"
        aria-label="确认删除端点"
        className="motion-dialog relative w-full max-w-[340px] rounded-xl border border-line bg-surface p-5 shadow-2xl ring-1 ring-hairline focus:outline-none"
      >
        <AlertTriangle className="text-danger" size={28} strokeWidth={1.75} />
        <h3 className="mt-3 text-[13px] font-semibold text-fg">确认删除</h3>
        <p className="mt-1.5 text-xs text-fg-muted">此操作不可撤销</p>

        <p className="selectable mt-3 text-xs leading-relaxed text-fg-body">
          确定要删除端点 <span className="font-semibold">&quot;{endpoint?.name}&quot;</span> 吗？
          删除后将无法恢复。
        </p>

        <div className="mt-5 flex justify-end gap-2">
          <Button variant="secondary" onClick={onCancel} disabled={loading}>
            取消
          </Button>
          <Button
            variant="danger"
            icon={Trash2}
            onClick={onConfirm}
            loading={loading}
          >
            确认删除
          </Button>
        </div>
      </div>
    </div>,
    document.body
  );
};

export default DeleteConfirmDialog;
