export const MAX_SOFT_FAILURE_WINDOW_MINUTES = 153722867;

const valueFromAliases = (source, names) => {
  for (const name of names) {
    if (Object.prototype.hasOwnProperty.call(source, name) && source[name] !== undefined) return source[name];
  }
  return null;
};

const isPositiveInteger = (value, maximum = Number.MAX_SAFE_INTEGER) => {
  if (typeof value !== 'number' && typeof value !== 'string') return false;
  const number = Number(value);
  return Number.isSafeInteger(number) && number > 0 && number <= maximum;
};

const validateValues = (windowMinutes, threshold) => {
  if (!isPositiveInteger(windowMinutes, MAX_SOFT_FAILURE_WINDOW_MINUTES)) {
    return `统计窗口须为 1 至 ${MAX_SOFT_FAILURE_WINDOW_MINUTES} 分钟的整数`;
  }
  if (!isPositiveInteger(threshold)) return '失败阈值须为正整数';
  return '';
};

// 显式 null 优先于旧别名，保证恢复默认和重复归一化不会重新启用覆盖值。
export const normalizeSoftFailurePolicyPayload = (input = {}) => {
  const windowMinutes = valueFromAliases(input, ['soft_failure_window_minutes', 'softFailureWindowMinutes', 'SoftFailureWindowMinutes']);
  const threshold = valueFromAliases(input, ['soft_failure_threshold', 'softFailureThreshold', 'SoftFailureThreshold']);
  if (windowMinutes == null && threshold == null) {
    return { soft_failure_window_minutes: null, soft_failure_threshold: null };
  }
  if (windowMinutes == null || threshold == null) throw new Error('统计窗口和失败阈值必须同时填写或同时留空');
  const error = validateValues(windowMinutes, threshold);
  if (error) throw new Error(error);
  return { soft_failure_window_minutes: Number(windowMinutes), soft_failure_threshold: Number(threshold) };
};

export const createSoftFailurePolicyState = (source = {}) => {
  const policy = normalizeSoftFailurePolicyPayload(source);
  return {
    softFailureCustom: policy.soft_failure_window_minutes != null,
    softFailureWindowMinutes: String(policy.soft_failure_window_minutes ?? 5),
    softFailureThreshold: String(policy.soft_failure_threshold ?? 3)
  };
};

export const validateSoftFailurePolicyState = (state = {}) => state.softFailureCustom
  ? validateValues(state.softFailureWindowMinutes, state.softFailureThreshold)
  : '';

export const buildSoftFailurePolicyPayload = (state = {}) => normalizeSoftFailurePolicyPayload(state.softFailureCustom
  ? { soft_failure_window_minutes: state.softFailureWindowMinutes, soft_failure_threshold: state.softFailureThreshold }
  : {});
