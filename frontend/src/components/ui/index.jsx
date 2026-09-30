// ============================================
// UI 组件库 - 基于 claude-dashboard 样式
// 2025-11-28
// ============================================

import { useState, useRef, useEffect, useCallback } from 'react';
import {
  CheckCircle2,
  Key,
  ChevronDown,
  ChevronUp,
  AlertCircle,
  XCircle,
  Clock,
  Loader2
} from 'lucide-react';

// ============================================
// KPI 卡片组件
// ============================================
export const KPICard = ({ title, value, tooltip, subText, icon: Icon, statusColor = 'bg-surface-mut text-fg-body' }) => (
  <div className="bg-surface p-5 rounded-2xl border border-line/60 shadow-sm flex flex-col justify-between hover:shadow-md transition-shadow">
    <div className="flex justify-between items-start mb-2">
      <div className={`p-2 rounded-lg ${statusColor}`}>
        <Icon size={20} />
      </div>
    </div>
    <div>
      <h3 className="text-fg-muted text-xs font-semibold uppercase tracking-wider mb-1">{title}</h3>
      <div
        className="text-xl font-bold text-fg truncate cursor-default"
        title={tooltip || value}
      >
        {value}
      </div>
      {subText && <div className="text-xs text-fg-subtle mt-1 font-medium">{subText}</div>}
    </div>
  </div>
);

// ============================================
// 统计详情项
// ============================================
export const StatDetailItem = ({ label, value, unit, valueColor }) => (
  <div className="bg-surface-sub rounded-xl p-4 flex flex-col items-center justify-center border border-line-soft hover:border-line transition-colors group">
    <div className={`text-2xl font-bold mb-1 ${valueColor || 'text-fg'} group-hover:scale-105 transition-transform`}>
      {value}<span className="text-sm font-medium text-fg-muted ml-0.5">{unit}</span>
    </div>
    <div className="text-xs text-fg-subtle font-medium">{label}</div>
  </div>
);

// ============================================
// 摘要卡片（追踪页面用）
// ============================================
export const TraceSummaryCard = ({ title, value, subValue, icon: Icon, colorClass, borderColorClass }) => (
  <div className="bg-surface p-4 rounded-xl border border-line/60 shadow-sm flex items-center space-x-4 relative overflow-hidden">
    <div className={`absolute left-0 top-0 bottom-0 w-1 ${borderColorClass}`}></div>
    <div className={`p-2.5 rounded-lg ${colorClass}`}>
      <Icon size={20} strokeWidth={2.5} />
    </div>
    <div>
      <div className="text-xl font-bold text-fg leading-tight">{value}</div>
      <div className="text-xs text-fg-muted font-medium mt-0.5">
        {title} {subValue && <span className="text-fg-subtle">({subValue})</span>}
      </div>
    </div>
  </div>
);

// ============================================
// 状态徽章
// ============================================
export const StatusBadge = ({ status }) => {
  // tone-* 一个类给齐底 / 字 / 边；icon 不再单独指定颜色，继承 tone 的 currentColor
  const configs = {
    healthy: {
      tone: 'tone-emerald',
      dot: 'bg-success-solid',
      label: '健康'
    },
    unhealthy: {
      tone: 'tone-rose',
      dot: 'bg-danger-solid',
      label: '异常'
    },
    completed: {
      tone: 'tone-emerald',
      icon: CheckCircle2,
      label: '已完成'
    },
    failed: {
      tone: 'tone-rose',
      icon: XCircle,
      label: '失败'
    },
    pending: {
      tone: 'tone-amber',
      icon: Clock,
      label: '等待中'
    },
    processing: {
      tone: 'tone-blue',
      icon: Loader2,
      label: '处理中'
    }
  };

  const config = configs[status] || configs.pending;
  const IconComponent = config.icon;

  return (
    <div className={`inline-flex items-center px-2.5 py-0.5 rounded-full text-xs font-medium border ${config.tone}`}>
      {config.dot && <span className={`w-1.5 h-1.5 rounded-full mr-1.5 ${config.dot}`}></span>}
      {IconComponent && <IconComponent size={12} className="mr-1" />}
      {config.label}
    </div>
  );
};

