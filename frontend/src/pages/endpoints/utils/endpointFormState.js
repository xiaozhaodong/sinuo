import { buildSoftFailurePolicyPayload, createSoftFailurePolicyState, validateSoftFailurePolicyState } from '../../../utils/softFailurePolicy.js';

import {
  createEmptyEndpointModelRewriteRule,
  parseEndpointModelRewriteSettings,
  serializeEndpointModelRewriteRules
} from './modelRewrite.js';

const numericValue = (value, fallback) => {
  const parsed = Number(value);
  return Number.isFinite(parsed) ? parsed : fallback;
};

export const summarizeEndpointAuthentication = (endpoint = {}) => {
  const methods = [];
  if (endpoint.tokenMasked) methods.push('Token');
  if (endpoint.apiKeyMasked) methods.push('API Key');
  const headerCount = Object.keys(endpoint.headers || {}).length;
  if (headerCount > 0) methods.push(`${headerCount} Header`);
  return methods.length > 0 ? methods.join(' + ') : '无认证';
};

export const createEndpointFormState = (endpoint = null) => {
  const modelRewriteSettings = parseEndpointModelRewriteSettings(endpoint?.modelRewriteRules || '');
  return {
    ...createSoftFailurePolicyState(endpoint || {}),
    name: endpoint?.name || '',
    url: endpoint?.url || '',
    token: '',
    apiKey: '',
    clearToken: false,
    clearApiKey: false,
    hasStoredToken: Boolean(endpoint?.tokenMasked),
    hasStoredApiKey: Boolean(endpoint?.apiKeyMasked),
    headerRows: Object.entries(endpoint?.headers || {}).map(([name, value]) => ({ name, value })),
    priority: endpoint?.priority ?? 1,
    failoverEnabled: endpoint?.failoverEnabled !== false,
    availabilityEnabled: endpoint?.availabilityEnabled !== false,
    cooldownSeconds: endpoint?.cooldownSeconds ?? '',
    timeoutSeconds: endpoint?.timeoutSeconds ?? 300,
    supportsCountTokens: endpoint?.supportsCountTokens === true,
    modelRewriteEnabled: modelRewriteSettings.enabled,
    modelRewriteRules: modelRewriteSettings.rules.length > 0
      ? modelRewriteSettings.rules
      : [createEmptyEndpointModelRewriteRule()],
    costMultiplier: endpoint?.costMultiplier ?? 1,
    inputCostMultiplier: endpoint?.inputCostMultiplier ?? 1,
    outputCostMultiplier: endpoint?.outputCostMultiplier ?? 1,
    cacheCreationCostMultiplier: endpoint?.cacheCreationCostMultiplier ?? 1,
    cacheCreationCostMultiplier1h: endpoint?.cacheCreationCostMultiplier1h ?? 1,
    cacheReadCostMultiplier: endpoint?.cacheReadCostMultiplier ?? 1
  };
};

export const validateEndpointFormState = (state = {}) => {
  const errors = {};
  const failurePolicyError = validateSoftFailurePolicyState(state);
  if (failurePolicyError) errors.softFailurePolicy = failurePolicyError;
  if (!String(state.name || '').trim()) {
    errors.name = '请输入端点名称';
  }

  const rawURL = String(state.url || '').trim();
  if (!rawURL) {
    errors.url = '请输入端点 URL';
  } else {
    try {
      const parsed = new URL(rawURL);
      if (!['http:', 'https:'].includes(parsed.protocol) || !parsed.hostname || parsed.username || parsed.password) {
        errors.url = '请输入不含账号密码的 HTTP(S) URL';
      }
    } catch {
      errors.url = '请输入有效的 HTTP(S) URL';
    }
  }

  const seenHeaders = new Set();
  for (const row of state.headerRows || []) {
    const name = String(row?.name || '').trim().toLowerCase();
    const value = String(row?.value || '').trim();
    if (!name && !value) continue;
    if (!name || !value) {
      errors.headers = 'Header 名称和值必须成对填写';
      break;
    }
    if (seenHeaders.has(name)) {
      errors.headers = 'Header 名称不能重复';
      break;
    }
    seenHeaders.add(name);
  }
  return errors;
};

export const buildEndpointFormPayload = (state = {}) => {
  const headers = {};
  for (const row of state.headerRows || []) {
    const name = String(row?.name || '').trim();
    const value = String(row?.value || '').trim();
    if (name && value) headers[name] = value;
  }

  return {
    ...buildSoftFailurePolicyPayload(state),
    name: String(state.name || '').trim(),
    url: String(state.url || '').trim(),
    token: String(state.token || '').trim(),
    apiKey: String(state.apiKey || '').trim(),
    clearToken: state.clearToken === true,
    clearApiKey: state.clearApiKey === true,
    headers,
    priority: Math.max(0, Math.trunc(numericValue(state.priority, 1))),
    failoverEnabled: state.failoverEnabled !== false,
    availabilityEnabled: state.availabilityEnabled !== false,
    cooldownSeconds: state.cooldownSeconds === '' ? '' : Math.max(0, Math.trunc(numericValue(state.cooldownSeconds, 0))),
    timeoutSeconds: Math.max(1, Math.trunc(numericValue(state.timeoutSeconds, 300))),
    supportsCountTokens: state.supportsCountTokens === true,
    modelRewriteRules: state.modelRewriteEnabled
      ? serializeEndpointModelRewriteRules(state.modelRewriteRules)
      : '',
    costMultiplier: numericValue(state.costMultiplier, 1),
    inputCostMultiplier: numericValue(state.inputCostMultiplier, 1),
    outputCostMultiplier: numericValue(state.outputCostMultiplier, 1),
    cacheCreationCostMultiplier: numericValue(state.cacheCreationCostMultiplier, 1),
    cacheCreationCostMultiplier1h: numericValue(state.cacheCreationCostMultiplier1h, 1),
    cacheReadCostMultiplier: numericValue(state.cacheReadCostMultiplier, 1)
  };
};
