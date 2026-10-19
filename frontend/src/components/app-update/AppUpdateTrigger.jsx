import { useState } from 'react';
import { resolveUpdateTrigger } from './appUpdateView.js';
import AppUpdateDialog from './AppUpdateDialog.jsx';

const AppUpdateTrigger = ({ update, onOpenSettings }) => {
  const [open, setOpen] = useState(false);
  const trigger = resolveUpdateTrigger(update.status);

  if (!trigger) return null;
  const openSettings = () => {
    setOpen(false);
    onOpenSettings();
  };

  return (
    <>
      <button
        type="button"
        aria-haspopup="dialog"
        aria-expanded={open}
        title={trigger.title}
        onClick={() => setOpen((value) => !value)}
        className="flex h-7 shrink-0 items-center whitespace-nowrap rounded-md px-1 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent"
      >
        <span className={trigger.badge
          ? 'rounded-full bg-accent-soft px-1.5 text-[11px] font-medium text-accent-fg'
          : 'font-mono text-[11px] text-fg-subtle hover:text-fg-muted'}>{trigger.label}</span>
      </button>
      {open && <AppUpdateDialog update={update} onClose={() => setOpen(false)} onOpenSettings={openSettings} />}
    </>
  );
};

export default AppUpdateTrigger;