// ============================================
// 延迟指示器
// ============================================
export const LatencyIndicator = ({ ms }) => {
  // 处理各种输入格式：数字、"376ms" 字符串、"-" 等
  let value = ms;
  if (typeof ms === 'string') {
    if (ms === '-' || ms === '') return <span className="text-fg-subtle">-</span>;
    // 提取数字部分，去掉 "ms" 后缀
    value = parseFloat(ms.replace(/ms$/i, '')) || 0;
  }
  if (!value || value === 0) return <span className="text-fg-subtle">-</span>;

  let colorClass = 'text-success';
  if (value > 500) colorClass = 'text-warn';
  if (value > 1000) colorClass = 'text-danger';

  return (
    <span className={`font-mono font-medium ${colorClass}`}>
      {Math.round(value)}ms
    </span>
  );
};

// ============================================
// Token 下拉选择器
// ============================================
export const TokenSelector = ({ tokens = [], currentTokenId, onSelect, disabled = false }) => {
  const [isOpen, setIsOpen] = useState(false);
  const containerRef = useRef(null);
  const currentToken = tokens.find(t => t.id === currentTokenId) || tokens[0];

  useEffect(() => {
    const handleClickOutside = (event) => {
      if (containerRef.current && !containerRef.current.contains(event.target)) {
        setIsOpen(false);
      }
    };
    document.addEventListener('mousedown', handleClickOutside);
    return () => document.removeEventListener('mousedown', handleClickOutside);
  }, []);

  if (!currentToken) {
    return (
      <span className="text-xs text-fg-subtle font-mono">--</span>
    );
  }

  return (
    <div className="relative inline-block text-left" ref={containerRef}>
      <button
        type="button"
        onClick={() => !disabled && setIsOpen(!isOpen)}
        disabled={disabled}
        className={`group inline-flex items-center justify-between w-36 px-3 py-1.5 text-sm bg-surface border rounded-lg shadow-sm transition-all duration-200 ${
          disabled
            ? 'opacity-50 cursor-not-allowed border-line'
            : isOpen
              ? 'border-accent-ring ring-2 ring-accent-soft z-10'
              : 'border-line hover:border-line-strong hover:shadow'
        }`}
      >
        <div className="flex items-center min-w-0">
          <Key size={14} className="mr-2 text-warn-solid flex-shrink-0" fill="currentColor" fillOpacity={0.6} strokeWidth={2.5} />
          <span className={`font-medium truncate ${isOpen ? 'text-accent' : 'text-fg-body group-hover:text-fg'}`}>
            {currentToken.name}
          </span>
        </div>
        <div className="ml-2 flex-shrink-0 text-fg-subtle group-hover:text-fg-muted">
          {isOpen ? <ChevronUp size={14} /> : <ChevronDown size={14} />}
        </div>
      </button>

      {isOpen && (
        <div className="absolute z-50 left-0 mt-2 w-64 origin-top-left bg-surface border border-line-soft rounded-xl shadow-2xl ring-1 ring-hairline focus:outline-none overflow-hidden">
          <div className="px-4 py-2.5 bg-surface-sub/80 border-b border-line-soft backdrop-blur-sm">
            <span className="text-[10px] font-bold text-fg-subtle uppercase tracking-wider">选择 Token</span>
          </div>
          <div className="py-1 max-h-60 overflow-y-auto custom-scrollbar">
            {tokens.map((token) => {
              const isActive = token.id === currentTokenId;
              return (
                <div
                  key={token.id}
                  onClick={() => {
                    onSelect?.(token.id);
                    setIsOpen(false);
                  }}
                  className={`group relative flex items-center justify-between px-4 py-3 cursor-pointer transition-colors ${
                    isActive ? 'bg-accent-soft/60' : 'hover:bg-surface-sub'
                  }`}
                >
                  <div className="flex flex-col min-w-0 pr-2">
                    <span className={`text-sm font-semibold truncate mb-0.5 ${isActive ? 'text-accent' : 'text-fg-body'}`}>
                      {token.name}
                    </span>
                    <span className="text-xs font-mono text-fg-subtle truncate tracking-tight">
                      {token.keyMask || token.key_mask || '••••••••'}
                    </span>
                  </div>
                  {isActive ? (
                    <span className="inline-flex items-center px-2 py-0.5 rounded text-[10px] font-bold bg-success-solid text-white shadow-sm flex-shrink-0">当前</span>
                  ) : (
                    <span className="hidden group-hover:inline-flex text-xs text-fg-subtle">选择</span>
                  )}
                  {isActive && <div className="absolute left-0 top-0 bottom-0 w-1 bg-accent-ring"></div>}
                </div>
              );
            })}
          </div>
        </div>
      )}
    </div>
  );
};

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
// 空状态
// ============================================
export const EmptyState = ({ icon: Icon, title, description, action }) => (
  <div className="flex flex-col items-center justify-center py-16 text-center">
    {Icon && (
      <div className="p-4 bg-surface-mut rounded-full mb-4">
        <Icon size={32} className="text-fg-subtle" />
      </div>
    )}
    <h3 className="text-lg font-semibold text-fg-body mb-2">{title}</h3>
    {description && <p className="text-sm text-fg-muted max-w-md mb-4">{description}</p>}
    {action}
  </div>
);

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
  // 实底彩色按钮不 token 化：饱和底 + 白字在深浅两种底色上对比度都够，
  // 反转反而破坏「主行动」的视觉权重。只有中性色按钮需要跟随主题。
  // 但 hover 必须分主题：浅色下变深（向按下的方向），暗色下变亮 ——
  // 深底上继续变深等于往页面里沉，会读成「按钮变灰」而不是被激活。
  const variants = {
    primary: 'bg-indigo-600 text-white hover:bg-indigo-700 dark:hover:bg-indigo-500 shadow-md hover:shadow-lg',
    secondary: 'bg-surface-mut text-fg-body hover:bg-surface-emph',
    success: 'bg-emerald-600 text-white hover:bg-emerald-700 dark:hover:bg-emerald-500',
    danger: 'bg-rose-600 text-white hover:bg-rose-700 dark:hover:bg-rose-500',
    ghost: 'bg-transparent text-fg-body hover:bg-surface-mut'
  };

  const sizes = {
    sm: 'px-3 py-1.5 text-xs',
    md: 'px-4 py-2 text-sm',
    lg: 'px-6 py-3 text-base'
  };

  return (
    <button
      disabled={disabled || loading}
      className={`
        inline-flex items-center justify-center font-medium rounded-lg transition-all duration-200
        ${variants[variant]} ${sizes[size]}
        ${disabled || loading ? 'opacity-50 cursor-not-allowed' : 'hover:-translate-y-0.5'}
        ${className}
      `}
      {...props}
    >
      {loading ? (
        <Loader2 size={16} className="animate-spin mr-2" />
      ) : Icon && (
        <Icon size={16} className="mr-2" />
      )}
      {children}
    </button>
  );
};

