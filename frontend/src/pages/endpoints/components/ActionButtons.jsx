import { LockKeyhole, Route, RotateCcw } from 'lucide-react';

const ActionButtons = ({ endpointName, routingState = {}, onSetRouting, disabled = false, disabledReason = '' }) => {
  const mode = routingState.mode || 'auto';
  const target = routingState.endpointName || routingState.endpoint_name || '';
  const selected = target === endpointName;

  const buttonClass = (active, tone = 'indigo') => {
    const activeClass = tone === 'rose' ? 'tone-rose' : 'tone-indigo';
    return `inline-flex items-center gap-1 rounded-md border px-2 py-1 text-[11px] font-medium transition disabled:cursor-not-allowed disabled:opacity-50 ${active ? activeClass : 'border-line bg-surface text-fg-muted hover:border-line-strong hover:text-fg-body'}`;
  };

  return (
    <div className="flex flex-wrap items-center justify-end gap-1" title={disabled && disabledReason ? disabledReason : undefined}>
      <button type="button" disabled={disabled} onClick={() => onSetRouting?.('manual_preferred', endpointName)} className={buttonClass(selected && mode === 'manual_preferred')} title="请求优先使用此端点；失败时允许 fallback"><Route size={12} />优选</button>
      <button type="button" disabled={disabled} onClick={() => onSetRouting?.('manual_fixed', endpointName)} className={buttonClass(selected && mode === 'manual_fixed', 'rose')} title="严格固定此端点，不做 fallback"><LockKeyhole size={12} />固定</button>
      {selected && mode !== 'auto' && <button type="button" disabled={disabled} onClick={() => onSetRouting?.('auto', '')} className={buttonClass(false)} title="恢复自动调度"><RotateCcw size={12} />自动</button>}
    </div>
  );
};

export default ActionButtons;
