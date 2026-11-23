// ============================================
// UI 基础组件库
// 2025-11-28（2026-11 清理已退役的老版卡片组件，拆分 CustomSelect）
// ============================================

import { AlertCircle, Loader2 } from 'lucide-react';
export { CustomSelect } from './CustomSelect.jsx';

// ============================================
// 加载 Spinner
// ============================================
export const LoadingSpinner = ({ size = 'md', text = '加载中...' }) => {
  const sizeClasses = {
    sm: 'w-4 h-4',
    md: 'w-8 h-8',
    lg: 'w-12 h-12'
  };

  return (
    <div className="loading-reveal flex flex-col items-center justify-center py-12">
      <div className={`${sizeClasses[size]} border-2 border-line border-t-accent rounded-full animate-spin`}></div>
      {text && <p className="mt-3 text-sm text-fg-muted">{text}</p>}
    </div>
  );
};

// ============================================
// 错误提示
// ============================================
export const ErrorMessage = ({ title = '加载失败', message, onRetry }) => (
  <div className="flex flex-col items-center justify-center py-12 text-center">
    <div className="p-4 bg-danger-soft rounded-full mb-4">
      <AlertCircle size={32} className="text-danger" />
    </div>
    <h3 className="text-lg font-semibold text-danger mb-2">{title}</h3>
    {message && <p className="text-sm text-danger max-w-md mb-4">{message}</p>}
    {onRetry && (
      <button
        type="button"
        onClick={onRetry}
        className="px-4 py-2 bg-rose-600 text-white rounded-lg text-sm font-medium hover:bg-rose-700 dark:hover:bg-rose-500 transition-colors"
      >
        重试
      </button>
    )}
  </div>
);

// ============================================
// 按钮组件
// ============================================
export const Button = ({
  children,
  variant = 'primary',
  size = 'md',
  icon: Icon,
  loading = false,
  disabled = false,
  className = '',
  ...props
}) => {
  // 主行动按钮使用 --app-accent-solid（浅深同饱和底 + 白字，hover 浅暗各自向正确方向移动）。
  const variants = {
    primary: 'bg-accent-solid text-white hover:bg-accent-solid-hover shadow-xs',
    // 2026-10-07 桌面化：次要按钮改为带边框的白底按钮（macOS push button），不再是灰色色块
    secondary: 'border border-line bg-surface text-fg-body shadow-xs hover:bg-surface-sub',
    success: 'bg-emerald-600 text-white hover:bg-emerald-700 dark:hover:bg-emerald-500',
    danger: 'bg-rose-600 text-white hover:bg-rose-700 dark:hover:bg-rose-500',
    ghost: 'bg-transparent text-fg-body hover:bg-surface-mut'
  };

  // 桌面化尺寸：24 / 28 / 32px 高
  const sizes = {
    sm: 'h-6 px-2.5 text-xs',
    md: 'h-7 px-3 text-[13px]',
    lg: 'h-8 px-4 text-sm'
  };

  return (
    <button
      disabled={disabled || loading}
      className={`
        inline-flex shrink-0 items-center justify-center whitespace-nowrap font-medium rounded-md transition-colors
        ${variants[variant]} ${sizes[size]}
        ${disabled || loading ? 'opacity-50 cursor-not-allowed' : ''}
        ${className}
      `}
      {...props}
    >
      {loading ? (
        <Loader2 size={14} className="animate-spin mr-1.5" />
      ) : Icon && (
        <Icon size={14} className="mr-1.5" />
      )}
      {children}
    </button>
  );
};
