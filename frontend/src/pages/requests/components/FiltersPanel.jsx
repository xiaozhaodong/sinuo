import { useMemo } from 'react';
import { Calendar, Check, Filter, X } from 'lucide-react';
import { CustomSelect } from '@components/ui';
import { SOURCE_VIEW_OPTIONS, STATUS_SELECT_OPTIONS } from '../utils/constants.js';
import { useTimezone } from '@contexts/TimezoneContext.jsx';
import { formatFilterDateTimeInput } from '../utils/filterTime.js';

const FAMILY_OPTIONS = [
  { value: '', label: '全部类型' },
  { value: 'claude', label: 'Claude' },
  { value: 'codex', label: 'Codex' },
  { value: 'image', label: 'Image' },
  { value: 'other', label: 'Other' }
];

const FiltersPanel = ({ isOpen, onClose, filters, updateFilter, onApply, onReset, models = [], upstreams = [] }) => {
  const { timezone } = useTimezone();
  const modelOptions = useMemo(() => [
    { value: '', label: '所有模型' },
    ...models.map((model) => {
      const name = typeof model === 'string' ? model : (model.model_name || model.name || '');
      return { value: name, label: name };
    }).filter((option) => option.value)
  ], [models]);

  const upstreamOptions = useMemo(() => [
    { value: '', label: '所有上游' },
    ...upstreams
  ], [upstreams]);

  if (!isOpen) return null;
  return (
    <div className="absolute left-0 right-0 z-20 mt-3 rounded-xl border border-line bg-surface p-5 shadow-xl ring-1 ring-hairline">
      <div className="mb-5 flex items-start justify-between">
        <h3 className="flex items-center gap-2 text-sm font-semibold text-fg"><Filter className="h-4 w-4 text-accent" />高级筛选</h3>
        <button type="button" onClick={onClose} className="text-fg-subtle hover:text-fg-muted" aria-label="关闭筛选"><X className="h-4 w-4" /></button>
      </div>
      <div className="space-y-4">
        <div className="flex flex-wrap items-center gap-3">
          <label className="min-w-[80px] text-sm font-medium text-fg-body">时间范围:</label>
          {['startDate', 'endDate'].map((field, index) => (
            <div key={field} className="flex items-center gap-2">
              {index === 1 && <span className="text-sm text-fg-subtle">至</span>}
              <div className="relative">
                <Calendar className="absolute left-2.5 top-1/2 h-4 w-4 -translate-y-1/2 text-fg-subtle" />
                <input type="datetime-local" step="any" aria-label={index === 0 ? '开始时间' : '结束时间'} value={formatFilterDateTimeInput(filters[field], timezone)} onChange={(event) => updateFilter(field, event.target.value)} className="w-[250px] rounded-lg border border-line bg-surface py-1.5 pl-9 pr-2 text-sm text-fg-body focus:border-accent-ring focus:ring-2 focus:ring-accent-ring/20" />
              </div>
            </div>
          ))}
        </div>
        <div className="grid gap-3 md:grid-cols-2 xl:grid-cols-5">
          <CustomSelect options={STATUS_SELECT_OPTIONS} value={filters.status} onChange={(value) => updateFilter('status', value)} size="sm" placeholder="状态" />
          <CustomSelect options={modelOptions} value={filters.model} onChange={(value) => updateFilter('model', value)} size="sm" placeholder="模型" />
          <CustomSelect options={FAMILY_OPTIONS} value={filters.requestFamily === 'all' ? '' : filters.requestFamily} onChange={(value) => { updateFilter('requestFamily', value || 'all'); updateFilter('upstreamName', 'all'); }} size="sm" placeholder="类型" />
          <CustomSelect options={SOURCE_VIEW_OPTIONS} value={filters.sourceView || 'all'} onChange={(value) => { updateFilter('sourceView', value); updateFilter('upstreamName', 'all'); }} size="sm" placeholder="来源" />
          <CustomSelect options={upstreamOptions} value={filters.upstreamName === 'all' ? '' : filters.upstreamName} onChange={(value) => updateFilter('upstreamName', value || 'all')} size="sm" placeholder="上游" />
        </div>
      </div>
      <div className="mt-4 flex items-center justify-between border-t border-line-soft pt-4">
        <button type="button" onClick={onReset} className="text-sm text-fg-muted hover:text-fg">重置所有条件</button>
        <div className="flex gap-2"><button type="button" onClick={onClose} className="rounded-lg border border-line bg-surface px-4 py-2 text-sm text-fg-body hover:bg-surface-sub">取消</button><button type="button" onClick={() => { onApply?.(); onClose?.(); }} className="flex items-center gap-2 rounded-lg bg-inverted px-4 py-2 text-sm text-fg-inverted shadow-md hover:opacity-90"><Check className="h-3.5 w-3.5" />应用筛选</button></div>
      </div>
    </div>
  );
};

export default FiltersPanel;
