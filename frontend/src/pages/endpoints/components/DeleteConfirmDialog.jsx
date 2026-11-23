// ============================================
// DeleteConfirmDialog - 端点删除确认对话框（统一复用 AlertDialog）
// ============================================

import { AlertTriangle, Trash2 } from 'lucide-react';
import { Button } from '@components/ui';
import { AlertDialog } from '@components/ui/ConfirmDialog.jsx';

const DeleteConfirmDialog = ({ endpoint, onConfirm, onCancel, loading }) => (
  <AlertDialog label="确认删除端点" busy={loading} closeOnBackdrop={false} onClose={onCancel}>
    <AlertTriangle className="text-danger" size={28} strokeWidth={1.75} />
    <h3 className="mt-3 text-[13px] font-semibold text-fg">确认删除</h3>
    <p className="mt-1.5 text-xs text-fg-muted">此操作不可撤销</p>

    <p className="selectable mt-3 text-xs leading-relaxed text-fg-body">
      确定要删除端点 <span className="font-semibold">&quot;{endpoint?.name}&quot;</span> 吗？
      删除后将无法恢复。
    </p>

    <div className="mt-5 flex justify-end gap-2">
      <Button type="button" variant="secondary" onClick={onCancel} disabled={loading}>
        取消
      </Button>
      <Button
        type="button"
        variant="danger"
        icon={Trash2}
        onClick={onConfirm}
        loading={loading}
      >
        确认删除
      </Button>
    </div>
  </AlertDialog>
);

export default DeleteConfirmDialog;
