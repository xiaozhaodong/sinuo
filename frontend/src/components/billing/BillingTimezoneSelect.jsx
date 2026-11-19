import { useEffect, useId, useMemo, useRef, useState } from 'react';
import { Check, ChevronDown } from 'lucide-react';
import { matchesTimezoneQuery, timezoneOption } from './billingTimezone.js';

export default function BillingTimezoneSelect({ value, appTimezone, onChange }) {
  const id = useId();
  const input = useRef(null);
  const list = useRef(null);
  const [query, setQuery] = useState(null);
  const [active, setActive] = useState(0);
  const [instant, setInstant] = useState(() => new Date());
  const open = query !== null;
  // 已保存的 IANA 别名可能与构建环境中的规范名称不同，保留原值供重新选择。
  const zones = useMemo(
    () => [...new Set([appTimezone, value, 'UTC', ...(import.meta.env?.IANA_TIME_ZONES || [])].filter(Boolean))],
    [appTimezone, value]
  );
  const selected = useMemo(() => timezoneOption(value, instant), [value, instant]);
  const options = useMemo(() => open ? zones.map((zone) => timezoneOption(zone, instant)) : [], [open, zones, instant]);
  const needle = (query || '').trim().toLowerCase().replace(/\s+/g, '_');
  const visible = options.filter((option) => matchesTimezoneQuery(option, query));
  // Intl 的主列表省略部分有效别名和 Etc/GMT 时区，完整名称仍可搜索选择。
  if (needle && !visible.length && /^[a-z][\w/+.-]*$/.test(needle)) {
    try {
      visible.push(timezoneOption(new Intl.DateTimeFormat('en', { timeZone: needle }).resolvedOptions().timeZone, instant));
    } catch (err) {
      if (!(err instanceof RangeError)) throw err;
    }
  }
  const show = () => {
    setInstant(new Date());
    setActive(Math.max(0, zones.indexOf(value)));
    setQuery('');
  };
  const choose = (zone) => {
    onChange(zone);
    setQuery(null);
  };
  useEffect(() => {
    const menu = list.current;
    const option = menu?.children[active];
    if (!option) return;
    if (option.offsetTop < menu.scrollTop) menu.scrollTop = option.offsetTop;
    else if (option.offsetTop + option.offsetHeight > menu.scrollTop + menu.clientHeight)
      menu.scrollTop = option.offsetTop + option.offsetHeight - menu.clientHeight;
  }, [active, query]);

  const onKeyDown = (event) => {
    if (event.nativeEvent.isComposing) return;
    if (event.key === 'ArrowDown' || event.key === 'ArrowUp') {
      event.preventDefault();
      if (!open) show();
      else setActive(Math.max(0, Math.min(visible.length - 1, active + (event.key === 'ArrowDown' ? 1 : -1))));
    } else if (open && event.key === 'Enter') {
      event.preventDefault();
      if (visible[active]) choose(visible[active].id);
    } else if (open && event.key === 'Escape') {
      event.stopPropagation();
      setQuery(null);
    }
  };
  return (
    <div className="min-w-0">
      <label htmlFor={id} className="mb-1.5 block text-xs font-medium text-fg">计费时区</label>
      <div className="relative">
        <input id={id} ref={input} role="combobox" aria-autocomplete="list" aria-expanded={open}
          aria-controls={`${id}-list`} aria-activedescendant={open && visible[active] ? `${id}-${active}` : undefined}
          autoComplete="off" spellCheck={false} className="desk-field w-full min-w-0 pr-8"
          value={open ? query : selected.label} title={value} placeholder={open ? '搜索城市或时区' : '选择时区'}
          onClick={() => { if (!open) show(); }} onBlur={() => setQuery(null)} onKeyDown={onKeyDown}
          onFocus={show} onChange={(e) => { setQuery(e.target.value); setActive(0); }} />
        <button type="button" tabIndex={-1} aria-label={open ? '收起时区列表' : '展开时区列表'}
          className="absolute inset-y-0 right-0 flex w-7 items-center justify-center text-fg-muted"
          onMouseDown={(e) => e.preventDefault()} onClick={() => {
            input.current.focus();
            if (open) setQuery(null);
            else show();
          }}><ChevronDown size={14} /></button>
        {open && (
          <div ref={list} role="listbox" id={`${id}-list`} aria-label="计费时区选项"
            className="absolute z-20 mt-1 max-h-36 w-full overflow-y-auto rounded-md border border-line bg-surface py-1 shadow-lg">
            {visible.map(({ id: zone, label }, i) => (
              <div key={zone} id={`${id}-${i}`} role="option" aria-selected={zone === value}
                className={`flex cursor-pointer items-center gap-2 px-2.5 py-1 text-xs hover:bg-surface-mut ${active === i ? 'bg-surface-mut' : ''}`}
                onMouseDown={(e) => e.preventDefault()} onClick={() => choose(zone)}>
                <span>{label}</span>
                <span className="ml-auto truncate text-[11px] text-fg-muted" title={zone}>{zone}</span>
                {zone === appTimezone && <span className="shrink-0 text-[11px] text-fg-muted">程序时区</span>}
                {zone === value && <Check size={13} className="text-accent" />}
              </div>
            ))}
            {!visible.length && <div role="status" className="px-2.5 py-1 text-xs text-fg-muted">没有匹配的时区</div>}
          </div>
        )}
      </div>
    </div>
  );
}
