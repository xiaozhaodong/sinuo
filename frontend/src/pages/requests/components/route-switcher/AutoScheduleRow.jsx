import { Check, RotateCcw } from 'lucide-react';

const AutoScheduleRow = ({ isCurrent, onRestore, loading = false, disabled = false, label = '自动调度' }) => (
  <>
    <div className={`flex h-9 items-center gap-2 rounded-lg px-2 ${isCurrent ? 'bg-surface-sub' : ''}`}>
      <RotateCcw size={13} className="shrink-0 text-fg-subtle" />
      <span className={`flex-1 text-xs ${isCurrent ? 'font-medium text-fg' : 'text-fg-body'}`}>{label}</span>
      {isCurrent
        ? <span className="tone-slate inline-flex items-center gap-1 rounded border px-1.5 py-px text-[10px] font-medium"><Check size={10} />当前</span>
        : <button type="button" disabled={loading || disabled} onClick={onRestore} className="inline-flex h-6 items-center gap-1 rounded-md border border-line bg-surface px-2 text-[11px] text-fg-muted hover:border-accent-line hover:text-accent disabled:opacity-50">恢复自动</button>}
    </div>
    <div className="mx-2 my-1.5 border-t border-line-soft" />
  </>
);

export default AutoScheduleRow;
