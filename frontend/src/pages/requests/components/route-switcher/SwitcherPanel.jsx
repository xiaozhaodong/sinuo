import { createPortal } from 'react-dom';
import { RefreshCw } from 'lucide-react';
import PageTabs from '@components/layout/PageTabs.jsx';

const SwitcherPanel = ({
  open,
  position,
  panelRef,
  panelId,
  title,
  headerStatus = null,
  tabs = null,
  activeTab,
  onTabChange,
  refresh = null,
  refreshing = false,
  children
}) => {
  if (!open || !position) return null;

  return createPortal(
    <div
      ref={panelRef}
      id={panelId}
      style={position}
      className="fixed z-50 overflow-y-auto rounded-xl border border-line bg-surface shadow-xl ring-1 ring-hairline motion-popover origin-top-left"
    >
      <div className="flex items-center gap-3 border-b border-line-soft px-4 py-2.5">
        <h2 className="text-sm font-semibold text-fg">{title}</h2>
        {headerStatus}
        {tabs && <PageTabs tabs={tabs} activeTab={activeTab} onChange={onTabChange} idPrefix={`${panelId}-tabs`} ariaLabel={`${title}页签`} size="sm" />}
        {refresh && (
          <button
            type="button"
            onClick={refresh}
            disabled={refreshing}
            title="刷新当前状态"
            aria-label="刷新当前状态"
            className="ml-auto rounded-lg p-1.5 text-fg-muted hover:bg-surface-sub hover:text-fg-body disabled:opacity-50"
          >
            <RefreshCw size={14} className={refreshing ? 'animate-spin' : ''} />
          </button>
        )}
      </div>
      {children}
    </div>,
    document.body
  );
};

export default SwitcherPanel;
