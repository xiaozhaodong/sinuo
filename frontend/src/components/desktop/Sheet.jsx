// 编辑弹窗共用外壳：遮罩不关闭，Esc 与焦点限制由共享生命周期管理。
import { useRef } from 'react';
import { createPortal } from 'react-dom';
import useModalLifecycle from '@hooks/useModalLifecycle.js';

const noop = () => {};

const Sheet = ({
  open = true,
  onClose,
  label,
  title,
  subtitle,
  width = 560,
  footer,
  footerStart,
  onSubmit,
  closeDisabled = false,
  closeLabel,
  overlayLabel,
  initialFocusRef,
  children
}) => {
  const panelRef = useRef(null);
  useModalLifecycle({ open, onClose: closeDisabled ? noop : onClose, initialFocusRef, focusTrapRef: panelRef });

  if (!open) return null;

  const Panel = onSubmit ? 'form' : 'div';

  return createPortal(
    <div className="fixed inset-0 z-[60] flex items-start justify-center px-4 pt-[12vh]">
      <div aria-hidden="true" aria-label={overlayLabel} className="motion-overlay absolute inset-0 bg-overlay" />
      <Panel
        ref={panelRef}
        tabIndex={-1}
        role="dialog"
        aria-modal="true"
        aria-label={label ?? title}
        onSubmit={onSubmit}
        className="motion-dialog relative flex w-full max-h-[76vh] flex-col overflow-hidden rounded-xl border border-line bg-surface shadow-2xl ring-1 ring-hairline"
        style={{ maxWidth: width }}
      >
        <div className="flex h-11 shrink-0 items-center gap-2 border-b border-line px-4">
          <h2 className="shrink-0 text-[13px] font-semibold text-fg">{title}</h2>
          {subtitle && <span className="min-w-0 truncate text-xs text-fg-muted" title={subtitle}>{subtitle}</span>}
          <button
            type="button"
            aria-label={closeLabel}
            disabled={closeDisabled}
            onClick={onClose}
            className="ml-auto h-6 shrink-0 rounded px-2 text-xs text-fg-muted hover:bg-surface-mut hover:text-fg disabled:opacity-50"
          >
            关闭
          </button>
        </div>
        <div className="min-h-0 flex-1 overflow-y-auto bg-pane px-4 pb-4">{children}</div>
        <div className="flex shrink-0 items-center gap-2 border-t border-line bg-surface px-4 py-2.5">
          <div className="min-w-0 flex-1 text-xs">{footerStart}</div>
          <div className="flex shrink-0 items-center gap-2">{footer}</div>
        </div>
      </Panel>
    </div>,
    document.body
  );
};

export default Sheet;
