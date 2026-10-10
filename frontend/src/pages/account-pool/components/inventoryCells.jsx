const STATE_DOT_CLASS = {
  emerald: 'bg-success-solid',
  amber: 'bg-warn-solid',
  rose: 'bg-danger-solid',
  slate: 'bg-fg-subtle'
};

const STATE_TEXT_CLASS = {
  emerald: 'text-fg-body',
  amber: 'text-warn',
  rose: 'text-danger',
  slate: 'text-fg-subtle'
};

const quotaBarClass = (percent) => {
  if (percent > 50) return 'bg-success-solid';
  if (percent > 20) return 'bg-warn-solid';
  return 'bg-danger-solid';
};

// 剩余额度：有百分比时画 40px 细条 + 数字；无限额 / 未刷新只给文字，free 账号没有 5h 窗口显示占位
const QuotaMeter = ({ text, percent, title }) => {
  if (!Number.isFinite(percent)) {
    const label = !text || text === '-' || text === '无' ? '—' : text;
    return <span className={`text-xs ${text === '无限额' ? 'text-fg-muted' : 'text-fg-subtle'}`} title={title}>{label}</span>;
  }
  const clamped = Math.max(Math.min(percent, 100), 0);
  return (
    <span className="inline-flex items-center gap-1.5 text-xs tabular-nums text-fg-body" title={title}>
      <span className="h-1 w-10 overflow-hidden rounded-full bg-surface-emph">
        <span className={`block h-full rounded-full ${quotaBarClass(clamped)}`} style={{ width: `${clamped}%` }} />
      </span>
      {Math.round(clamped)}%
    </span>
  );
};

export { QuotaMeter, STATE_DOT_CLASS, STATE_TEXT_CLASS };

