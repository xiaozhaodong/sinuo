import { ChevronDown, Loader2 } from 'lucide-react';

const SwitcherTrigger = ({
  brand,
  label,
  warn = false,
  busy = false,
  badge = null,
  open = false,
  triggerRef,
  menuId,
  ariaLabel,
  title,
  onClick
}) => (
  <button
    ref={triggerRef}
    type="button"
    aria-expanded={open}
    aria-controls={menuId}
    aria-label={ariaLabel || `${brand}：${label}`}
    title={title}
    onClick={onClick}
    className="flex h-9 w-[170px] shrink-0 items-center gap-1.5 rounded-lg border border-line bg-surface px-3 text-sm shadow-sm hover:border-accent-line lg:w-[190px] xl:w-[220px]"
  >
    {busy ? <Loader2 size={13} className="shrink-0 animate-spin text-accent" /> : <span className={`h-1.5 w-1.5 shrink-0 rounded-full ${warn ? 'bg-warn-solid' : 'bg-success-solid'}`} />}
    <span className="shrink-0 font-semibold text-fg">{brand}</span>
    <span className={`min-w-0 flex-1 truncate text-left font-medium ${warn ? 'text-warn' : 'text-fg-muted'}`}>{label}</span>
    {badge && <span className={`shrink-0 text-[10px] font-semibold ${badge.toneClass}`}>{badge.text}</span>}
    <ChevronDown size={14} className={`shrink-0 text-fg-subtle transition-transform ${open ? 'rotate-180' : ''}`} />
  </button>
);

export default SwitcherTrigger;
