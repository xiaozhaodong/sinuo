// 列表、详情和通知共用固定文案；不把事件中的错误原文带入 UI。
export const PRIVACY_WARNINGS = Object.freeze({
  request_fail_open: {
    stage: 'request', title: '请求脱敏失败 · 原文放行',
    message: '请求隐私处理失败，已按配置原文放行。', short: '请求脱敏失败后原文放行'
  },
  request_failed: {
    stage: 'request', title: '请求脱敏失败 · 已拦截',
    message: '请求隐私处理失败，已按策略拦截。', short: '请求脱敏失败并拦截'
  },
  response_bypassed: {
    stage: 'response', title: '响应回填未完成 · 原样放行',
    message: '响应回填未完成，已按配置原样放行，可能包含占位符。', short: '响应回填失败后原样放行'
  },
  response_failed: {
    stage: 'response', title: '响应回填失败 · 已停止交付',
    message: '响应回填失败，已停止交付后续内容。', short: '响应回填失败并停止交付'
  }
});

export const normalizePrivacyWarnings = (codes) => {
  if (!Array.isArray(codes)) return [];
  return Object.keys(PRIVACY_WARNINGS).filter((code) => codes.includes(code));
};

export const privacyWarningGroups = (codes) => ['request', 'response'].flatMap((stage) => {
  const warnings = normalizePrivacyWarnings(codes).map((code) => PRIVACY_WARNINGS[code]).filter((warning) => warning.stage === stage);
  return warnings.length ? [{ stage, title: warnings.map((w) => w.title).join('；'), message: warnings.map((w) => w.message).join(' ') }] : [];
});

export const normalizePrivacyNotification = (payload) => {
  const requestId = payload.request_id ?? payload.requestId;
  const warningCodes = normalizePrivacyWarnings(payload.warning_codes ?? payload.warningCodes);
  if (typeof requestId !== 'string' || !requestId.trim() || requestId.length > 128 || !warningCodes.length) return null;
  return {
    kind: 'privacy_warning', level: 'warning', request_id: requestId, warningCodes,
    title: warningCodes.length === 1 ? PRIVACY_WARNINGS[warningCodes[0]].title : '请求存在隐私处理异常',
    message: warningCodes.map((code) => PRIVACY_WARNINGS[code].message).join(' '), duration: 8000
  };
};
