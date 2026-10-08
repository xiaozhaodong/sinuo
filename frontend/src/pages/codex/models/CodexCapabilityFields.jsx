import { CustomSelect } from '@components/ui';
import { Property, PropertyList } from '@components/desktop/Inspector.jsx';
import { DEFAULT_INPUT_MODALITIES, REASONING_LEVELS } from '../../account-pool/utils/codexModelCatalog.js';

const VISIBILITY_OPTIONS = [
  { value: 'list', label: '显示在模型菜单' },
  { value: 'hide', label: '隐藏' },
  { value: 'none', label: '不列出（none）' }
];
const MODALITY_OPTIONS = [
  { value: 'text', label: '文本输入' },
  { value: 'image', label: '图片输入' }
];

const CheckboxLabel = ({ checked, onChange, disabled, children }) => (
  <label className="inline-flex items-center gap-2 text-xs">
    <input type="checkbox" className="app-checkbox" checked={checked} disabled={disabled} onChange={(event) => onChange(event.target.checked)} />
    {children}
  </label>
);

const CodexCapabilityFields = ({ metadata, templates, disabled, onSelectTemplate, onUpdate, onToggleLevel, onToggleModality }) => {
  const levels = metadata?.supported_reasoning_levels || [];
  const effortOptions = [...REASONING_LEVELS, ...levels.map((level) => level.effort).filter((effort) => !REASONING_LEVELS.includes(effort))];
  const defaultLevelOptions = [
    { value: '', label: '由客户端选择' },
    ...levels.map((level) => ({ value: level.effort, label: level.effort }))
  ];
  const modalities = metadata?.input_modalities ?? DEFAULT_INPUT_MODALITIES;
  const numeric = (key, value) => onUpdate(key, value === '' ? null : Number(value));

  return (
    <PropertyList labelWidth={76}>
      <Property label="能力模板" truncate={false}>
        <div title="会替换当前草稿的能力设置，保留 ID、名称和描述">
          <CustomSelect options={templates.map((template) => ({ value: template.id, label: `${template.display_name || template.id} · ${template.id}` }))}
            value="" placeholder={templates.length ? '选择能力模板…' : '暂无可用能力模板'}
            disabled={disabled || !templates.length} size="sm" className="w-full" onChange={onSelectTemplate} />
        </div>
      </Property>
      {metadata && (
        <>
          <Property label="上下文窗口" truncate={false}>
            <div className="flex items-center gap-2">
              <input aria-label="上下文窗口（tokens）" type="number" min="1" step="1" className="desk-field w-28 text-right tabular-nums"
                value={metadata.context_window ?? ''} disabled={disabled} onChange={(event) => numeric('context_window', event.target.value)} />
              <span className="text-[11.5px] text-fg-muted">tokens</span>
            </div>
          </Property>
          <Property label="最大上下文" truncate={false}>
            <div className="flex items-center gap-2">
              <input aria-label="最大上下文（tokens）" type="number" min="1" step="1" className="desk-field w-28 text-right tabular-nums"
                value={metadata.max_context_window ?? ''} disabled={disabled} onChange={(event) => numeric('max_context_window', event.target.value)} />
              <span className="text-[11.5px] text-fg-muted">tokens</span>
            </div>
          </Property>
          <Property label="有效比例" truncate={false}>
            <div className="flex items-center gap-2">
              <input aria-label="有效上下文比例（%）" type="number" min="1" max="100" step="1" className="desk-field w-16 text-right tabular-nums"
                value={metadata.effective_context_window_percent ?? ''} disabled={disabled} onChange={(event) => numeric('effective_context_window_percent', event.target.value)} />
              <span className="text-[11.5px] text-fg-muted">%</span>
            </div>
          </Property>
          <Property label="思考等级" truncate={false}>
            <div className="grid grid-cols-2 gap-x-3 gap-y-1">
              {effortOptions.map((effort) => (
                <CheckboxLabel key={effort} checked={levels.some((level) => level.effort === effort)} disabled={disabled} onChange={() => onToggleLevel(effort)}>{effort}</CheckboxLabel>
              ))}
            </div>
          </Property>
          <Property label="默认等级" truncate={false}>
            <CustomSelect options={defaultLevelOptions} value={metadata.default_reasoning_level ?? ''} placeholder="由客户端选择"
              size="sm" className="w-full" disabled={disabled} onChange={(value) => onUpdate('default_reasoning_level', value || null)} />
          </Property>
          <Property label="菜单可见性" truncate={false}>
            <CustomSelect options={VISIBILITY_OPTIONS} value={metadata.visibility} size="sm" className="w-full"
              disabled={disabled} onChange={(value) => onUpdate('visibility', value)} />
          </Property>
          <Property label="输入" truncate={false}>
            <div className="flex flex-wrap gap-x-3 gap-y-1">
              {MODALITY_OPTIONS.map((option) => (
                <CheckboxLabel key={option.value} checked={modalities.includes(option.value)} disabled={disabled}
                  onChange={(checked) => onToggleModality(option.value, checked)}>{option.label}</CheckboxLabel>
              ))}
            </div>
          </Property>
          <Property label="API" truncate={false}>
            <CheckboxLabel checked={metadata.supported_in_api === true} disabled={disabled} onChange={(checked) => onUpdate('supported_in_api', checked)}>API Key 可用</CheckboxLabel>
          </Property>
        </>
      )}
    </PropertyList>
  );
};

export default CodexCapabilityFields;
