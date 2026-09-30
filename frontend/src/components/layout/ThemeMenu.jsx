// ============================================
// 布局组件 - 页头主题菜单
// 2026-09-30：跟随系统 / 浅色 / 深色 三选一，替代原浅深两态快捷开关
// ============================================

import { useId, useRef, useState } from 'react';
import { createPortal } from 'react-dom';
import { Check, Monitor, Moon, Sun } from 'lucide-react';
import { useTheme } from '@contexts/ThemeContext.jsx';
import useAnchoredPanel from '@hooks/useAnchoredPanel.js';

const MENU_WIDTH = 200;

const THEME_OPTIONS = [
  { value: 'system', label: '跟随系统', icon: Monitor },
  { value: 'light', label: '浅色', icon: Sun },
  { value: 'dark', label: '深色', icon: Moon }
];

const THEME_LABELS = { light: '浅色', dark: '深色' };

const ThemeMenu = () => {
  const { preference, system, setPreference } = useTheme();
  const [open, setOpen] = useState(false);
  const triggerRef = useRef(null);
  const panelRef = useRef(null);
  const menuId = useId();
  const position = useAnchoredPanel({ open, setOpen, triggerRef, panelRef, maxWidth: MENU_WIDTH });

  const current = THEME_OPTIONS.find((option) => option.value === preference) || THEME_OPTIONS[0];
  const CurrentIcon = current.icon;
  const summary = preference === 'system' ? `跟随系统（当前${THEME_LABELS[system]}）` : current.label;

  const choose = (value) => {
    setPreference(value);
    setOpen(false);
  };

  return (
    <>
      <button
        ref={triggerRef}
        type="button"
        aria-haspopup="menu"
        aria-expanded={open}
        aria-controls={menuId}
        aria-label={`主题：${summary}`}
        title={`主题：${summary}`}
        onClick={() => setOpen((value) => !value)}
        className={`p-2 rounded-lg transition-colors hover:text-fg hover:bg-surface-mut ${open ? 'text-fg bg-surface-mut' : 'text-fg-muted'}`}
      >
        <CurrentIcon size={16} />
      </button>

      {open && position && createPortal(
        <div
          ref={panelRef}
          id={menuId}
          role="menu"
          aria-label="主题"
          style={position}
          className="fixed z-50 rounded-xl border border-line bg-surface p-1 shadow-xl ring-1 ring-hairline motion-popover origin-top-right"
        >
          {THEME_OPTIONS.map(({ value, label, icon: Icon }) => {
            const checked = value === preference;
            return (
              <button
                key={value}
                type="button"
                role="menuitemradio"
                aria-checked={checked}
                onClick={() => choose(value)}
                className={`flex w-full items-center gap-2.5 rounded-lg px-2.5 py-2 text-left text-sm ${checked ? 'bg-accent-soft text-accent-fg' : 'text-fg-body hover:bg-surface-sub'}`}
              >
                <Icon size={15} className="shrink-0" />
                <span className="flex-1">{label}</span>
                {value === 'system' && <span className="text-[11px] text-fg-subtle">当前{THEME_LABELS[system]}</span>}
                <Check size={14} className={`shrink-0 ${checked ? '' : 'invisible'}`} />
              </button>
            );
          })}
        </div>,
        document.body
      )}
    </>
  );
};

export default ThemeMenu;
