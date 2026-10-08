// ============================================
// DynamicConfigSection - 动态配置区块组件
// 用于显示未预设的配置section
// 2025-12-01
// ============================================

import { Settings } from 'lucide-react';
import ConfigSection from './ConfigSection.jsx';
import { FormRow } from '@components/desktop/FormSection.jsx';

const DynamicConfigSection = ({ sectionName, sectionData }) => {
  // 格式化纳秒为可读时间格式
  const formatDuration = (ns) => {
    const seconds = ns / 1000000000;
    if (seconds < 1) return `${Math.round(ns / 1000000)}ms`;
    if (seconds < 60) return `${Math.round(seconds)}s`;
    if (seconds < 3600) return `${Math.round(seconds / 60)}m`;
    return `${Math.round(seconds / 3600)}h`;
  };

  // 判断是否为duration类型（纳秒数）
  const isDuration = (key, value) => {
    if (typeof value !== 'number') return false;
    const durationKeys = [
      'interval', 'timeout', 'delay', 'duration', 'ttl',
      'cooldown', 'heartbeat', 'idle', 'lifetime'
    ];
    return durationKeys.some(k => key.toLowerCase().includes(k));
  };

  // 判断值的类型
  const detectType = (key, value) => {
    if (typeof value === 'boolean') return 'boolean';
    if (isDuration(key, value)) return 'duration';
    if (typeof value === 'number') return 'number';
    if (typeof value === 'string' && /^\d+[smh]$/.test(value)) return 'duration';
    return 'text';
  };

  // 格式化section名称为标题
  const formatTitle = (name) => {
    // 将下划线转换为空格并首字母大写
    return name
      .split('_')
      .map(word => word.charAt(0).toUpperCase() + word.slice(1))
      .join(' ');
  };

  // 渲染嵌套对象
  const renderValue = (key, value) => {
    if (value === null || value === undefined) {
      return <span className="text-fg-subtle">-</span>;
    }

    if (typeof value === 'object' && !Array.isArray(value)) {
      // 嵌套对象，递归显示
      return (
        <div className="min-w-0 divide-y divide-line-soft">
          {Object.entries(value).map(([nestedKey, val]) => (
            <FormRow key={nestedKey} label={nestedKey}>
          <div className="selectable min-w-0 break-all text-right text-xs">{renderValue(nestedKey, val)}</div>
            </FormRow>
          ))}
        </div>
      );
    }

    if (Array.isArray(value)) {
      return (
        <span className="rounded border border-line bg-surface-mut px-1.5 text-[11px] leading-[18px] text-fg-body">
          Array ({value.length} items)
        </span>
      );
    }

    const type = detectType(key, value);
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
      const displayValue = typeof value === 'number' ? formatDuration(value) : value;
      return <span className="font-mono text-xs text-accent">{displayValue}</span>;
    }

    if (type === 'number') {
      return <span className="font-mono text-xs text-fg-body">{value}</span>;
    }

    return <span className="font-mono text-xs text-fg-body break-all">{String(value)}</span>;
  };

  if (!sectionData || typeof sectionData !== 'object') {
    return null;
  }

  return (
    <ConfigSection title={formatTitle(sectionName)} icon={Settings}>
      {Object.entries(sectionData).map(([key, value]) => (
        <FormRow key={key} label={key}>
        <div className="selectable min-w-0 break-all text-right text-xs">{renderValue(key, value)}</div>
        </FormRow>
      ))}
    </ConfigSection>
  );
};

export default DynamicConfigSection;
