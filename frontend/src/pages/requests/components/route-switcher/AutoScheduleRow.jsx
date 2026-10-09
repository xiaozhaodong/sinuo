// 「自动调度」行：当前为自动时打勾；否则单击即恢复自动（一次点击一次写入）
// 2026-10-07 桌面化：浮层 26px 行，整行可点，不再单独放「恢复自动」按钮
import { RotateCcw } from 'lucide-react';
import { CheckSlot } from '@components/desktop/PopoverParts.jsx';

const AutoScheduleRow = ({ isCurrent, onRestore, loading = false, disabled = false, label = '自动调度', meta = '', title, indent = false, flash = false }) => (
  <button
    type="button"
    aria-pressed={isCurrent}
    disabled={loading || disabled}
    title={title}
    onClick={() => { if (!isCurrent) onRestore?.(); }}
    className={`flex h-[26px] w-full items-center gap-1.5 rounded-[5px] pr-2 text-left hover:bg-surface-mut disabled:opacity-50 ${indent ? 'pl-[22px]' : 'pl-1.5'}`}
  >
    <CheckSlot checked={isCurrent} />
    <RotateCcw size={13} className="shrink-0 text-fg-muted" />
    <span className={`min-w-0 flex-1 truncate transition-colors duration-700 ${isCurrent ? 'font-medium' : ''} ${flash ? 'text-accent' : isCurrent ? 'text-fg' : 'text-fg-body'}`}>{label}</span>
    {meta && <span className="shrink-0 text-[11px] text-fg-subtle">{meta}</span>}
  </button>
);

export default AutoScheduleRow;
