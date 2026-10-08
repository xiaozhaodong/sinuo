// ============================================
// ConfigItem - 配置项组件
// 2025-12-01
// ============================================

import { FormRow } from '@components/desktop/FormSection.jsx';

const ConfigItem = ({ label, value, type = 'text' }) => {
  // 格式化纳秒为可读时间格式
  const formatDuration = (ns) => {
    const seconds = ns / 1000000000;
    if (seconds < 1) return `${Math.round(ns / 1000000)}ms`;
    if (seconds < 60) return `${Math.round(seconds)}s`;
    if (seconds < 3600) return `${Math.round(seconds / 60)}m`;
    return `${Math.round(seconds / 3600)}h`;
  };

  const renderValue = () => {
    if (value === undefined || value === null) {
      return <span className="text-fg-subtle">-</span>;
    }

    if (type === 'boolean') {
      return (
        <span className={`rounded border px-1.5 text-[11px] font-medium leading-[18px] ${
          value ? 'border-success-line bg-success-soft text-success' : 'border-line bg-surface-mut text-fg-body'
        }`}>
          {value ? '启用' : '禁用'}
        </span>
      );
    }

    if (type === 'duration') {
      // 如果是数字，认为是纳秒数，需要格式化
      const displayValue = typeof value === 'number' ? formatDuration(value) : value;
      return <span className="font-mono text-accent">{displayValue}</span>;
    }

    if (type === 'number') {
      return <span className="font-mono text-fg-body">{value}</span>;
    }

    return <span className="font-mono text-fg-body break-all">{String(value)}</span>;
  };

  return (
    <FormRow label={label}>
      <span className="selectable min-w-0 break-all text-right text-xs">{renderValue()}</span>
    </FormRow>
  );
};

export default ConfigItem;
