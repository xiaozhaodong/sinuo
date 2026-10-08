// ============================================
// 布局组件 - 工具栏搜索框（⌘F / Ctrl+F 聚焦，Esc 清空）
// 2026-10-07
// ============================================
// 同一时刻只有当前页面的搜索框挂载，快捷键直接聚焦这一个。

import { useEffect, useRef } from 'react';
import { Search, X } from 'lucide-react';
import { formatShortcut, isMacPlatform } from '@utils/platform.js';

const ToolbarSearch = ({ value, onChange, placeholder = '搜索', width = 200 }) => {
  const inputRef = useRef(null);

  useEffect(() => {
    const mac = isMacPlatform();
    const handleKeyDown = (event) => {
      const primary = mac ? event.metaKey : event.ctrlKey;
      if (!primary || event.altKey || event.shiftKey || event.key.toLowerCase() !== 'f') return;
      if (document.querySelector('[aria-modal="true"]')) return;
      event.preventDefault();
      inputRef.current?.focus();
      inputRef.current?.select();
    };
    window.addEventListener('keydown', handleKeyDown);
    return () => window.removeEventListener('keydown', handleKeyDown);
  }, []);

  return (
    <label
      className="flex h-7 shrink-0 items-center gap-1.5 rounded-[7px] bg-surface-mut px-2 text-fg-subtle focus-within:bg-surface focus-within:ring-1 focus-within:ring-accent-ring"
      style={{ width }}
    >
      <Search size={14} className="shrink-0" />
      <input
        ref={inputRef}
        type="search"
        value={value}
        onChange={(event) => onChange(event.target.value)}
        onKeyDown={(event) => {
          if (event.key === 'Escape' && value) {
            event.stopPropagation();
            onChange('');
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
        <span className="shrink-0 font-mono text-[11px]">{formatShortcut('F')}</span>
      )}
    </label>
  );
};

export default ToolbarSearch;
