// ============================================
// FilterBar - 请求页筛选条（工具栏下方）
// 2026-10-07 桌面化：替代「弹出面板 + 应用筛选」。每个条件一个小按钮，选了立即生效；
//            生效的条件高亮并带 ×；时间按钮设置自定义区间（今天 / 7 天 / 30 天在工具栏）
// ============================================

import { useMemo, useRef, useState } from 'react';
import { createPortal } from 'react-dom';
import { ChevronDown, X } from 'lucide-react';
import { Button } from '@components/ui';
import useContextMenu from '@components/desktop/useContextMenu.jsx';
import useAnchoredPanel from '@hooks/useAnchoredPanel.js';
import { useTimezone } from '@contexts/TimezoneContext.jsx';
import { SOURCE_VIEW_OPTIONS, STATUS_SELECT_OPTIONS } from '../utils/constants.js';
import { buildCustomRangePatch, formatFilterDateTimeInput } from '../utils/filterTime.js';
import { countActiveChips, describeTimeChip, isChipActive, isValidCustomRange } from '../utils/filterBar.js';

const FAMILY_OPTIONS = [
  { value: 'all', label: '全部' },
  { value: 'claude', label: 'Claude' },
  { value: 'codex', label: 'Codex' },
  { value: 'image', label: 'Image' },
  { value: 'other', label: 'Other' }
];

// 「全部××」在按钮上只写「全部」，菜单里保留完整说法
const shortAllLabel = (label) => (String(label).startsWith('全部') || String(label).startsWith('所有') ? '全部' : label);

const CHIP = 'inline-flex h-[22px] shrink-0 items-center whitespace-nowrap rounded-[5px] border text-xs';
const chipTone = (active) => (active
  ? 'border-accent-line bg-accent-soft text-accent-fg'
  : 'border-line bg-surface text-fg-body hover:bg-surface-sub');

const FilterChip = ({ label, valueLabel, active, onOpen, onClear }) => (
  <span className={`${CHIP} ${chipTone(active)}`}>
    <button
      type="button"
      aria-haspopup={onOpen ? 'menu' : undefined}
      onClick={onOpen}
      className={`inline-flex h-full max-w-[220px] items-center gap-1 pl-2 ${active ? 'pr-0.5' : 'pr-1.5'}`}
    >
      <span className={active ? 'opacity-70' : 'text-fg-muted'}>{label}</span>
      <span className="truncate">{valueLabel}</span>
      {!active && onOpen && <ChevronDown size={11} className="shrink-0 opacity-60" />}
    </button>
    {active && (
      <button type="button" aria-label={`移除「${label}」条件`} title="移除此条件" onClick={onClear} className="inline-flex h-full items-center pl-0.5 pr-1.5 opacity-70 hover:opacity-100">
        <X size={11} />
      </button>
    )}
  </span>
);

// 时间按钮：展开一个小浮层填自定义区间
const TimeRangeChip = ({ label, active, startDate, endDate, onApply }) => {
  const { timezone } = useTimezone();
  const [open, setOpen] = useState(false);
  const [draft, setDraft] = useState({ start: '', end: '' });
  const triggerRef = useRef(null);
  const panelRef = useRef(null);
  const position = useAnchoredPanel({ open, setOpen, triggerRef, panelRef, maxWidth: 280 });
  const valid = isValidCustomRange(draft.start, draft.end);

  const toggle = () => {
    if (!open) {
      setDraft({ start: formatFilterDateTimeInput(startDate, timezone), end: formatFilterDateTimeInput(endDate, timezone) });
    }
    setOpen((value) => !value);
  };

  return (
    <>
      <span ref={triggerRef} className={`${CHIP} ${chipTone(active)}`}>
        <button type="button" aria-haspopup="dialog" aria-expanded={open} onClick={toggle} className="inline-flex h-full items-center gap-1 pl-2 pr-1.5">
          <span className={active ? 'opacity-70' : 'text-fg-muted'}>时间</span>
          <span>{label}</span>
          <ChevronDown size={11} className="shrink-0 opacity-60" />
        </button>
      </span>
      {open && position && createPortal(
        <div
          ref={panelRef}
          role="dialog"
          aria-label="自定义时间段"
          style={position}
          className="fixed z-50 rounded-[10px] bg-surface/95 p-3 text-xs text-fg shadow-xl ring-1 ring-hairline backdrop-blur-xl motion-popover origin-top-left"
        >
          <div className="mb-2 text-[11px] font-semibold text-fg-subtle">自定义时间段</div>
          <div className="grid grid-cols-[20px_minmax(0,1fr)] items-center gap-x-2 gap-y-1.5 text-fg-muted">
            <span>从</span>
            <input type="datetime-local" step="any" aria-label="开始时间" value={draft.start} onChange={(event) => setDraft((prev) => ({ ...prev, start: event.target.value }))} className="desk-field w-full text-xs" />
            <span>到</span>
            <input type="datetime-local" step="any" aria-label="结束时间" value={draft.end} onChange={(event) => setDraft((prev) => ({ ...prev, end: event.target.value }))} className="desk-field w-full text-xs" />
          </div>
          {!valid && draft.start && draft.end && <div className="mt-1.5 text-[11px] text-danger">开始时间需要早于结束时间</div>}
          <div className="mt-3 flex justify-end gap-1.5">
            <Button size="sm" variant="secondary" onClick={() => setOpen(false)}>取消</Button>
            <Button size="sm" disabled={!valid} onClick={() => { onApply?.(buildCustomRangePatch({ startDate, endDate }, draft, timezone)); setOpen(false); }}>应用</Button>
          </div>
        </div>,
        document.body
      )}
    </>
  );
};

