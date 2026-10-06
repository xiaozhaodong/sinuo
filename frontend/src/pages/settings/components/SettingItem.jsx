// ============================================
// SettingItem - 可编辑的设置项组件
// v5.1.0 (2025-12-08)
// ============================================

import { useState, useEffect } from 'react';
import { AlertTriangle } from 'lucide-react';
import { CustomSelect } from '../../../components/ui';

// 值类型到输入类型的映射
const VALUE_TYPE_CONFIG = {
  string: { type: 'text', step: null },
  int: { type: 'number', step: 1 },
  float: { type: 'number', step: 0.1 },
  bool: { type: 'checkbox', step: null },
  duration: { type: 'text', step: null, placeholder: '例如: 30s, 5m, 1h' },
  password: { type: 'password', step: null }
};

// 策略类型选项
const STRATEGY_OPTIONS = [
  { value: 'priority', label: 'priority (优先级)' },
  { value: 'fastest', label: 'fastest (最快响应)' }
];

// 失败处理动作选项
const FAILURE_ACTION_OPTIONS = [
  { value: 'failover', label: 'failover - 故障转移到其他端点' },
  { value: 'reject', label: 'reject - 直接拒绝返回错误' }
];

// 菜单栏文字（仅 macOS），取值与后端 tray.MenuBar* 常量一致
const MENUBAR_LABEL_OPTIONS = [
  { value: 'off', label: '不显示' },
  { value: 'today_cost', label: '今日花费' },
  { value: 'claude_quota', label: 'Claude 订阅剩余' },
  { value: 'codex_quota', label: 'Codex 账号剩余' }
];

// 用下拉框编辑的设置：category.key → 选项
const SELECT_SETTING_OPTIONS = {
  'strategy.type': STRATEGY_OPTIONS,
  'request.failure_action': FAILURE_ACTION_OPTIONS,
  'desktop.menubar_label': MENUBAR_LABEL_OPTIONS
};

const CODEX_IDENTITY_MODE_OPTIONS = [
  { value: 'auto', label: '自动（推荐）' },
  { value: 'disabled', label: '强制关闭（排障）' },
  { value: 'enabled', label: '强制开启（排障）' }
];

const CODEX_IDENTITY_MODE_HELP = {
  auto: '按每次实际选中的账号自动处理：官方 OAuth 隔离会话，API Key / 第三方保留原有身份信息。切换接入或路由模式无需调整。',
  disabled: '已覆盖自动判断：所有账号池请求停止身份补全和会话映射。排障结束后可恢复自动。',
  enabled: '已覆盖自动判断：官方 OAuth 映射身份，API Key / 第三方也执行身份清理（含 client_metadata）。排障结束后可恢复自动。'
};

const CodexIdentityModeSetting = ({ value, onChange, disabled }) => (
  <div className="py-3 border-b border-line-soft last:border-0">
    <div className="flex items-center justify-between gap-4">
      <span className="text-sm font-medium text-fg-body">身份处理</span>
      <span className="text-xs text-fg-subtle">
        {CODEX_IDENTITY_MODE_OPTIONS.find((option) => option.value === value)?.label || value}
      </span>
    </div>
    <p className="text-xs text-fg-subtle mt-1">{CODEX_IDENTITY_MODE_HELP[value]}</p>
    <p className="text-xs text-fg-subtle mt-1">使用客户端订阅凭据直通时，始终保留客户端身份，不受此设置影响。</p>
    <details className="mt-3">
      <summary className="text-xs text-fg-subtle cursor-pointer">高级排障</summary>
      <div className="flex justify-end mt-2">
        <CustomSelect
          options={CODEX_IDENTITY_MODE_OPTIONS}
          value={value}
          onChange={onChange}
          disabled={disabled}
          size="md"
        />
      </div>
    </details>
  </div>
);