// ============================================
// 输入框
// ============================================
export const Input = ({
  label,
  error,
  className = '',
  ...props
}) => (
  <div className="flex flex-col">
    {label && (
      <label className="text-sm font-medium text-fg-body mb-1.5">{label}</label>
    )}
    <input
      className={`
        w-full px-3 py-2 border rounded-lg text-sm bg-surface text-fg
        focus:outline-none focus:ring-2 focus:ring-accent-ring focus:border-accent-ring
        ${error ? 'border-danger-line focus:ring-danger-solid focus:border-danger-solid' : 'border-line'}
        ${className}
      `}
      {...props}
    />
    {error && (
      <span className="text-xs text-danger mt-1">{error}</span>
    )}
  </div>
);

// ============================================
// 选择框（原生）
// ============================================
export const Select = ({
  label,
  options = [],
  className = '',
  ...props
}) => (
  <div className="flex flex-col">
    {label && (
      <label className="text-sm font-medium text-fg-body mb-1.5">{label}</label>
    )}
    <select
      className={`
        px-3 py-2 bg-surface-sub border border-line rounded-lg text-sm text-fg-body
        focus:outline-none focus:ring-2 focus:ring-accent-ring focus:border-accent-ring
        ${className}
      `}
      {...props}
    >
      {options.map((opt) => (
        <option key={opt.value} value={opt.value}>
          {opt.label}
        </option>
      ))}
    </select>
  </div>
);

