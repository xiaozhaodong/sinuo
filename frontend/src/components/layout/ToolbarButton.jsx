// ============================================
// 布局组件 - 工具栏按钮 / 分隔 / 弹性占位
// 2026-10-07
// ============================================
// variant：plain 图标按钮（悬停才出底色）；bordered 带边框的次要文字按钮；primary 主行动。
// pressed 只给开关类按钮传（如「显示筛选」），普通动作按钮不要传，避免读屏误报为切换按钮。

import { Loader2 } from 'lucide-react';

const VARIANTS = {
  plain: 'text-fg-muted hover:bg-surface-mut hover:text-fg',
  bordered: 'border border-line bg-surface text-fg-body shadow-xs hover:bg-surface-sub',
  primary: 'bg-indigo-600 text-white hover:bg-indigo-700 dark:hover:bg-indigo-500'
};

const PRESSED = 'bg-accent-soft text-accent';

export const ToolbarButton = ({
  icon: Icon,
  label,
  title,
  variant = 'plain',
  pressed,
  loading = false,
  disabled = false,
  className = '',
  ...props
}) => {
  const shape = label ? 'px-2.5' : 'w-7';
  const tone = pressed ? PRESSED : VARIANTS[variant];

  return (
    <button
      type="button"
      title={title ?? label}
      aria-label={label ? undefined : title}
      aria-pressed={pressed}
      disabled={disabled || loading}
      className={`inline-flex h-7 shrink-0 items-center justify-center gap-1.5 rounded-md text-[13px] transition-colors disabled:opacity-40 ${shape} ${tone} ${className}`}
      {...props}
    >
      {loading
        ? <Loader2 size={15} className="animate-spin" />
        : Icon && <Icon size={label ? 15 : 17} strokeWidth={1.75} />}
      {label && <span className="whitespace-nowrap">{label}</span>}
    </button>
  );
};

export const ToolbarSpacer = () => <span className="flex-1 self-stretch" aria-hidden="true" />;

export const ToolbarSeparator = () => <span className="mx-0.5 h-[18px] w-px shrink-0 bg-line" aria-hidden="true" />;
