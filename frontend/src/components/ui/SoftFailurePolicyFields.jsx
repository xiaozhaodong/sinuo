import { useId } from 'react';
import { MAX_SOFT_FAILURE_WINDOW_MINUTES, validateSoftFailurePolicyState } from '../../utils/softFailurePolicy.js';

const SoftFailurePolicyFields = ({ value, onChange, disabled = false, error = '' }) => {
  const id = useId();
  const validationError = error || validateSoftFailurePolicyState(value);

  return (
    <fieldset disabled={disabled} className="space-y-3 rounded-lg border border-line bg-surface-sub/50 p-4 disabled:opacity-60">
      <legend className="px-1 text-sm font-semibold text-fg-body">失败统计策略</legend>
      <label className="flex cursor-pointer items-center gap-2 text-sm text-fg-body">
        <input
          type="checkbox"
          checked={value.softFailureCustom === true}
          onChange={(event) => onChange({ softFailureCustom: event.target.checked })}
          className="h-4 w-4 rounded border-line accent-accent"
        />
        自定义统计窗口和失败阈值
      </label>
      {value.softFailureCustom ? (
        <div className="grid gap-3 sm:grid-cols-2">
          <label htmlFor={`${id}-window`} className="space-y-1.5 text-xs font-medium text-fg-muted">
            <span>统计窗口（分钟）</span>
            <input
                id={`${id}-window`}
                type="number"
                min="1"
                max={MAX_SOFT_FAILURE_WINDOW_MINUTES}
                step="1"
                required
                value={value.softFailureWindowMinutes}
                onChange={(event) => onChange({ softFailureWindowMinutes: event.target.value })}
                aria-invalid={Boolean(validationError)}
                aria-describedby={validationError ? `${id}-error` : undefined}
                className="w-full rounded-lg border border-line bg-surface px-3 py-2 text-sm text-fg outline-none focus:border-accent-line focus:ring-2 focus:ring-accent-soft"
            />
          </label>
          <label htmlFor={`${id}-threshold`} className="space-y-1.5 text-xs font-medium text-fg-muted">
            <span>失败阈值（次）</span>
            <input
                id={`${id}-threshold`}
                type="number"
                min="1"
                step="1"
                required
                value={value.softFailureThreshold}
                onChange={(event) => onChange({ softFailureThreshold: event.target.value })}
                aria-invalid={Boolean(validationError)}
                aria-describedby={validationError ? `${id}-error` : undefined}
                className="w-full rounded-lg border border-line bg-surface px-3 py-2 text-sm text-fg outline-none focus:border-accent-line focus:ring-2 focus:ring-accent-soft"
            />
          </label>
        </div>
      ) : (
        <p className="text-xs leading-5 text-fg-subtle">跟随全局设置，初始默认值为 5 分钟内累计 3 次失败。</p>
      )}
      {validationError && <p id={`${id}-error`} role="alert" className="text-xs text-danger">{validationError}</p>}
      <p className="text-xs leading-5 text-fg-subtle">累计达到阈值后按现有规则处理冷却和切换。修改此策略会重新累计失败次数。</p>
    </fieldset>
  );
};

export default SoftFailurePolicyFields;
