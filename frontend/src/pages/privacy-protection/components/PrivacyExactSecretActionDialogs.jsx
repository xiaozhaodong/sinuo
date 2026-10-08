import { useRef, useState } from 'react';
import { createPortal } from 'react-dom';
import { AlertTriangle, Trash2 } from 'lucide-react';
import { Button } from '@components/ui';
import useModalLifecycle from '@hooks/useModalLifecycle.js';

const CLEAR_CONFIRM_TEXT = '清空本地敏感值';

const DialogFrame = ({ title, loading, onClose, initialFocusRef, width = 340, children }) => {
  const dialogRef = useRef(null);
  const handleRequestClose = () => {
    if (!loading) onClose();
  };

  useModalLifecycle({
    open: true,
    onClose: handleRequestClose,
    initialFocusRef: initialFocusRef ?? dialogRef,
    focusTrapRef: dialogRef
  });

  return createPortal(
    <div className="fixed inset-0 z-50 flex items-start justify-center px-4 pt-[18vh]">
      <div className="motion-overlay absolute inset-0 bg-overlay" onClick={handleRequestClose} />
      <div
        ref={dialogRef}
        tabIndex={-1}
        role="dialog"
        aria-modal="true"
        aria-label={title}
        style={{ maxWidth: width }}
        className="motion-dialog relative w-full rounded-xl border border-line bg-surface p-5 shadow-2xl ring-1 ring-hairline focus:outline-none"
      >
        {children}
      </div>
    </div>,
    document.body
  );
};

export const DeleteExactSecretDialog = ({ secret, loading, onConfirm, onClose }) => (
  <DialogFrame title="确认删除本地敏感值" loading={loading} onClose={onClose}>
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
  </DialogFrame>
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
    <DialogFrame title="确认清空本地敏感值" loading={loading} onClose={onClose} width={400} initialFocusRef={confirmInputRef}>
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
    </DialogFrame>
  );
};
