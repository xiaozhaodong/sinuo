import { Plus, Trash2 } from 'lucide-react';
import { FormSection } from '@components/desktop/FormSection.jsx';
import { Button } from '@components/ui';

const columns = 'grid items-center gap-x-3 px-3 grid-cols-[minmax(0,1fr)_minmax(0,1fr)_1.5rem] @sm:grid-cols-[minmax(5rem,10rem)_minmax(4.5rem,6rem)_minmax(6rem,8.5rem)_1.5rem] @2xl:grid-cols-[10rem_6rem_8.5rem_minmax(0,1fr)_1.5rem]';
const timeField = 'desk-field w-full min-w-0 max-w-24 tabular-nums';

function dayPercent(time) {
  const [hours, minutes] = time.split(':').map(Number);
  return (hours * 60 + minutes) / 1440 * 100;
}

function DayCoverage({ start, end, fullDay }) {
  const known = start && end && (fullDay || start !== end);
  let spans = [];
  if (known) {
    const from = dayPercent(start);
    const to = dayPercent(end);
    spans = fullDay ? [[0, 100]] : end < start ? [[0, to], [from, 100]] : [[from, to]];
  }
  return (
    <div aria-hidden="true" className="relative hidden h-2 overflow-hidden rounded-sm bg-line-soft @2xl:block">
      {spans.map(([from, to]) => (
        <span key={from} className="absolute inset-y-0 bg-accent/65" style={{ left: `${from}%`, width: `${to - from}%` }} />
      ))}
      <span className="absolute inset-y-0 left-1/2 border-l border-surface/80" />
    </div>
  );
}

export default function RatePeriods({ periods, nextIndices, onName, onStart, onEnd, onAdd, onRemove }) {
  return (
    <FormSection
      title="计费时段"
      description={periods.length ? '结束时间与下一段开始时间联动。' : '全天使用同一时段。'}
      actions={<Button type="button" size="sm" variant="ghost" icon={Plus} onClick={onAdd}>添加时段</Button>}
    >
      {periods.length > 0 && (
        <div aria-hidden="true" className={`${columns} hidden rounded-t-[10px] bg-surface-mut/60 py-1.5 text-xs text-fg-muted @sm:grid`}>
          <span>名称</span><span>开始时间</span><span>结束时间</span>
          <div className="hidden justify-between text-[11px] tabular-nums @2xl:flex">
            <span>00:00</span><span>12:00</span><span>24:00</span>
          </div>
        </div>
      )}
      {periods.map((period, index) => {
        const next = nextIndices[index];
        const end = periods[next].time;
        const nextDay = end && period.time && (index === next || end < period.time);
        return (
          <div key={period.key} className={`${columns} gap-y-2 py-2 text-xs`}>
            <label className="col-span-2 block min-w-0 @sm:col-span-1">
              <span className="mb-1 block text-fg-muted @sm:sr-only">名称</span>
              <input aria-label={`时段 ${index + 1} 名称`} className="desk-field w-full max-w-40 min-w-0"
                value={period.name} onChange={(e) => onName(index, e.target.value)} />
            </label>
            <label className="col-start-1 row-start-2 min-w-0 @sm:col-start-2 @sm:row-start-1">
              <span className="mb-1 block text-fg-muted @sm:sr-only">开始时间</span>
              <input aria-label={`时段 ${index + 1} 开始时间`} type="time" required className={timeField}
                value={period.time} onChange={(e) => onStart(index, e.target.value)} />
            </label>
            <label className="col-span-2 col-start-2 row-start-2 min-w-0 @sm:col-span-1 @sm:col-start-3 @sm:row-start-1">
              <span className="mb-1 block text-fg-muted @sm:sr-only">结束时间</span>
              <span className="flex items-center gap-1.5">
                <input aria-label={`时段 ${index + 1} 结束时间${nextDay ? '（次日）' : ''}`} type="time" required className={timeField}
                  value={end} onChange={(e) => onEnd(next, e.target.value)} />
                {nextDay && <span className="shrink-0 text-[11px] text-fg-muted">次日</span>}
              </span>
            </label>
            <DayCoverage start={period.time} end={end} fullDay={periods.length === 1} />
            <button type="button" className="col-start-3 row-start-1 justify-self-end rounded p-1 text-fg-muted hover:text-danger @sm:col-start-4 @2xl:col-start-5"
              aria-label={`移除时段 ${index + 1} 及对应费率`} onClick={() => onRemove(index)}>
              <Trash2 size={14} />
            </button>
          </div>
        );
      })}
    </FormSection>
  );
}