/**
 * @param {Object} props
 * @param {Object} props.filters - useFilters 的筛选状态
 * @param {string} props.activeRange - 工具栏时间预设（today / 7days / 30days / custom）
 * @param {Array} props.models - 模型列表（字符串或 { model_name }）
 * @param {Array} props.upstreams - 上游候选 [{ value, label }]
 * @param {Function} props.onChange - (key, value) => void，立即生效
 * @param {Function} props.onClear - 清除全部条件（不含时间）
 * @param {Function} props.onApplyCustomRange - ({ startDate, endDate }) => void
 */
const FilterBar = ({ filters, activeRange, models = [], upstreams = [], onChange, onClear, onApplyCustomRange }) => {
  const { timezone } = useTimezone();
  const { contextMenu, openMenuAt } = useContextMenu();

  const modelOptions = useMemo(() => [
    { value: '', label: '全部模型' },
    ...models
      .map((model) => (typeof model === 'string' ? model : (model.model_name || model.name || '')))
      .filter(Boolean)
      .map((name) => ({ value: name, label: name }))
  ], [models]);

  const chips = [
    { key: 'requestFamily', label: '类型', options: FAMILY_OPTIONS },
    { key: 'sourceView', label: '来源', options: SOURCE_VIEW_OPTIONS },
    { key: 'status', label: '状态', options: STATUS_SELECT_OPTIONS },
    { key: 'model', label: '模型', options: modelOptions },
    { key: 'upstreamName', label: '上游', options: [{ value: 'all', label: '全部上游' }, ...upstreams] }
  ];
  const activeCount = countActiveChips(filters);

  const openChipMenu = (event, chip) => {
    const current = filters[chip.key] ?? '';
    openMenuAt(event.currentTarget, chip.options.map((option) => ({
      label: option.label,
      checked: String(option.value) === String(current),
      onSelect: () => onChange?.(chip.key, option.value)
    })));
  };

  return (
    <div className="flex h-8 shrink-0 items-center gap-1.5 overflow-x-auto border-b border-line bg-inspector px-3">
      {chips.map((chip) => {
        const current = filters[chip.key] ?? '';
        const option = chip.options.find((item) => String(item.value) === String(current));
        return (
          <FilterChip
            key={chip.key}
            label={chip.label}
            valueLabel={shortAllLabel(option?.label ?? (current || '全部'))}
            active={isChipActive(filters, chip.key)}
            onOpen={(event) => openChipMenu(event, chip)}
            onClear={() => onChange?.(chip.key, '')}
          />
        );
      })}
      {isChipActive(filters, 'includeCompaction') && (
        <FilterChip label="种类" valueLabel="仅生成" active onClear={() => onChange?.('includeCompaction', true)} />
      )}
      <TimeRangeChip
        label={describeTimeChip(activeRange, formatFilterDateTimeInput(filters.startDate, timezone), formatFilterDateTimeInput(filters.endDate, timezone))}
        active={activeRange === 'custom'}
        startDate={filters.startDate}
        endDate={filters.endDate}
        onApply={onApplyCustomRange}
      />
      <span className="min-w-4 flex-1" aria-hidden="true" />
      {activeCount > 0
        ? <button type="button" onClick={onClear} className="shrink-0 text-xs text-accent hover:text-accent-fg">清除筛选</button>
        : <span className="shrink-0 truncate text-xs text-fg-subtle">单击选中 · 右键更多操作 · ↑↓ 切换</span>}
      {contextMenu}
    </div>
  );
};

export default FilterBar;
