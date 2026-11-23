// ============================================
// 桌面化组件 - 分段控件（单选，macOS segmented control 外观）
// 2026-10-07
// ============================================
// 选项互斥的少量取值（时间范围、模式等）。页面子页签用 PageTabs（tab 语义），这里是单选语义。
// 支持 ← / → / Home / End 方向键切换。

import { useRef } from 'react';

const SegmentedControl = ({ options, value, onChange, ariaLabel, disabled = false }) => {
  const groupRef = useRef(null);
  const hasSelected = options.some((option) => option.value === value);

  const handleKeyDown = (event, index) => {
    if (disabled || !options.length) return;
    let nextIndex = null;
    if (event.key === 'ArrowRight' || event.key === 'ArrowDown') nextIndex = (index + 1) % options.length;
    else if (event.key === 'ArrowLeft' || event.key === 'ArrowUp') nextIndex = (index - 1 + options.length) % options.length;
    else if (event.key === 'Home') nextIndex = 0;
    else if (event.key === 'End') nextIndex = options.length - 1;
    if (nextIndex === null) return;

    event.preventDefault();
    const nextOption = options[nextIndex];
    if (!nextOption) return;
    onChange?.(nextOption.value);
    const buttons = groupRef.current?.querySelectorAll('[role="radio"]');
    buttons?.[nextIndex]?.focus();
  };

  return (
    <div ref={groupRef} role="radiogroup" aria-label={ariaLabel} className="inline-flex shrink-0 items-center rounded-[7px] bg-surface-mut p-0.5">
      {options.map((option, index) => {
        const selected = option.value === value;
        const focusable = selected || (!hasSelected && index === 0);
        return (
          <button
            key={option.value}
            type="button"
            role="radio"
            tabIndex={focusable ? 0 : -1}
            aria-checked={selected}
            disabled={disabled}
            title={option.title}
            onClick={() => onChange?.(option.value)}
            onKeyDown={(event) => handleKeyDown(event, index)}
            className={`h-[22px] whitespace-nowrap rounded-[5px] px-2.5 text-xs transition-colors disabled:opacity-50 ${
              selected
                ? 'bg-surface font-medium text-fg shadow-xs ring-1 ring-hairline dark:bg-surface-emph'
                : 'text-fg-body hover:text-fg'
            }`}
          >
            {option.label}
          </button>
        );
      })}
    </div>
  );
};

export default SegmentedControl;
