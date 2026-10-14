// ============================================
// SettingItem - 可编辑的设置项组件
// v5.1.0 (2025-12-08)
// 2026-10-07：桌面化——系统设置式单行（左侧名称与说明、右侧控件），开关 / 输入框改为桌面尺寸
// ============================================

import { CustomSelect } from '../../../components/ui';
import Switch from '@components/desktop/Switch.jsx';
import { FormBlock, FormRow } from '@components/desktop/FormSection.jsx';

// 值类型到输入类型的映射
const VALUE_TYPE_CONFIG = {
  string: { type: 'text', step: null },
  int: { type: 'number', step: 1 },
  float: { type: 'number', step: 0.1 },
  bool: { type: 'checkbox', step: null },
  duration: { type: 'text', step: null, placeholder: '例如: 30s, 5m, 1h' },
  password: { type: 'password', step: null }
};

// 菜单栏文字（仅 macOS），取值与后端 tray.MenuBar* 常量一致
const MENUBAR_LABEL_OPTIONS = [
  { value: 'off', label: '不显示' },
  { value: 'today_cost', label: '今日花费' },
  { value: 'claude_quota', label: 'Claude 订阅剩余' },
  { value: 'codex_quota', label: 'Codex 账号剩余' }
];

// 用下拉框编辑的设置：category.key → 选项
const SELECT_SETTING_OPTIONS = {
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

// 输入框宽度按设置项内容长度给
const INPUT_WIDTHS = {
  endpoint_url: 'w-72',
  api_key: 'w-56',
  model: 'w-56',
  token: 'w-48'
};

const RestartBadge = () => (
  <span className="rounded border border-warn-line px-1 text-[10.5px] leading-[15px] text-warn">需重启</span>
);

const CodexIdentityModeSetting = ({ value, onChange, disabled }) => (
  <FormBlock>
    <div className="flex items-center justify-between gap-4">
      <span className="text-[13px] text-fg">身份处理</span>
      <span className="text-xs text-fg-muted">
        {CODEX_IDENTITY_MODE_OPTIONS.find((option) => option.value === value)?.label || value}
      </span>
    </div>
    <p className="mt-1 text-[11.5px] leading-snug text-fg-muted">{CODEX_IDENTITY_MODE_HELP[value]}</p>
    <p className="mt-1 text-[11.5px] leading-snug text-fg-muted">使用客户端订阅凭据直通时，始终保留客户端身份，不受此设置影响。</p>
    <details className="mt-2">
      <summary className="text-xs text-fg-subtle">高级排障</summary>
      <div className="mt-2 flex justify-end">
        <CustomSelect
          options={CODEX_IDENTITY_MODE_OPTIONS}
          value={value}
          onChange={onChange}
          disabled={disabled}
          size="md"
        />
      </div>
    </details>
  </FormBlock>
);

const SettingItem = ({
  setting,
  value,
  onChange,
  disabled = false
}) => {
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
  const emit = (nextValue) => onChange(setting.category, setting.key, nextValue);
  const rowProps = {
    label: displayLabel,
    description: setting.description,
    badge: setting.requires_restart ? <RestartBadge /> : null
  };

  if (setting.category === 'codex_identity' && setting.key === 'mode') {
    return <CodexIdentityModeSetting value={value} onChange={emit} disabled={disabled} />;
  }

  // 布尔类型使用开关
  if (valueType === 'bool') {
    return (
      <FormRow {...rowProps}>
        <Switch checked={value === 'true' || value === true} onChange={(checked) => emit(checked ? 'true' : 'false')} disabled={disabled} label={displayLabel} />
      </FormRow>
    );
  }

  // 策略类型、失败处理动作、菜单栏文字使用下拉选择
  const selectOptions = SELECT_SETTING_OPTIONS[`${setting.category}.${setting.key}`];
  if (selectOptions) {
    return (
      <FormRow {...rowProps}>
        <CustomSelect options={selectOptions} value={value} onChange={emit} disabled={disabled} size="md" />
      </FormRow>
    );
  }

  // 其他类型使用文本/数字输入框
  const input = (
    <FormRow {...rowProps}>
      <input
        type={config.type}
        autoComplete={valueType === 'password' ? 'new-password' : undefined}
        value={value}
        onChange={(event) => emit(event.target.value)}
        disabled={disabled}
        step={setting.key === 'fixed_price_usd' ? 0.001 : config.step}
        placeholder={inputPlaceholder}
        aria-label={displayLabel}
        className={`desk-field text-right font-mono ${INPUT_WIDTHS[setting.key] || 'w-28'}`}
      />
    </FormRow>
  );
  if (setting.category === 'codex_identity' && setting.key === 'compat_version') {
    return (
      <details className="px-3.5 py-2">
        <summary className="text-xs text-fg-subtle">高级兼容设置</summary>
        <div className="-mx-3.5">{input}</div>
      </details>
    );
  }
  return input;
};

export default SettingItem;