const SettingItem = ({
  setting,
  value,
  onChange,
  disabled = false
}) => {
  const [localValue, setLocalValue] = useState(value);

  // 同步外部 value 变化
  useEffect(() => {
    setLocalValue(value);
  }, [value]);

  // 检测值类型（优先使用 setting.value_type，然后尝试自动检测）
  const valueType = setting.value_type || (
    (value === 'true' || value === 'false' || value === true || value === false) ? 'bool' : 'string'
  );

  const config = VALUE_TYPE_CONFIG[valueType] || VALUE_TYPE_CONFIG.string;
  const inputPlaceholder = valueType === 'password' && setting.secret_configured
    ? '已配置，留空则保持不变'
    : config.placeholder;

  // 显示标签（优先使用 label，没有则使用 key）
  const displayLabel = setting.label || setting.key;

  // 处理值变更
  const handleChange = (e) => {
    let newValue;
    if (valueType === 'bool') {
      newValue = e.target.checked ? 'true' : 'false';
    } else {
      newValue = e.target.value;
    }
    setLocalValue(newValue);
    onChange(setting.category, setting.key, newValue);
  };

  if (setting.category === 'codex_identity' && setting.key === 'mode') {
    return (
      <CodexIdentityModeSetting
        value={localValue}
        onChange={(newValue) => {
          setLocalValue(newValue);
          onChange(setting.category, setting.key, newValue);
        }}
        disabled={disabled}
      />
    );
  }

  // 布尔类型使用 Toggle 开关
  if (valueType === 'bool') {
    const isChecked = localValue === 'true' || localValue === true;
    return (
      <div className="flex justify-between items-center py-3 border-b border-line-soft last:border-0">
        <div className="flex-1 min-w-0 pr-4">
          <div className="flex items-center gap-2">
            <span className="text-sm font-medium text-fg-body">{displayLabel}</span>
            {setting.requires_restart && (
              <span className="inline-flex items-center px-1.5 py-0.5 rounded text-[10px] font-medium bg-warn-soft text-warn">
                <AlertTriangle size={10} className="mr-0.5" />
                重启生效
              </span>
            )}
          </div>
          {setting.description && (
            <p className="text-xs text-fg-subtle mt-0.5">{setting.description}</p>
          )}
        </div>
        <label className="relative inline-flex items-center cursor-pointer">
          <input
            type="checkbox"
            checked={isChecked}
            onChange={handleChange}
            disabled={disabled}
            className="sr-only peer"
          />
          <div className={`
            w-11 h-6 bg-surface-emph rounded-full peer
            peer-checked:bg-indigo-600
            peer-focus:ring-2 peer-focus:ring-accent-soft
            after:content-[''] after:absolute after:top-0.5 after:left-[2px]
            after:bg-surface after:rounded-full after:h-5 after:w-5
            after:transition-all after:shadow-sm
            peer-checked:after:translate-x-full
            ${disabled ? 'opacity-50 cursor-not-allowed' : ''}
          `}></div>
        </label>
      </div>
    );
  }

  // 策略类型、失败处理动作、菜单栏文字使用下拉选择
  const selectOptions = SELECT_SETTING_OPTIONS[`${setting.category}.${setting.key}`];
  if (selectOptions) {
    const handleSelectChange = (newValue) => {
      setLocalValue(newValue);
      onChange(setting.category, setting.key, newValue);
    };

    return (
      <div className="flex justify-between items-center py-3 border-b border-line-soft last:border-0">
        <div className="flex-1 min-w-0 pr-4">
          <div className="flex items-center gap-2">
            <span className="text-sm font-medium text-fg-body">{displayLabel}</span>
            {setting.requires_restart && (
              <span className="inline-flex items-center px-1.5 py-0.5 rounded text-[10px] font-medium bg-warn-soft text-warn">
                <AlertTriangle size={10} className="mr-0.5" />
                重启生效
              </span>
            )}
          </div>
          {setting.description && (
            <p className="text-xs text-fg-subtle mt-0.5">{setting.description}</p>
          )}
        </div>
        <CustomSelect
          options={selectOptions}
          value={localValue}
          onChange={handleSelectChange}
          disabled={disabled}
          size="md"
        />
      </div>
    );
  }

  // 其他类型使用文本/数字输入框
  const input = (
    <div className="flex justify-between items-center py-3 border-b border-line-soft last:border-0">
      <div className="flex-1 min-w-0 pr-4">
        <div className="flex items-center gap-2">
          <span className="text-sm font-medium text-fg-body">{displayLabel}</span>
          {setting.requires_restart && (
            <span className="inline-flex items-center px-1.5 py-0.5 rounded text-[10px] font-medium bg-warn-soft text-warn">
              <AlertTriangle size={10} className="mr-0.5" />
              重启生效
            </span>
          )}
        </div>
        {setting.description && (
          <p className="text-xs text-fg-subtle mt-0.5">{setting.description}</p>
        )}
      </div>
      <input
        type={config.type}
        autoComplete={valueType === 'password' ? 'new-password' : undefined}
        value={localValue}
        onChange={handleChange}
        disabled={disabled}
        step={setting.key === 'fixed_price_usd' ? 0.001 : config.step}
        placeholder={inputPlaceholder}
        className={`
          w-32 px-3 py-1.5 bg-surface-sub border border-line rounded-lg text-sm text-fg-body
          font-mono text-right
          focus:outline-none focus:ring-2 focus:ring-accent-ring focus:border-accent-line
          ${disabled ? 'opacity-50 cursor-not-allowed' : ''}
          ${setting.key === 'token' ? 'w-48' : ''}
          ${setting.key === 'endpoint_url' ? 'w-72 max-w-[58%]' : ''}
          ${setting.key === 'api_key' || setting.key === 'model' ? 'w-56 max-w-[50%]' : ''}
        `}
      />
    </div>
  );
  if (setting.category === 'codex_identity' && setting.key === 'compat_version') {
    return (
      <details className="py-3">
        <summary className="text-xs text-fg-subtle cursor-pointer">高级兼容设置</summary>
        {input}
      </details>
    );
  }
  return input;
};

export default SettingItem;
