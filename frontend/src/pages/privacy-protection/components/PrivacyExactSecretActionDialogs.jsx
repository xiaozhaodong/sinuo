import { useRef, useState } from 'react';
import { AlertTriangle, Trash2 } from 'lucide-react';
import { Button } from '@components/ui';
import { AlertDialog } from '@components/ui/ConfirmDialog.jsx';

const CLEAR_CONFIRM_TEXT = '清空本地敏感值';

export const DeleteExactSecretDialog = ({ secret, loading, onConfirm, onClose }) => (
  <AlertDialog title="确认删除本地敏感值" busy={loading} onClose={onClose}>
    <AlertTriangle className="text-danger" size={28} strokeWidth={1.75} />
    <h3 className="mt-3 text-[13px] font-semibold text-fg">确认删除</h3>
    <p className="mt-1.5 text-xs text-fg-muted">删除后将立即停止匹配，且无法恢复</p>

    <p className="selectable mt-3 text-xs leading-relaxed text-fg-body">
      确定删除本地敏感值
      <span className="mx-1 font-semibold">「{secret?.name || '-'}」</span>
      吗？
    </p>

    <div className="mt-5 flex justify-end gap-2">
      <Button type="button" variant="secondary" onClick={onClose} disabled={loading}>
        取消
      </Button>
      <Button type="button" variant="danger" icon={Trash2} onClick={onConfirm} loading={loading}>
        确认删除
      </Button>
    </div>
  </AlertDialog>
);

export const ClearExactSecretsDialog = ({ loading, onConfirm, onClose }) => {
  const [confirmText, setConfirmText] = useState('');
  const confirmInputRef = useRef(null);
  const confirmed = confirmText.trim() === CLEAR_CONFIRM_TEXT;

  const handleSubmit = (event) => {
    event.preventDefault();
    if (confirmed && !loading) onConfirm(confirmText.trim());
  };

  return (
    <AlertDialog title="确认清空本地敏感值" busy={loading} onClose={onClose} width={400} initialFocusRef={confirmInputRef}>
      <form onSubmit={handleSubmit}>
        <AlertTriangle className="text-danger" size={28} strokeWidth={1.75} />
        <h3 className="mt-3 text-[13px] font-semibold text-fg">确认清空</h3>
        <p className="mt-1.5 text-xs text-fg-muted">所有本地敏感值都将被永久删除</p>

        <label className="mt-3 block text-xs text-fg-body">
          输入 <span className="font-semibold text-danger">{CLEAR_CONFIRM_TEXT}</span> 以确认：
          <input
            ref={confirmInputRef}
            autoComplete="off"
            spellCheck={false}
            value={confirmText}
            onChange={(event) => setConfirmText(event.target.value)}
            className="desk-field mt-2 w-full"
          />
        </label>

        <div className="mt-5 flex justify-end gap-2">
          <Button type="button" variant="secondary" onClick={onClose} disabled={loading}>
            取消
          </Button>
          <Button type="submit" variant="danger" icon={Trash2} loading={loading} disabled={!confirmed}>
            确认清空
          </Button>
        </div>
      </form>
    </AlertDialog>
  );
};
