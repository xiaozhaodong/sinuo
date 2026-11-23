// ============================================
// 账号池删除确认弹窗（统一收敛至 340px AlertDialog）
// 2026-03-07
// ============================================

import { AlertTriangle, Trash2 } from 'lucide-react';
import { Button } from '@components/ui';
import { AlertDialog } from '@components/ui/ConfirmDialog.jsx';
import { resolveAccountId } from '../utils.js';

const DeleteAccountDialog = ({ deleteTarget, busyKey = '', onClose, onSubmit }) => {
  const deleteId = deleteTarget ? resolveAccountId(deleteTarget) : null;
  const submitting = deleteTarget ? busyKey === `account-delete-${deleteId}` : false;

  if (!deleteTarget) return null;

  const handleSubmit = (event) => {
    event.preventDefault();
    if (!submitting) onSubmit?.(event);
  };

  return (
    <AlertDialog open={Boolean(deleteTarget)} label="确认删除账号" busy={submitting} closeOnBackdrop={false} onClose={onClose}>
      <form onSubmit={handleSubmit}>
        <AlertTriangle className="text-danger" size={28} strokeWidth={1.75} />
        <h3 className="mt-3 text-[13px] font-semibold text-fg">确认删除账号</h3>
        <p className="mt-1.5 text-xs text-fg-muted">此操作不可撤销。</p>
        <p className="selectable mt-3 text-xs leading-relaxed text-fg-body">
          确认删除账号
          <span className="mx-1 font-semibold">
            {`「${deleteTarget?.account_name || deleteTarget?.accountName || '-'}」`}
          </span>
          吗？
        </p>

        <div className="mt-5 flex justify-end gap-2">
          <Button type="button" variant="secondary" onClick={onClose} disabled={submitting}>取消</Button>
          <Button type="submit" variant="danger" icon={Trash2} loading={submitting}>确认删除</Button>
        </div>
      </form>
    </AlertDialog>
  );
};

export default DeleteAccountDialog;
