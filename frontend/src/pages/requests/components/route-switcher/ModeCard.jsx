const ModeCard = ({ card, selected, meta, onSelect, disabled = false }) => {
  const { Icon } = card;
  return (
    <button
      type="button"
      role="radio"
      aria-checked={selected}
      disabled={disabled}
      onClick={onSelect}
      className={`flex flex-col items-start gap-1 rounded-xl border p-3 text-left transition-colors disabled:cursor-not-allowed disabled:opacity-60 ${selected ? 'border-accent-ring bg-accent-soft/50 ring-1 ring-accent-ring/20' : 'border-line hover:border-line-strong hover:bg-surface-sub'}`}
    >
      <span className="flex w-full items-center gap-2">
        <span className={`flex h-4 w-4 shrink-0 items-center justify-center rounded-full border ${selected ? 'border-accent-ring' : 'border-line-strong'}`}>
          {selected && <span className="h-2 w-2 rounded-full bg-accent-ring" />}
        </span>
        <Icon size={14} className={selected ? 'text-accent' : 'text-fg-subtle'} />
        <span className={`text-sm font-semibold ${selected ? 'text-fg' : 'text-fg-body'}`}>{card.label}</span>
      </span>
      <span className="pl-6 text-[11px] leading-relaxed text-fg-muted">{card.shortHint}</span>
      <span className="pl-6 text-[11px] text-fg-subtle">{meta}</span>
    </button>
  );
};

export default ModeCard;
