import { FormRow, FormSection } from '@components/desktop/FormSection.jsx';
import SegmentedControl from '@components/desktop/SegmentedControl.jsx';
import { Button } from '@components/ui';
import { COST_MODE_OPTIONS } from './billingForms.js';

const SPLIT_FIELDS_BY_CHANNEL = {
  endpoint: [
    ['inputCostMultiplier', '输入'],
    ['outputCostMultiplier', '输出'],
    ['cacheReadCostMultiplier', '缓存读取'],
    ['cacheCreationCostMultiplier', '5 分钟缓存创建'],
    ['cacheCreationCostMultiplier1h', '1 小时缓存创建']
  ],
  account: [
    ['inputCostMultiplier', '输入'],
    ['outputCostMultiplier', '输出'],
    ['cacheReadCostMultiplier', '缓存读取'],
    ['cacheCreationCostMultiplier', '缓存创建（30m / 12h）']
  ],
  default: [
    ['inputCostMultiplier', '输入'],
    ['outputCostMultiplier', '输出'],
    ['cacheReadCostMultiplier', '缓存读取'],
    ['cacheCreationCostMultiplier', '缓存创建（5m / 30m / 12h）'],
    ['cacheCreationCostMultiplier1h', '1 小时缓存创建']
  ]
};

const MultiplierInput = ({ label, value, step, invalid = false, disabled, onChange }) => (
  <input
    type="number"
    step={step}
    aria-label={label}
    aria-invalid={invalid}
    disabled={disabled}
    value={value ?? ''}
    placeholder="1.0"
    onChange={(event) => onChange(event.target.value)}
    className="desk-field w-24 text-right"
  />
);

const ruleSummary = (rule) => {
  const parts = [];
  if (rule.fast_multiplier != null) parts.push(`Fast ×${rule.fast_multiplier}`);
  if (rule.period) parts.push(`固定时段 ${rule.period}`);
  (rule.tiers || []).forEach(({ threshold, multiplier }, index) => {
    if (threshold == null && multiplier == null) return;
    const tier = [`长档 ${index + 1}`];
    if (threshold != null) tier.push(`> ${threshold.toLocaleString()} Token`);
    if (multiplier != null) tier.push(`×${multiplier}`);
    parts.push(tier.join(' '));
  });
  return parts.join(' · ');
};

// 渠道（账号 / 端点）表单里的「计费」分区：按模型规则入口 + 成本倍率（统一 / 分项）。
// channel 为 { type, id, name }，id 为 0 表示尚未保存；fixed 表示该渠道倍率固定为 1.0、不可调；
// step 是倍率输入框的步长，账号用 0.01（0.05 这类值不被原生校验拒绝），端点用 any。
const ChannelBillingSection = ({
  channel,
  onEditRules,
  rules,
  listError,
  reloadRules,
  disabled = false,
  fixed = false,
  step = 'any',
  mode,
  onModeChange,
  values,
  onValueChange,
  error,
  invalidFields = []
}) => {
  const saved = channel.id > 0;
  const models = Object.keys(rules || {}).sort();
  const invalidSet = new Set(invalidFields);
  const splitFields = SPLIT_FIELDS_BY_CHANNEL[channel?.type] || SPLIT_FIELDS_BY_CHANNEL.default;
  return (
    <FormSection title="计费">
      <FormRow label="模型计费规则" description={!saved ? '保存后可配置' : listError ? undefined
        : !rules ? '正在读取规则…' : models.length ? `已配置 ${models.length} 个模型` : '跟随官方价表'}>
        <Button type="button" size="sm" variant="secondary" disabled={disabled || !saved}
          onClick={(event) => onEditRules(event.currentTarget)}>
          配置模型规则
        </Button>
      </FormRow>
      {listError && <p role="alert" className="px-3.5 py-2 text-xs text-danger">{listError} <button type="button" className="underline" onClick={reloadRules}>重试</button></p>}
      {models.length > 0 && (
        <ul aria-label="已配置的模型计费规则" className="px-3.5 py-1">
          {models.map((model) => (
            <li key={model}>
              <button type="button" disabled={disabled}
                onClick={(event) => onEditRules(event.currentTarget, model)}
                className="flex w-full flex-wrap items-baseline justify-between gap-x-3 rounded py-1.5 text-left text-xs hover:bg-surface-mut">
                <span className="break-all font-medium text-accent">{model}</span>
                <span className="text-fg-muted">{ruleSummary(rules[model])}</span>
              </button>
            </li>
          ))}
        </ul>
      )}
      <div>
        <FormRow label="成本倍率" description={!fixed && mode === 'split' ? '分项倍率须大于 0，留空按 1.0 处理' : undefined}>
          {fixed ? (
            <span className="text-xs text-fg-subtle">固定 1.0</span>
          ) : (
            <>
              <SegmentedControl
                options={COST_MODE_OPTIONS}
                value={mode}
                onChange={onModeChange}
                ariaLabel="成本倍率模式"
                disabled={disabled}
              />
              {mode === 'unified' && (
                <MultiplierInput
                  label="统一倍率"
                  step={step}
                  value={values.costMultiplier}
                  invalid={Boolean(error) && (invalidSet.size === 0 || invalidSet.has('costMultiplier'))}
                  disabled={disabled}
                  onChange={(value) => onValueChange('costMultiplier', value)}
                />
              )}
            </>
          )}
        </FormRow>
        {error && mode === 'unified' && (
          <p role="alert" className="px-3.5 pb-2 text-right text-[11.5px] text-danger">
            {error}
          </p>
        )}
      </div>
      {!fixed &&
        mode === 'split' &&
        splitFields.map(([key, label]) => (
          <FormRow key={key} label={label}>
            <MultiplierInput
              label={`${label}倍率`}
              step={step}
              value={values[key]}
              invalid={invalidSet.has(key)}
              disabled={disabled}
              onChange={(value) => onValueChange(key, value)}
            />
          </FormRow>
        ))}
      {!fixed && mode === 'split' && error && (
        <p role="alert" className="px-3.5 py-2 text-right text-[11.5px] text-danger">
          {error}
        </p>
      )}
    </FormSection>
  );
};

export default ChannelBillingSection;
