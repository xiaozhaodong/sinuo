// 右侧抽屉共用外壳：定位到内容区，统一关闭与焦点恢复生命周期。
import { useRef } from 'react';
import { createPortal } from 'react-dom';
import { X } from 'lucide-react';
import useModalLifecycle from '@hooks/useModalLifecycle.js';
import { getContentAreaInsets } from '@components/layout/contentArea.js';

const SidePanel = ({
  open,
  onClose,
  label,
  title,
  subtitle,
  headerActions,
  footer,
  width = 640,
  layer = 45,
  closeOnOverlay = true,
  overlayLabel,
  initialFocusRef,
  children
}) => {
  const closeButtonRef = useRef(null);
  useModalLifecycle({ open, onClose, initialFocusRef: initialFocusRef ?? closeButtonRef });

  if (!open) return null;

  return createPortal(
    <div className="fixed inset-0 flex justify-end" style={{ ...getContentAreaInsets(), zIndex: layer }}>
      {closeOnOverlay ? (
        <button type="button" tabIndex={-1} aria-label={overlayLabel} onClick={onClose} className="motion-overlay absolute inset-0 bg-overlay" />
      ) : (
        <div aria-hidden="true" className="motion-overlay absolute inset-0 bg-overlay" />
      )}
      <aside
        role="dialog"
        aria-modal="true"
        aria-label={label}
        className="motion-drawer relative z-10 flex h-full w-full flex-col border-l border-line bg-pane shadow-xl"
        style={{ maxWidth: width }}
      >
        <div className="flex h-11 shrink-0 items-center gap-2 border-b border-line bg-surface px-4">
          <h2 className="shrink-0 text-[13px] font-semibold text-fg">{title}</h2>
          {subtitle && <span className="min-w-0 truncate text-xs text-fg-muted" title={subtitle}>{subtitle}</span>}
          <div className="ml-auto flex shrink-0 items-center gap-1">
            {headerActions}
            <button
              ref={closeButtonRef}
              type="button"
              aria-label="关闭抽屉"
              onClick={onClose}
              className="inline-flex h-6 w-6 items-center justify-center rounded text-fg-muted hover:bg-surface-mut hover:text-fg"
            >
              <X size={14} />
            </button>
          </div>
        </div>
        <div className="min-h-0 flex-1 overflow-y-auto px-4 pb-5">{children}</div>
        {footer && <div className="flex shrink-0 items-center gap-2 border-t border-line bg-surface px-4 py-2.5">{footer}</div>}
      </aside>
    </div>,
    document.body
  );
};

export default SidePanel;
