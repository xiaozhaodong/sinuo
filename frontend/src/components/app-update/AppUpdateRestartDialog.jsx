import { useId, useRef } from 'react';
import { createPortal } from 'react-dom';
import { RefreshCw } from 'lucide-react';
import useModalLifecycle from '@hooks/useModalLifecycle.js';
import { Button } from '@components/ui';

const AppUpdateRestartDialog = ({ activeRequests, onChoose }) => {
  const dialogRef = useRef(null);
  const messageId = useId();
  const cancel = () => onChoose('');
  useModalLifecycle({ open: true, onClose: cancel, initialFocusRef: dialogRef, focusTrapRef: dialogRef });
  return createPortal(
    <div className="fixed inset-0 z-[70] flex items-start justify-center px-4 pt-[18vh]">
      <button type="button" tabIndex={-1} aria-label="取消" className="motion-overlay absolute inset-0 bg-overlay" onClick={cancel} />
      <div ref={dialogRef} tabIndex={-1} role="alertdialog" aria-modal="true" aria-label="重启以完成更新" aria-describedby={messageId}
        className="motion-dialog relative w-full max-w-[400px] rounded-xl border border-line bg-surface p-5 shadow-2xl ring-1 ring-hairline focus:outline-none">
        <RefreshCw size={28} strokeWidth={1.75} className="text-accent-fg" />
        <h3 className="mt-3 text-[13px] font-semibold text-fg">重启以完成更新</h3>
        <p id={messageId} className="selectable mt-1.5 text-xs leading-relaxed text-fg-body">仍有 {activeRequests} 个请求进行中。立即重启会中断这些请求。</p>
        <div className="mt-5 flex flex-wrap justify-end gap-2">
          <Button type="button" variant="secondary" onClick={cancel}>取消</Button>
          <Button type="button" variant="secondary" onClick={() => onChoose('now')}>立即重启</Button>
          <Button type="button" onClick={() => onChoose('wait')}>等待完成后重启</Button>
        </div>
      </div>
    </div>, document.body
  );
};

export default AppUpdateRestartDialog;
