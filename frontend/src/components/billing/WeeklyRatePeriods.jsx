import { Plus, Trash2 } from 'lucide-react';
import { FormSection } from '@components/desktop/FormSection.jsx';
import { Button } from '@components/ui';
import { newWeeklyWindow } from './billingForms.js';

const days = ['一', '二', '三', '四', '五', '六', '日'];

export default function WeeklyRatePeriods({ periods, onChange, onAdd, onRemove }) {
  return (
    <FormSection title="每周计费窗口" description="窗口以价表时区为准，其余时间使用默认档。"
      actions={<Button type="button" size="sm" variant="ghost" icon={Plus} onClick={onAdd}>添加价格档</Button>}>
      <div className="divide-y divide-line-soft">
        {periods.map((p, index) => {
          const periodLabel = `价格档 ${p.name.trim() || index}`;
          const updateWindows = (windows) => onChange(index, { windows });
          const updateWindow = (i, patch) => updateWindows(p.windows.map((w, n) => n === i ? { ...w, ...patch } : w));
          return (
            <div key={p.key} className="space-y-2 px-3 py-2.5">
              <div className="flex flex-wrap items-center gap-2">
                <label className="flex items-center gap-2 text-xs text-fg-muted">
                  {p.default ? '默认档' : '价格档'}
                  <input className="desk-field w-36" aria-label={p.default ? '默认档名称' : `价格档 ${index} 名称`}
                    value={p.name} onChange={(event) => onChange(index, { name: event.target.value })} />
                </label>
                {!p.default && <>
                  <Button type="button" size="sm" variant="ghost" icon={Plus}
                    onClick={() => updateWindows([...p.windows, newWeeklyWindow()])}>添加窗口</Button>
                  <button type="button" className="ml-auto p-1 text-fg-muted hover:text-danger"
                    aria-label={`移除${periodLabel}`} onClick={() => onRemove(index)}><Trash2 size={14} /></button>
                </>}
              </div>
              {p.windows.map((w, i) => (
                <fieldset key={i} className="min-w-0 py-1">
                  <legend className="sr-only">{periodLabel} 窗口 {i + 1}</legend>
                  <div className="flex flex-wrap items-center gap-x-3 gap-y-2">
                    <div className="inline-flex gap-0.5" role="group" aria-label="生效星期">
                      {days.map((label, dayIndex) => {
                        const day = dayIndex + 1;
                        const selected = w.weekdays.includes(day);
                        return <button key={day} type="button" aria-label={`星期${label}`} aria-pressed={selected}
                          className={`h-7 w-7 rounded text-xs ${selected ? 'bg-accent/15 font-semibold text-accent' : 'bg-surface-mut text-fg-muted'}`}
                          onClick={() => updateWindow(i, { weekdays: selected ? w.weekdays.filter((d) => d !== day) : [...w.weekdays, day].sort((a, b) => a - b) })}>
                          {label}
                        </button>;
                      })}
                    </div>
                    <div className="inline-flex items-center gap-1 text-xs text-fg-muted">
                      <input aria-label="窗口开始时间" type="time" className="desk-field tabular-nums" value={w.start}
                        onChange={(event) => updateWindow(i, { start: event.target.value })} />
                      <span>至</span>
                      <input aria-label="窗口结束时间" type="time" className="desk-field tabular-nums" value={w.end}
                        onChange={(event) => updateWindow(i, { end: event.target.value })} />
                      {w.start && w.end && (w.end < w.start || w.end === '00:00') && <span>次日</span>}
                    </div>
                    <button type="button" aria-label={`移除窗口 ${i + 1}`} className="p-1 text-fg-muted hover:text-danger"
                      onClick={() => updateWindows(p.windows.filter((_, n) => i !== n))}><Trash2 size={13} /></button>
                  </div>
                </fieldset>
              ))}
            </div>
          );
        })}
      </div>
    </FormSection>
  );
}
