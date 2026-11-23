// ============================================
// 布局组件 - 工具栏搜索框（⌘F / Ctrl+F 聚焦，Esc 清空）
// 2026-10-07
// 2026-11：支持窄容器自动收敛为紧凑搜索按钮，点击或 ⌘F 展开
// ============================================

import { useRef, useState } from 'react';
import { Search, X } from 'lucide-react';
import { formatShortcut } from '@utils/platform.js';
import useSearchShortcut from '@hooks/useSearchShortcut.js';

const ToolbarSearch = ({ value, onChange, placeholder = '搜索', width = 200 }) => {
  const inputRef = useRef(null);
  const rootRef = useRef(null);
  const [expanded, setExpanded] = useState(false);

  const openAndFocus = () => {
    setExpanded(true);
    requestAnimationFrame(() => {
      inputRef.current?.focus();
      inputRef.current?.select();
    });
  };

  useSearchShortcut(inputRef, {
    onFocusRequest: () => {
      // 仅对当前可见工具栏生效（隐藏 tabpanel 的容器 offsetParent 为 null）
      if (!rootRef.current || rootRef.current.offsetParent === null || rootRef.current.closest('[hidden]')) {
        return false;
      }
      openAndFocus();
      return true;
    }
  });

  const showFull = expanded || Boolean(value);

  return (
    <div ref={rootRef} className="inline-flex shrink-0 items-center">
      {!showFull && (
        <button
          type="button"
          onClick={openAndFocus}
          title={`${placeholder}  ${formatShortcut('F')}`}
          aria-label={placeholder}
          className="hidden h-7 w-7 shrink-0 items-center justify-center rounded-md text-fg-muted transition-colors hover:bg-surface-mut hover:text-fg @max-[640px]:inline-flex"
        >
          <Search size={16} strokeWidth={1.75} />
        </button>
      )}
      <label
        className={`h-7 shrink-0 items-center gap-1.5 rounded-[7px] bg-surface-mut px-2 text-fg-subtle transition-[width,background-color] focus-within:bg-surface focus-within:ring-1 focus-within:ring-accent-ring ${
          showFull ? 'flex' : 'flex @max-[640px]:hidden'
        }`}
        style={{ width: `min(${width}px, 34vw)` }}
      >
        <Search size={14} className="shrink-0" />
        <input
          ref={inputRef}
          type="search"
          value={value}
          onChange={(event) => onChange(event.target.value)}
          onBlur={() => {
            if (!value) setExpanded(false);
          }}
          onKeyDown={(event) => {
            if (event.key === 'Escape') {
              if (value) {
                event.stopPropagation();
                onChange('');
              } else {
                setExpanded(false);
                event.currentTarget.blur();
              }
            }
          }}
          placeholder={placeholder}
          aria-label={placeholder}
          className="min-w-0 flex-1 bg-transparent text-[13px] text-fg outline-none placeholder:text-fg-subtle [&::-webkit-search-cancel-button]:hidden"
        />
        {value ? (
          <button type="button" onClick={() => onChange('')} aria-label="清空搜索" className="shrink-0 rounded-full p-0.5 hover:text-fg">
            <X size={12} />
          </button>
        ) : (
          <span className="shrink-0 font-mono text-[11px] @max-[720px]:hidden">{formatShortcut('F')}</span>
        )}
      </label>
    </div>
  );
};

export default ToolbarSearch;