// ============================================
// 自定义下拉选择器（统一样式）
// ============================================
export const CustomSelect = ({
  options = [],
  value,
  onChange,
  size = 'sm',
  placeholder = '请选择',
  disabled = false,
  className = ''
}) => {
  const [isOpen, setIsOpen] = useState(false);
  const [dropdownPosition, setDropdownPosition] = useState({ top: 0, left: 0, width: 0, isUpward: false });
  const containerRef = useRef(null);
  const buttonRef = useRef(null);
  const dropdownRef = useRef(null);

  const currentOption = options.find(opt => opt.value === value);

  // 更新下拉菜单位置（智能判断向上或向下）
  const updatePosition = useCallback(() => {
    if (buttonRef.current && dropdownRef.current) {
      const buttonRect = buttonRef.current.getBoundingClientRect();
      const dropdownHeight = dropdownRef.current.offsetHeight || 200; // 预估高度
      const viewportHeight = window.innerHeight;

      // 计算下方和上方的可用空间
      const spaceBelow = viewportHeight - buttonRect.bottom;
      const spaceAbove = buttonRect.top;

      // 判断是否向上展开
      const shouldOpenUpward = spaceBelow < dropdownHeight && spaceAbove > spaceBelow;

      setDropdownPosition({
        top: shouldOpenUpward ? buttonRect.top - dropdownHeight - 4 : buttonRect.bottom + 4,
        left: buttonRect.left,
        width: buttonRect.width,
        isUpward: shouldOpenUpward
      });
    }
  }, []);

  // 计算下拉菜单位置
  useEffect(() => {
    if (isOpen) {
      // 首次打开时计算位置
      updatePosition();

      // 监听滚动和窗口调整，更新位置
      window.addEventListener('scroll', updatePosition, true);
      window.addEventListener('resize', updatePosition);

      return () => {
        window.removeEventListener('scroll', updatePosition, true);
        window.removeEventListener('resize', updatePosition);
      };
    }
  }, [isOpen, updatePosition]);

  useEffect(() => {
    const handleClickOutside = (event) => {
      if (containerRef.current && !containerRef.current.contains(event.target)) {
        setIsOpen(false);
      }
    };
    document.addEventListener('mousedown', handleClickOutside);
    return () => document.removeEventListener('mousedown', handleClickOutside);
  }, []);

  const handleSelect = (optValue) => {
    onChange?.(optValue);
    setIsOpen(false);
  };

  const sizeClasses = {
    xs: 'px-2 py-1 text-xs min-w-[80px]',
    sm: 'px-2.5 py-1.5 text-xs min-w-[100px]',
    md: 'px-3 py-2 text-sm min-w-[120px]'
  };

  const dropdownSizeClasses = {
    xs: 'text-xs',
    sm: 'text-xs',
    md: 'text-sm'
  };

  return (
    <div className={`relative inline-block ${className}`} ref={containerRef}>
      <button
        ref={buttonRef}
        type="button"
        onClick={() => !disabled && setIsOpen(!isOpen)}
        disabled={disabled}
        className={`
          group inline-flex w-full items-center justify-between bg-surface border rounded-lg
          transition-all duration-200 font-medium
          ${sizeClasses[size]}
          ${disabled
            ? 'opacity-50 cursor-not-allowed border-line text-fg-subtle'
            : isOpen
              ? 'border-accent-ring ring-2 ring-accent-soft text-accent'
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

      {isOpen && (
        <div
          ref={dropdownRef}
          className={`
            fixed z-[9999] bg-surface border border-line
            rounded-lg shadow-lg overflow-hidden animate-fade-in
            ${dropdownSizeClasses[size]}
          `}
          style={{
            top: `${dropdownPosition.top}px`,
            left: `${dropdownPosition.left}px`,
            minWidth: `${dropdownPosition.width}px`,
            maxHeight: '240px', // 最大高度，超出滚动
            transformOrigin: dropdownPosition.isUpward ? 'bottom' : 'top'
          }}
        >
          <div className="py-1 max-h-[240px] overflow-y-auto">
            {options.map((option) => {
              const isSelected = option.value === value;
              return (
                <div
                  key={option.value}
                  onClick={() => handleSelect(option.value)}
                  className={`
                    px-3 py-2 cursor-pointer transition-colors flex items-center justify-between whitespace-nowrap
                    ${isSelected
                      ? 'bg-accent-soft text-accent font-medium'
                      : 'text-fg-body hover:bg-surface-sub'
                    }
                  `}
                >
                  <span>{option.label}</span>
                  {isSelected && (
                    <CheckCircle2 size={14} className="text-accent-ring ml-2 flex-shrink-0" />
                  )}
                </div>
              );
            })}
          </div>
        </div>
      )}
    </div>
  );
};
