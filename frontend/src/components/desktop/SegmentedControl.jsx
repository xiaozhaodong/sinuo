// ============================================
// 桌面化组件 - 分段控件（单选，macOS segmented control 外观）
// 2026-10-07
// ============================================
// 选项互斥的少量取值（时间范围、模式等）。页面子页签用 PageTabs（tab 语义），这里是单选语义。

const SegmentedControl = ({ options, value, onChange, ariaLabel, disabled = false }) => (
  <div role="radiogroup" aria-label={ariaLabel} className="inline-flex shrink-0 items-center rounded-[7px] bg-surface-mut p-0.5">
    {options.map((option) => {
      const selected = option.value === value;
      return (
        <button
          key={option.value}
          type="button"
          role="radio"
          aria-checked={selected}
          disabled={disabled}
          title={option.title}
          onClick={() => onChange?.(option.value)}
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

export default SegmentedControl;
