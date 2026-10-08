import { useId } from 'react';
import { FormBlock, FormFieldRow, FormRow, FormSection } from '@components/desktop/FormSection.jsx';
import Switch from '@components/desktop/Switch.jsx';
import { MAX_SOFT_FAILURE_WINDOW_MINUTES, validateSoftFailurePolicyState } from '../../utils/softFailurePolicy.js';

const SoftFailurePolicyFields = ({ value, onChange, disabled = false, error = '' }) => {
  const id = useId();
  const validationError = error || validateSoftFailurePolicyState(value);
  return (
    <FormSection title="失败统计策略">
      <FormRow
        label="自定义统计窗口和失败阈值"
        description={[
          !value.softFailureCustom && '跟随全局设置，初始默认值为 5 分钟内累计 3 次失败。',
          '累计达到阈值后按现有规则处理冷却和切换。修改此策略会重新累计失败次数。'
        ].filter(Boolean).join(' ')}
      >
        <Switch label="自定义统计窗口和失败阈值" disabled={disabled} checked={value.softFailureCustom === true}
          onChange={(softFailureCustom) => onChange({ softFailureCustom })} />
      </FormRow>
      {value.softFailureCustom ? (
        <>
          <FormFieldRow label="统计窗口（分钟）" htmlFor={`${id}-window`}>
            <input id={`${id}-window`} type="number" min="1" max={MAX_SOFT_FAILURE_WINDOW_MINUTES} step="1" required
              disabled={disabled} value={value.softFailureWindowMinutes}
              onChange={(event) => onChange({ softFailureWindowMinutes: event.target.value })}
              aria-invalid={Boolean(validationError)} aria-describedby={validationError ? `${id}-error` : undefined}
              className="desk-field w-24 text-right" />
          </FormFieldRow>
          <FormFieldRow label="失败阈值（次）" htmlFor={`${id}-threshold`}>
            <input id={`${id}-threshold`} type="number" min="1" step="1" required disabled={disabled}
              value={value.softFailureThreshold} onChange={(event) => onChange({ softFailureThreshold: event.target.value })}
              aria-invalid={Boolean(validationError)} aria-describedby={validationError ? `${id}-error` : undefined}
              className="desk-field w-24 text-right" />
          </FormFieldRow>
        </>
      ) : null}
      {validationError && (
        <FormBlock>
          <p id={`${id}-error`} role="alert" className="text-xs text-danger">{validationError}</p>
        </FormBlock>
      )}
    </FormSection>
  );
};
export default SoftFailurePolicyFields;
