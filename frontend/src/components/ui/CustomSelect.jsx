// ============================================
// 自定义下拉选择器（Portal + 完整键盘导航）
// ============================================

import { useEffect, useId, useLayoutEffect, useRef, useState } from 'react';
import { createPortal } from 'react-dom';
import { Check, ChevronDown } from 'lucide-react';
import { isListNavigationKey, nextListId } from '@components/desktop/listNavigation.js';
import { FLOATING_LAYER_ATTR } from '@utils/floatingLayer.js';

const VIEWPORT_MARGIN = 6;
const ANCHOR_GAP = 4;
const clamp = (value, min, max) => Math.max(min, Math.min(value, max));

export const CustomSelect = ({
  options = [],
  value,
  onChange,
  size = 'sm',
  placeholder = '请选择',
  disabled = false,
  className = '',
  ariaLabel,
  'aria-label': ariaLabelAttr
}) => {
  const [isOpen, setIsOpen] = useState(false);
  const buttonRef = useRef(null);
  const dropdownRef = useRef(null);
  const listboxId = useId();
  const label = ariaLabel ?? ariaLabelAttr;
  const currentOption = options.find((opt) => opt.value === value);

  // 绘制前精确量出下拉菜单尺寸并收回视口内，走 Portal 避免被带 transform / backdrop-filter 的父容器错位
  useLayoutEffect(() => {
    if (!isOpen || !buttonRef.current || !dropdownRef.current) return;
    const buttonRect = buttonRef.current.getBoundingClientRect();
    const dropdown = dropdownRef.current;
    dropdown.style.minWidth = `${buttonRect.width}px`;
    const rect = dropdown.getBoundingClientRect();
    const spaceBelow = window.innerHeight - buttonRect.bottom;
    const spaceAbove = buttonRect.top;
    const upward = spaceBelow < rect.height + ANCHOR_GAP + VIEWPORT_MARGIN && spaceAbove > spaceBelow;
    const rawTop = upward ? buttonRect.top - rect.height - ANCHOR_GAP : buttonRect.bottom + ANCHOR_GAP;
    const left = clamp(buttonRect.left, VIEWPORT_MARGIN, window.innerWidth - rect.width - VIEWPORT_MARGIN);
    const top = clamp(rawTop, VIEWPORT_MARGIN, window.innerHeight - rect.height - VIEWPORT_MARGIN);
    dropdown.style.left = `${left}px`;
    dropdown.style.top = `${top}px`;
    dropdown.style.transformOrigin = upward ? 'bottom' : 'top';

    const selectedButton = dropdown.querySelector('[role="option"][aria-selected="true"]:not(:disabled)');
    const firstButton = dropdown.querySelector('[role="option"]:not(:disabled)');
    (selectedButton || firstButton || dropdown).focus({ preventScroll: true });
    selectedButton?.scrollIntoView({ block: 'nearest' });
  }, [isOpen]);

  useEffect(() => {
    if (!isOpen) return undefined;
    const close = () => setIsOpen(false);
    const handlePointerDown = (event) => {
      if (buttonRef.current?.contains(event.target) || dropdownRef.current?.contains(event.target)) return;
      close();
    };
    const handleScroll = (event) => {
      if (dropdownRef.current?.contains(event.target)) return;
      close();
    };
    document.addEventListener('mousedown', handlePointerDown);
    window.addEventListener('scroll', handleScroll, true);
    window.addEventListener('resize', close);
    window.addEventListener('blur', close);
    return () => {
      document.removeEventListener('mousedown', handlePointerDown);
      window.removeEventListener('scroll', handleScroll, true);
      window.removeEventListener('resize', close);
      window.removeEventListener('blur', close);
    };
  }, [isOpen]);

  const closeAndFocusTrigger = () => {
    setIsOpen(false);
    buttonRef.current?.focus({ preventScroll: true });
  };

  const handleSelect = (optValue) => {
    onChange?.(optValue);
    closeAndFocusTrigger();
  };

  const handleTriggerKeyDown = (event) => {
    if (disabled) return;
    if (event.key === 'ArrowDown' || event.key === 'ArrowUp' || event.key === 'Enter' || event.key === ' ') {
      event.preventDefault();
      setIsOpen(true);
    }
  };

  const handleListboxKeyDown = (event) => {
    if (event.key === 'Escape') {
      event.preventDefault();
      event.stopPropagation();
      closeAndFocusTrigger();
      return;
    }
    if (event.key === 'Tab') {
      // 先把焦点还给触发按钮，再由浏览器（或外层模态的焦点陷阱）按正常顺序移到下一个控件
      closeAndFocusTrigger();
      return;
    }
    if (!isListNavigationKey(event.key)) return;
    event.preventDefault();
    event.stopPropagation();
    const buttons = Array.from(dropdownRef.current?.querySelectorAll('[role="option"]:not(:disabled)') || []);
    const next = nextListId(buttons, document.activeElement, event.key);
    next?.focus({ preventScroll: true });
    next?.scrollIntoView({ block: 'nearest' });
  };

  // 桌面化：弹出按钮式触发器统一 24px 高，与 desk-field 一致；size 只控制字号与最小宽度
  const sizeClasses = {
    xs: 'h-6 px-2 text-xs min-w-[80px]',
    sm: 'h-6 px-2.5 text-xs min-w-[100px]',
    md: 'h-6 px-2.5 text-[13px] min-w-[120px]'
  };

  const dropdownSizeClasses = {
    xs: 'text-xs',
    sm: 'text-xs',
    md: 'text-sm'
  };

  return (
    <div className={`relative inline-block ${className}`}>
      <button
        ref={buttonRef}
        type="button"
        aria-label={label}
        aria-haspopup="listbox"
        aria-expanded={isOpen}
        aria-controls={isOpen ? listboxId : undefined}
        onClick={() => !disabled && setIsOpen((prev) => !prev)}
        onKeyDown={handleTriggerKeyDown}
        disabled={disabled}
        className={`
          group inline-flex w-full items-center justify-between bg-surface border rounded-md shadow-xs
          transition-colors font-medium
          ${sizeClasses[size]}
          ${disabled
            ? 'opacity-50 cursor-not-allowed border-line text-fg-subtle'
            : isOpen
              ? 'border-accent-ring ring-3 ring-accent-ring/25 text-fg'
              : 'border-line text-fg-body hover:border-line-strong hover:text-fg'
          }
        `}
      >
        <span className="truncate">
          {currentOption?.label || placeholder}
        </span>
        <ChevronDown
          size={size === 'xs' ? 12 : 14}
          className={`ml-1.5 flex-shrink-0 transition-transform duration-200 ${
            isOpen ? 'rotate-180' : ''
          } ${disabled ? 'text-fg-subtle/60' : 'text-fg-subtle'}`}
        />
      </button>

      {isOpen && typeof document !== 'undefined' && createPortal(
        <div
          ref={dropdownRef}
          id={listboxId}
          role="listbox"
          tabIndex={-1}
          {...{ [FLOATING_LAYER_ATTR]: '' }}
          aria-label={label}
          onKeyDown={handleListboxKeyDown}
          className={`
            fixed z-[130] bg-surface/95 border border-line backdrop-blur-xl
            rounded-lg shadow-xl ring-1 ring-hairline overflow-hidden motion-popover focus:outline-none
            ${dropdownSizeClasses[size]}
          `}
          style={{ top: 0, left: 0, maxHeight: '240px' }}
        >
          <div className="p-1 max-h-[240px] overflow-y-auto">
            {options.map((option) => {
              const isSelected = option.value === value;
              return (
                <button
                  key={option.value}
                  type="button"
                  role="option"
                  aria-selected={isSelected}
                  disabled={option.disabled}
                  onClick={() => handleSelect(option.value)}
                  className={`
                    group flex h-6 w-full items-center justify-between rounded px-2 text-left whitespace-nowrap
                    hover:bg-accent-solid hover:text-white focus-visible:bg-accent-solid focus-visible:text-white focus-visible:outline-none
                    disabled:pointer-events-none disabled:opacity-40
                    ${isSelected ? 'font-medium text-fg' : 'text-fg-body'}
                  `}
                >
                  <span>{option.label}</span>
                  {isSelected && (
                    <Check size={13} className="ml-3 flex-shrink-0 text-accent group-hover:text-white group-focus-visible:text-white" />
                  )}
                </button>
              );
            })}
          </div>
        </div>,
        document.body
      )}
    </div>
  );
};

export default CustomSelect;
