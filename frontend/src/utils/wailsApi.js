import { normalizeSoftFailurePolicyPayload } from './softFailurePolicy.js';
import { toError } from './errors.js';

// ============================================
// Wails API 适配层
// 2025-12-04
// 将 Wails Bindings 包装为与 HTTP API 兼容的接口
// ============================================

// 检测是否在 Wails 环境中运行
export const isWailsEnvironment = () => {
  if (typeof window === 'undefined') return false;
  return Boolean(
    window.location?.protocol === 'wails:' ||
    window.location?.hostname === 'wails.localhost' ||
    window._wails?.invoke ||
    window._wails?.environment?.OS ||
    window.chrome?.webview?.postMessage ||
    window.webkit?.messageHandlers?.external?.postMessage ||
    window.wails?.invoke
  );
};

// 动态导入 Wails 绑定（避免在非 Wails 环境中报错）
let WailsApp = null;
let WailsRuntime = null;

// wrapWailsBinding 把单个绑定函数的 reject 值归一化成 Error 实例。
// Wails 的 reject 有三种形态（Error / { message } / 字符串），页面层几乎都写
// `err.message || 兜底`，字符串形态会把后端说明的原因静默丢掉；在这里统一收口后
// 上层不必逐处防御。
const wrapWailsBinding = (method) => async (...args) => {
  try {
    return await method(...args);
  } catch (err) {
    throw toError(err);
  }
};

// wrapWailsBindings 把绑定模块里的全部函数包一层错误归一化；非函数成员原样保留。
export const wrapWailsBindings = (bindings) => {
  if (!bindings || typeof bindings !== 'object') return bindings;
  const wrapped = {};
  for (const name of Object.keys(bindings)) {
    const member = bindings[name];
    wrapped[name] = typeof member === 'function' ? wrapWailsBinding(member) : member;
  }
  return wrapped;
};

const loadWailsBindings = async () => {
  if (!isWailsEnvironment()) {
    return false;
  }

  try {
    WailsApp = wrapWailsBindings(await import('@wailsjs/sinuo/app.js'));
    WailsRuntime = await import('@wailsio/runtime');
    return true;
  } catch (error) {
    console.warn('Failed to load Wails bindings:', error);
    return false;
  }
};

// 初始化 Wails 绑定
let wailsInitialized = false;
let wailsInitPromise = null;

export const initWails = async () => {
  if (wailsInitialized) return true;
  if (wailsInitPromise) return wailsInitPromise;

  wailsInitPromise = loadWailsBindings().then(result => {
    wailsInitialized = result;
    return result;
  });

  return wailsInitPromise;
};

const getWailsMethod = (methodName, { optional = false } = {}) => {
  const method = WailsApp?.[methodName];
  if (typeof method === 'function') {
    return method;
  }
  if (optional) {
    return null;
  }
  throw new Error(`当前后端版本暂未提供 ${methodName}，请升级后端后重试`);
};

const parseEntityId = (id) => {
  if (id === null || id === undefined) return null;
  if (typeof id === 'number') {
    return Number.isFinite(id) ? id : null;
  }
  if (typeof id === 'string') {
    const trimmed = id.trim();
    if (!trimmed) return null;
    const numeric = Number(trimmed);
    return Number.isNaN(numeric) ? trimmed : numeric;
  }
  return null;
};

const normalizeEntityId = (id) => {
  const normalized = parseEntityId(id);
  return normalized === null ? id : normalized;
};

// ============================================
// Wails Events 适配
// ============================================

// 事件监听器映射
const eventListeners = new Map();

/**
 * 订阅 Wails 事件
 * @param {string} eventName - 事件名称
 * @param {Function} callback - 回调函数
 * @returns {Function} - 取消订阅函数
 */
export const subscribeToEvent = (eventName, callback) => {
  // 异步初始化后订阅
  let unsubscribeFunc = null;
  let isUnsubscribed = false;

  console.log(`📡 [subscribeToEvent] 开始订阅事件: ${eventName}`);

  initWails().then(() => {
    if (isUnsubscribed) {
      console.log(`📡 [subscribeToEvent] 事件 ${eventName} 已取消订阅，跳过注册`);
      return;
    }

    if (!WailsRuntime) {
      console.warn(`📡 [subscribeToEvent] Wails Runtime not loaded, 无法订阅 ${eventName}`);
      return;
    }

    console.log(`📡 [subscribeToEvent] 注册事件监听: ${eventName}`);
    unsubscribeFunc = WailsRuntime.Events.On(eventName, (event) => {
      const data = event && Object.prototype.hasOwnProperty.call(event, 'data') ? event.data : event;
      console.log(`📡 [subscribeToEvent] 收到事件 ${eventName}:`, data);
      callback(data);
    });

    // 存储监听器以便清理
    if (!eventListeners.has(eventName)) {
      eventListeners.set(eventName, []);
    }
    eventListeners.get(eventName).push({ callback, unsubscribe: unsubscribeFunc });
  });

  // 返回取消订阅函数
  return () => {
    isUnsubscribed = true;
    if (unsubscribeFunc && typeof unsubscribeFunc === 'function') {
      unsubscribeFunc();
    }
  };
};

/**
 * 取消订阅所有事件
 */
export const unsubscribeAll = () => {
  if (!WailsRuntime) return;

  eventListeners.forEach((listeners) => {
    listeners.forEach(({ unsubscribe }) => {
      if (typeof unsubscribe === 'function') {
        unsubscribe();
      }
    });
  });
  eventListeners.clear();
};

export const unsubscribeFromEvent = (eventName) => {
  if (WailsRuntime?.Events?.Off) {
    WailsRuntime.Events.Off(eventName);
  }
  eventListeners.delete(eventName);
};

export const openExternalURL = async (url) => {
  await initWails();
  if (!WailsRuntime?.Browser?.OpenURL) throw new Error('Wails not available');
  return WailsRuntime.Browser.OpenURL(url);
};

export const getRecentLogs = async (limit) => {
  await initWails();
  return getWailsMethod('GetRecentLogs')(limit);
};

export const startLogStream = async () => {
  await initWails();
  return getWailsMethod('StartLogStream')();
};

export const stopLogStream = async () => {
  await initWails();
  return getWailsMethod('StopLogStream')();
};

export const getLogStreamStatus = async () => {
  await initWails();
  return getWailsMethod('GetLogStreamStatus')();
};

// ============================================
// 系统状态 API
// ============================================

export const getSystemStatus = async () => {
  await initWails();
  if (!WailsApp) throw new Error('Wails not available');

  const status = await WailsApp.GetSystemStatus();

  // 转换为前端期望的格式
  return {
    status: status.proxy_running ? 'running' : 'stopped',
    version: status.version,
    uptime: status.uptime_seconds,
    start_time: status.start_time, // ISO8601 格式的启动时间
    proxy_running: status.proxy_running,
    proxy_port: status.proxy_port,
    proxy_host: status.proxy_host,
    active_group: status.active_group,
    config_path: status.config_path,
    auth_enabled: status.auth_enabled
  };
};

export const getConfig = async () => {
  await initWails();
  if (!WailsApp) throw new Error('Wails not available');

  return await WailsApp.GetConfig();
};

const normalizeMigrationStatus = (status = {}) => ({
  state: status.state || status.State || 'initializing',
  migrationId: status.migration_id || status.migrationId || status.MigrationID || '',
  phase: status.phase || status.Phase || '',
  error: status.error || status.Error || '',
  databasePath: status.database_path || status.databasePath || status.DatabasePath || '',
  configPath: status.config_path || status.configPath || status.ConfigPath || '',
  backupDir: status.backup_dir || status.backupDir || status.BackupDir || '',
  databaseIntegrity: status.database_integrity || status.databaseIntegrity || status.DatabaseIntegrity || '',
  backupIntegrity: status.backup_integrity || status.backupIntegrity || status.BackupIntegrity || '',
  retryAllowed: status.retry_allowed ?? status.retryAllowed ?? status.RetryAllowed ?? false,
  endpointCountBefore: status.endpoint_count_before ?? status.endpointCountBefore ?? status.EndpointCountBefore ?? 0,
  endpointCountAfter: status.endpoint_count_after ?? status.endpointCountAfter ?? status.EndpointCountAfter ?? 0,
  derivedRecordCount: status.derived_record_count ?? status.derivedRecordCount ?? status.DerivedRecordCount ?? 0
});

export const getMigrationStatus = async () => {
  await initWails();
  if (!WailsApp) return normalizeMigrationStatus({ state: 'ready' });
  const method = getWailsMethod('GetMigrationStatus');
  return normalizeMigrationStatus(await method());
};

export const retryStartupMigration = async () => {
  await initWails();
  if (!WailsApp) throw new Error('Wails not available');
  const method = getWailsMethod('RetryStartupMigration');
  return normalizeMigrationStatus(await method());
};

// ============================================
// 端点管理 API
// ============================================

export const getEndpoints = async () => {
  await initWails();
  if (!WailsApp) throw new Error('Wails not available');

  const endpoints = await WailsApp.GetEndpoints();
  const formattedEndpoints = endpoints.map(ep => ({
    name: ep.name,
    url: ep.url,
    priority: ep.priority,
    healthy: ep.healthy,
    status: ep.healthy ? 'healthy' : 'unhealthy',
    last_check: ep.last_check,
    response_time: ep.response_time_ms,
    consecutive_fail: ep.consecutive_fail,
    never_checked: ep.never_checked === true || !ep.last_check
  }));

  const healthy = formattedEndpoints.filter(e => e.healthy).length;

  return {
    endpoints: formattedEndpoints,
    total: formattedEndpoints.length,
    healthy
  };
};

export const setEndpointPriority = async (endpointName, priority) => {
  await initWails();
  if (!WailsApp) throw new Error('Wails not available');

  await WailsApp.SetEndpointPriority(endpointName, priority);
  return { success: true };
};

export const triggerHealthCheck = async (endpointName) => {
  await initWails();
  if (!WailsApp) throw new Error('Wails not available');

  await WailsApp.TriggerHealthCheck(endpointName);
  return { success: true, healthy: true, message: '连通性测试已完成' };
};

export const batchHealthCheckAll = async () => {
  await initWails();
  if (!WailsApp) throw new Error('Wails not available');

  const result = await WailsApp.BatchHealthCheckAll();
  return {
    success: result.success,
    message: result.message || '批量连通性测试完成',
    total: result.total,
    healthy_count: result.healthy_count,
    unhealthy_count: result.unhealthy_count
  };
};

const normalizeClaudeRoutingState = (state = {}) => ({
  mode: state.mode || state.Mode || 'auto',
  endpoint_name: state.endpoint_name || state.endpointName || state.EndpointName || '',
  endpointName: state.endpoint_name || state.endpointName || state.EndpointName || '',
  set_by: state.set_by || state.setBy || state.SetBy || '',
  setAt: state.set_at || state.setAt || state.SetAt || '',
  set_at: state.set_at || state.setAt || state.SetAt || '',
  fallback_enabled: state.fallback_enabled ?? state.fallbackEnabled ?? state.FallbackEnabled ?? true,
  fallbackEnabled: state.fallback_enabled ?? state.fallbackEnabled ?? state.FallbackEnabled ?? true,
  revision: state.revision ?? state.Revision ?? 0,
  fallback_reason: state.fallback_reason || state.fallbackReason || state.FallbackReason || '',
  fallbackReason: state.fallback_reason || state.fallbackReason || state.FallbackReason || '',
  last_effective_endpoint: state.last_effective_endpoint || state.lastEffectiveEndpoint || state.LastEffectiveEndpoint || '',
  lastEffectiveEndpoint: state.last_effective_endpoint || state.lastEffectiveEndpoint || state.LastEffectiveEndpoint || '',
  last_decision_at: state.last_decision_at || state.lastDecisionAt || state.LastDecisionAt || '',
  lastDecisionAt: state.last_decision_at || state.lastDecisionAt || state.LastDecisionAt || '',
  available_endpoints: state.available_endpoints || state.availableEndpoints || state.AvailableEndpoints || [],
  availableEndpoints: state.available_endpoints || state.availableEndpoints || state.AvailableEndpoints || []
});

export const getClaudeRoutingState = async () => {
  await initWails();
  if (!WailsApp) throw new Error('Wails not available');

  const method = getWailsMethod('GetClaudeRoutingState');
  const state = await method();
  return normalizeClaudeRoutingState(state);
};

export const setClaudeRoutingOverride = async ({ mode, endpointName, endpoint_name, fallbackEnabled, fallback_enabled } = {}) => {
  await initWails();
  if (!WailsApp) throw new Error('Wails not available');

  const method = getWailsMethod('SetClaudeRoutingOverride');
  const state = await method({
    mode: mode || 'auto',
    endpoint_name: endpoint_name || endpointName || '',
    fallback_enabled: fallback_enabled ?? fallbackEnabled ?? false
  });
  return normalizeClaudeRoutingState(state);
};

export const clearClaudeRoutingOverride = async () => {
  await initWails();
  if (!WailsApp) throw new Error('Wails not available');

  const method = getWailsMethod('ClearClaudeRoutingOverride');
  const state = await method();
  return normalizeClaudeRoutingState(state);
};

const normalizeModelRoutingSettings = (settings = {}) => ({
  claude_enabled: settings.claude_enabled ?? settings.claudeEnabled ?? false,
  codex_enabled: settings.codex_enabled ?? settings.codexEnabled ?? false,
  status: settings.status || 'ready',
  version: settings.version ?? 0,
  updated_at: settings.updated_at || settings.updatedAt || '',
  binding_count: settings.binding_count ?? settings.bindingCount ?? 0,
  claude_status: settings.claude_status || settings.claudeStatus || settings.status || 'ready',
  codex_status: settings.codex_status || settings.codexStatus || (settings.codex_enabled ? 'load_failed' : 'unavailable'),
  claude_version: settings.claude_version ?? settings.claudeVersion ?? settings.version ?? 0,
  codex_version: settings.codex_version ?? settings.codexVersion ?? 0,
  claude_binding_count: settings.claude_binding_count ?? settings.claudeBindingCount ?? settings.binding_count ?? settings.bindingCount ?? 0,
  codex_binding_count: settings.codex_binding_count ?? settings.codexBindingCount ?? 0
});

const normalizeRoutingEndpoint = (member = {}) => ({
  name: member.name || member.Name || '',
  exists: member.exists ?? member.Exists ?? false,
  priority: member.priority ?? member.Priority ?? 0,
  availability_enabled: member.availability_enabled ?? member.availabilityEnabled ?? false,
  auto_schedule_enabled: member.auto_schedule_enabled ?? member.autoScheduleEnabled ?? false,
  healthy: member.healthy ?? member.Healthy ?? false,
  cooldown_until: member.cooldown_until || member.cooldownUntil || '',
  cooldown_reason: member.cooldown_reason || member.cooldownReason || '',
  detail: member.detail || member.Detail || '',
  status: member.status || 'missing'
});

const normalizeModelRouteBinding = (binding = {}) => ({
  assignment_id: parseEntityId(binding.assignment_id ?? binding.assignmentId),
  kind: binding.kind || binding.Kind || 'model',
  model: binding.model || binding.Model || '',
  endpoints: (binding.endpoints || binding.Endpoints || []).map(normalizeRoutingEndpoint),
  route_mode: binding.route_mode || binding.routeMode || 'auto',
  route_target: binding.route_target || binding.routeTarget || '',
  enabled: binding.enabled ?? true,
  note: binding.note || '',
  shared_models: binding.shared_models || binding.sharedModels || [],
  last_effective_endpoint: binding.last_effective_endpoint || binding.lastEffective || '',
  last_decision_at: binding.last_decision_at || binding.lastDecisionAt || '',
  last_outcome: binding.last_outcome || binding.lastOutcome || '',
  updated_at: binding.updated_at || binding.updatedAt || ''
});

const toModelRouteBindingPayload = (input = {}) => ({
  kind: input.kind || 'model',
  model: (input.model || '').trim(),
  endpoints: (input.endpoints || []).map((name) => String(name).trim()).filter(Boolean),
  route_mode: input.route_mode || input.routeMode || 'auto',
  // 自动模式下后端拒收非空目标，这里先归零，避免用户切回自动时被校验打回。
  route_target: (input.route_mode || input.routeMode) === 'auto' ? '' : (input.route_target || input.routeTarget || '').trim(),
  enabled: input.enabled ?? true,
  note: input.note || ''
});

export const getModelRoutingSettings = async () => {
  await initWails();
  if (!WailsApp) return { unsupported: true, ...normalizeModelRoutingSettings() };
  return normalizeModelRoutingSettings(await getWailsMethod('GetModelRoutingSettings')());
};

export const updateModelRoutingSettings = async ({ claude_enabled, claudeEnabled, codex_enabled, codexEnabled } = {}) => {
  await initWails();
  if (!WailsApp) return { unsupported: true, ...normalizeModelRoutingSettings() };
  const payload = {};
  if (claude_enabled !== undefined || claudeEnabled !== undefined) payload.claude_enabled = claude_enabled ?? claudeEnabled;
  if (codex_enabled !== undefined || codexEnabled !== undefined) payload.codex_enabled = codex_enabled ?? codexEnabled;
  return normalizeModelRoutingSettings(await getWailsMethod('UpdateModelRoutingSettings')(payload));
};

export const listCodexModelRouteBindings = async () => {
  await initWails();
  if (!WailsApp) return [];
  const rows = await getWailsMethod('ListCodexModelRouteBindings')();
  return (rows || []).map((binding = {}) => ({
    kind: binding.kind || binding.Kind || 'model',
    assignment_id: parseEntityId(binding.assignment_id ?? binding.assignmentId),
    model: binding.model || binding.Model || '',
    account_ids: (binding.account_ids || binding.accountIds || []).map((id) => Number(id)).filter(Number.isFinite),
    member_enabled: Object.fromEntries(Object.entries(binding.member_enabled || binding.memberEnabled || {}).map(([id, enabled]) => [Number(id), enabled !== false])),
    route_mode: binding.route_mode || binding.routeMode || 'auto',
    route_target_account_id: Number(binding.route_target_account_id ?? binding.routeTargetID ?? binding.routeTargetId ?? 0) || 0,
    enabled: binding.enabled ?? true,
    note: binding.note || '',
    last_account_id: Number(binding.last_account_id ?? binding.lastAccountID ?? 0) || 0,
    last_outcome: binding.last_outcome || binding.lastOutcome || '',
    last_decision_at: binding.last_decision_at || binding.lastDecisionAt || '',
    updated_at: binding.updated_at || binding.updatedAt || ''
  }));
};

export const createCodexModelRouteBinding = async (input = {}) => {
  await initWails();
  if (!WailsApp) throw new Error('Wails not available');
  return await getWailsMethod('CreateCodexModelRouteBinding')(input);
};

export const deleteCodexModelRouteBinding = async (assignmentId) => {
  await initWails();
  if (!WailsApp) throw new Error('Wails not available');
  return await getWailsMethod('DeleteCodexModelRouteBinding')(assignmentId);
};

export const updateCodexModelRouteBinding = async (assignmentId, input = {}) => {
  await initWails();
  if (!WailsApp) throw new Error('Wails not available');
  return await getWailsMethod('UpdateCodexModelRouteBinding')(assignmentId, input);
};

export const listModelRouteBindings = async () => {
  await initWails();
  if (!WailsApp) return [];
  return (await getWailsMethod('ListModelRouteBindings')()).map(normalizeModelRouteBinding);
};

export const createModelRouteBinding = async (input = {}) => {
  await initWails();
  if (!WailsApp) throw new Error('Wails not available');
  return normalizeModelRouteBinding(await getWailsMethod('CreateModelRouteBinding')(toModelRouteBindingPayload(input)));
};

export const updateModelRouteBinding = async (assignmentId, input = {}) => {
  await initWails();
  if (!WailsApp) throw new Error('Wails not available');
  await getWailsMethod('UpdateModelRouteBinding')(assignmentId, toModelRouteBindingPayload(input));
  return { success: true };
};

export const deleteModelRouteBinding = async (assignmentId) => {
  await initWails();
  if (!WailsApp) throw new Error('Wails not available');
  await getWailsMethod('DeleteModelRouteBinding')(assignmentId);
  return { success: true };
};

export const testModelRouting = async (model) => {
  await initWails();
  if (!WailsApp) return { unsupported: true, model, assigned: false, scope_kind: 'global', members: [] };
  const result = await getWailsMethod('TestModelRouting')({ model: model || '' });
  return { ...result, members: (result?.members || []).map(normalizeRoutingEndpoint) };
};

export const testCodexModelRouting = async (model) => {
  await initWails();
  if (!WailsApp) return { unsupported: true, model, assigned: false, scope_kind: 'global', accounts: [] };
  return await getWailsMethod('TestCodexModelRouting')({ model: model || '' });
};

export const clearNegativeHitCache = async (endpointName = '') => {
  await initWails();
  if (!WailsApp) throw new Error('Wails not available');

  const method = getWailsMethod('ClearNegativeHitCache');
  await method(endpointName || '');
  return { success: true };
};

// ============================================
// v6.0+ 账号池 API (Wails)
// ============================================

export const parseNullableNumber = (value) => {
  if (value === null || value === undefined || value === '') {
    return null;
  }
  const numeric = Number.parseFloat(value);
  return Number.isFinite(numeric) ? numeric : null;
};

const pickAccountValue = (account, keys, fallback = '') => {
  if (!account || typeof account !== 'object') {
    return fallback;
  }

  for (const key of keys) {
    const value = account[key];
    if (value !== undefined && value !== null && value !== '') {
      return value;
    }
  }

  return fallback;
};

const mirrorAliasedField = (target, snakeKey, camelKey, value) => {
  target[snakeKey] = value;
  target[camelKey] = value;
  return target;
};

export const normalizeUpstreamAccount = (account = {}) => {
  const failurePolicy = normalizeSoftFailurePolicyPayload(account);
  const normalized = {
    id: parseEntityId(pickAccountValue(account, ['id', 'ID', 'Id', 'account_id', 'accountId', 'AccountID'], null)),
    priority: Number.parseInt(pickAccountValue(account, ['priority', 'Priority'], 1), 10) || 1,
    enabled: pickAccountValue(account, ['enabled', 'Enabled'], true) !== false,
    state: pickAccountValue(account, ['state', 'State'], 'active'),
    fingerprint: pickAccountValue(account, ['fingerprint', 'Fingerprint'], '')
  };

  const credentialRaw = pickAccountValue(account, ['credential_raw', 'credentialRaw', 'CredentialRaw'], '');
  const hasCredential = pickAccountValue(account, ['has_credential', 'hasCredential', 'HasCredential'], false) === true || Boolean(credentialRaw);

  [
    ['provider_type', 'providerType', pickAccountValue(account, ['provider_type', 'providerType', 'ProviderType'], '')],
    ['account_name', 'accountName', pickAccountValue(account, ['account_name', 'accountName', 'AccountName'], '')],
    ['is_active_selection', 'isActiveSelection', pickAccountValue(account, ['is_active_selection', 'isActiveSelection', 'IsActiveSelection'], false) === true],
    ['is_group_preferred', 'isGroupPreferred', pickAccountValue(account, ['is_group_preferred', 'isGroupPreferred', 'IsGroupPreferred'], false) === true],
    ['credential_raw', 'credentialRaw', credentialRaw],
    ['credential_raw_masked', 'credentialRawMasked', pickAccountValue(account, ['credential_raw_masked', 'credentialRawMasked', 'CredentialRawMasked'], credentialRaw)],
    ['has_credential', 'hasCredential', hasCredential],
    ['base_url', 'baseURL', pickAccountValue(account, ['base_url', 'baseURL', 'BaseURL'], '')],
    ['model_rewrite_rules', 'modelRewriteRules', pickAccountValue(account, ['model_rewrite_rules', 'modelRewriteRules', 'ModelRewriteRules'], '')],
    ['soft_failure_window_minutes', 'softFailureWindowMinutes', failurePolicy.soft_failure_window_minutes],
    ['soft_failure_threshold', 'softFailureThreshold', failurePolicy.soft_failure_threshold],
    ['enable_request_compression', 'enableRequestCompression', pickAccountValue(account, ['enable_request_compression', 'enableRequestCompression', 'EnableRequestCompression'], false) === true],
    ['strip_codex_internal_metadata', 'stripCodexInternalMetadata', pickAccountValue(account, ['strip_codex_internal_metadata', 'stripCodexInternalMetadata', 'StripCodexInternalMetadata'], false) === true],
    ['cost_multiplier', 'costMultiplier', Number.parseFloat(pickAccountValue(account, ['cost_multiplier', 'costMultiplier', 'CostMultiplier'], 1.0)) || 1.0],
    ['input_cost_multiplier', 'inputCostMultiplier', Number.parseFloat(pickAccountValue(account, ['input_cost_multiplier', 'inputCostMultiplier', 'InputCostMultiplier'], 1.0)) || 1.0],
    ['output_cost_multiplier', 'outputCostMultiplier', Number.parseFloat(pickAccountValue(account, ['output_cost_multiplier', 'outputCostMultiplier', 'OutputCostMultiplier'], 1.0)) || 1.0],
    ['cache_creation_cost_multiplier', 'cacheCreationCostMultiplier', Number.parseFloat(pickAccountValue(account, ['cache_creation_cost_multiplier', 'cacheCreationCostMultiplier', 'CacheCreationCostMultiplier'], 1.0)) || 1.0],
    ['cache_creation_cost_multiplier_1h', 'cacheCreationCostMultiplier1h', Number.parseFloat(pickAccountValue(account, ['cache_creation_cost_multiplier_1h', 'cacheCreationCostMultiplier1h', 'CacheCreationCostMultiplier1h'], 1.0)) || 1.0],
    ['cache_read_cost_multiplier', 'cacheReadCostMultiplier', Number.parseFloat(pickAccountValue(account, ['cache_read_cost_multiplier', 'cacheReadCostMultiplier', 'CacheReadCostMultiplier'], 1.0)) || 1.0],
    ['group_key', 'groupKey', pickAccountValue(account, ['group_key', 'groupKey', 'GroupKey'], '')],
    ['cooldown_until', 'cooldownUntil', pickAccountValue(account, ['cooldown_until', 'cooldownUntil', 'CooldownUntil'], '')],
    ['fail_count', 'failCount', pickAccountValue(account, ['fail_count', 'failCount', 'FailCount'], 0)],
    ['last_success_at', 'lastSuccessAt', pickAccountValue(account, ['last_success_at', 'lastSuccessAt', 'LastSuccessAt'], '')],
    ['last_error', 'lastError', pickAccountValue(account, ['last_error', 'lastError', 'LastError'], '')],
    ['plan_type', 'planType', pickAccountValue(account, ['plan_type', 'planType', 'PlanType'], '')],
    ['chatgpt_account_id', 'chatgptAccountId', pickAccountValue(account, ['chatgpt_account_id', 'chatgptAccountId', 'ChatGPTAccountID'], '')],
    ['chatgpt_user_id', 'chatgptUserId', pickAccountValue(account, ['chatgpt_user_id', 'chatgptUserId', 'ChatGPTUserID'], '')],
    ['organization_id', 'organizationId', pickAccountValue(account, ['organization_id', 'organizationId', 'OrganizationID'], '')],
    ['quota_5h_used_percent', 'quota5hUsedPercent', parseNullableNumber(pickAccountValue(account, ['quota_5h_used_percent', 'quota5hUsedPercent', 'Quota5HUsedPercent'], null))],
    ['quota_5h_reset_at', 'quota5hResetAt', pickAccountValue(account, ['quota_5h_reset_at', 'quota5hResetAt', 'Quota5HResetAt'], '')],
    ['quota_weekly_used_percent', 'quotaWeeklyUsedPercent', parseNullableNumber(pickAccountValue(account, ['quota_weekly_used_percent', 'quotaWeeklyUsedPercent', 'QuotaWeeklyUsedPercent'], null))],
    ['quota_weekly_reset_at', 'quotaWeeklyResetAt', pickAccountValue(account, ['quota_weekly_reset_at', 'quotaWeeklyResetAt', 'QuotaWeeklyResetAt'], '')],
    ['quota_status', 'quotaStatus', pickAccountValue(account, ['quota_status', 'quotaStatus', 'QuotaStatus'], '')],
    ['quota_refreshed_at', 'quotaRefreshedAt', pickAccountValue(account, ['quota_refreshed_at', 'quotaRefreshedAt', 'QuotaRefreshedAt'], '')],
    ['created_at', 'createdAt', pickAccountValue(account, ['created_at', 'createdAt', 'CreatedAt'], '')],
    ['updated_at', 'updatedAt', pickAccountValue(account, ['updated_at', 'updatedAt', 'UpdatedAt'], '')]
  ].forEach(([snakeKey, camelKey, value]) => {
    mirrorAliasedField(normalized, snakeKey, camelKey, value);
  });

  return normalized;
};

const normalizeModelRewriteSource = (sourceModel = '') => {
  const trimmed = String(sourceModel || '').trim();
  if (!trimmed) {
    return '';
  }
  return trimmed.endsWith('*') ? trimmed.slice(0, -1).trim() : trimmed;
};

const serializeModelRewriteRules = (value) => {
  if (typeof value === 'string') {
    return value.trim();
  }
  const rules = Array.isArray(value) ? value : [];
  const normalized = rules
    .map((rule = {}) => ({
      paths: Array.isArray(rule.paths) && rule.paths.length > 0
        ? rule.paths
        : ['/v1/responses', '/v1/responses/compact'],
      match: String(rule.match || 'exact').trim() || 'exact',
      from: normalizeModelRewriteSource(rule.from ?? rule.source ?? ''),
      to: String(rule.to ?? rule.target ?? '').trim()
    }))
    .filter((rule) => rule.from && rule.to);
  return normalized.length > 0 ? JSON.stringify(normalized) : '';
};

export const buildUpstreamAccountPayload = (input = {}) => {
  const {
    strip_codex_internal_metadata: stripSnake,
    stripCodexInternalMetadata: stripCamel,
    StripCodexInternalMetadata: stripGo,
    ...payloadInput
  } = input;
  for (const key of ['softFailureCustom', 'softFailureWindowMinutes', 'softFailureThreshold', 'SoftFailureWindowMinutes', 'SoftFailureThreshold']) {
    delete payloadInput[key];
  }
  const stripMetadata = stripSnake ?? stripCamel ?? stripGo;
  if (stripMetadata != null && typeof stripMetadata !== 'boolean') {
    throw new TypeError('移除 Codex 内部元数据开关必须为布尔值');
  }
  const providerType = String(input.provider_type || input.providerType || '').trim().toLowerCase();
  const isAPIKeyAccount = providerType === 'api_key';
  const isChatGPTOAuthAccount = ['chatgpt_refresh_token', 'chatgpt_rt', 'refresh_token', 'rt', 'oauth', 'openai_oauth'].includes(providerType);
  const supportsRequestCompression = isAPIKeyAccount || isChatGPTOAuthAccount;
  const normalizeMultiplier = (value) => {
    const parsed = Number.parseFloat(value);
    return Number.isFinite(parsed) && parsed > 0 ? parsed : 1.0;
  };

  return {
    ...payloadInput,
    ...normalizeSoftFailurePolicyPayload(input),
    ...(stripMetadata == null ? {} : { strip_codex_internal_metadata: stripMetadata }),
    account_name: input.account_name || input.accountName || '',
    provider_type: input.provider_type || input.providerType || '',
    credential_raw: input.credential_raw || input.credentialRaw || '',
    base_url: input.base_url || input.baseURL || '',
    model_rewrite_rules: isAPIKeyAccount ? serializeModelRewriteRules(input.model_rewrite_rules ?? input.modelRewriteRules ?? '') : '',
    enable_request_compression: supportsRequestCompression && Boolean(input.enable_request_compression ?? input.enableRequestCompression),
    cost_multiplier: isAPIKeyAccount ? normalizeMultiplier(input.cost_multiplier ?? input.costMultiplier) : 1.0,
    input_cost_multiplier: isAPIKeyAccount ? normalizeMultiplier(input.input_cost_multiplier ?? input.inputCostMultiplier) : 1.0,
    output_cost_multiplier: isAPIKeyAccount ? normalizeMultiplier(input.output_cost_multiplier ?? input.outputCostMultiplier) : 1.0,
    cache_creation_cost_multiplier: isAPIKeyAccount ? normalizeMultiplier(input.cache_creation_cost_multiplier ?? input.cacheCreationCostMultiplier) : 1.0,
    cache_creation_cost_multiplier_1h: isAPIKeyAccount ? normalizeMultiplier(input.cache_creation_cost_multiplier_1h ?? input.cacheCreationCostMultiplier1h) : 1.0,
    cache_read_cost_multiplier: isAPIKeyAccount ? normalizeMultiplier(input.cache_read_cost_multiplier ?? input.cacheReadCostMultiplier) : 1.0,
    group_key: input.group_key || input.groupKey || '',
    priority: Number.parseInt(input.priority, 10) || 1,
    enabled: input.enabled !== false
  };
};

export const normalizeAccountScheduleCandidateDecision = (candidate = {}) => ({
  account_id: parseEntityId(candidate.account_id ?? candidate.accountId ?? candidate.AccountID ?? null),
  accountId: parseEntityId(candidate.account_id ?? candidate.accountId ?? candidate.AccountID ?? null),
  account_name: candidate.account_name || candidate.accountName || candidate.AccountName || '',
  accountName: candidate.account_name || candidate.accountName || candidate.AccountName || '',
  provider_type: candidate.provider_type || candidate.providerType || candidate.ProviderType || '',
  providerType: candidate.provider_type || candidate.providerType || candidate.ProviderType || '',
  priority: Number.parseInt(candidate.priority ?? candidate.Priority, 10) || 0,
  tier_index: Number.parseInt(candidate.tier_index ?? candidate.tierIndex ?? candidate.TierIndex, 10) || 0,
  tierIndex: Number.parseInt(candidate.tier_index ?? candidate.tierIndex ?? candidate.TierIndex, 10) || 0,
  tier_label: candidate.tier_label || candidate.tierLabel || candidate.TierLabel || '',
  tierLabel: candidate.tier_label || candidate.tierLabel || candidate.TierLabel || '',
  quota_status: candidate.quota_status || candidate.quotaStatus || candidate.QuotaStatus || '',
  quotaStatus: candidate.quota_status || candidate.quotaStatus || candidate.QuotaStatus || '',
  effective_quota_remaining: parseNullableNumber(candidate.effective_quota_remaining ?? candidate.effectiveQuotaRemaining ?? candidate.EffectiveQuotaRemaining ?? null),
  effectiveQuotaRemaining: parseNullableNumber(candidate.effective_quota_remaining ?? candidate.effectiveQuotaRemaining ?? candidate.EffectiveQuotaRemaining ?? null),
  fail_count: Number.parseInt(candidate.fail_count ?? candidate.failCount ?? candidate.FailCount, 10) || 0,
  failCount: Number.parseInt(candidate.fail_count ?? candidate.failCount ?? candidate.FailCount, 10) || 0,
  last_success_at: candidate.last_success_at || candidate.lastSuccessAt || candidate.LastSuccessAt || '',
  lastSuccessAt: candidate.last_success_at || candidate.lastSuccessAt || candidate.LastSuccessAt || '',
  decision: candidate.decision || candidate.Decision || '',
  reason: candidate.reason || candidate.Reason || '',
  reason_detail: candidate.reason_detail || candidate.reasonDetail || candidate.ReasonDetail || '',
  reasonDetail: candidate.reason_detail || candidate.reasonDetail || candidate.ReasonDetail || '',
  runtime_outcome: candidate.runtime_outcome || candidate.runtimeOutcome || candidate.RuntimeOutcome || '',
  runtimeOutcome: candidate.runtime_outcome || candidate.runtimeOutcome || candidate.RuntimeOutcome || '',
  runtime_error: candidate.runtime_error || candidate.runtimeError || candidate.RuntimeError || '',
  runtimeError: candidate.runtime_error || candidate.runtimeError || candidate.RuntimeError || ''
});

export const normalizeLatestAccountScheduleSnapshot = (snapshot = {}) => {
  const hasSnapshot = (snapshot.has_snapshot ?? snapshot.hasSnapshot ?? snapshot.HasSnapshot) === true;
  const candidates = Array.isArray(snapshot.candidates || snapshot.Candidates)
    ? (snapshot.candidates || snapshot.Candidates).map(normalizeAccountScheduleCandidateDecision)
    : [];

  return {
    unsupported: snapshot.unsupported === true,
    message: snapshot.message || '',
    has_snapshot: hasSnapshot,
    hasSnapshot,
    request_id: snapshot.request_id || snapshot.requestId || snapshot.RequestID || '',
    requestId: snapshot.request_id || snapshot.requestId || snapshot.RequestID || '',
    captured_at: snapshot.captured_at || snapshot.capturedAt || snapshot.CapturedAt || '',
    capturedAt: snapshot.captured_at || snapshot.capturedAt || snapshot.CapturedAt || '',
    updated_at: snapshot.updated_at || snapshot.updatedAt || snapshot.UpdatedAt || '',
    updatedAt: snapshot.updated_at || snapshot.updatedAt || snapshot.UpdatedAt || '',
    request_path: snapshot.request_path || snapshot.requestPath || snapshot.RequestPath || '',
    requestPath: snapshot.request_path || snapshot.requestPath || snapshot.RequestPath || '',
    request_model: snapshot.request_model || snapshot.requestModel || snapshot.RequestModel || '',
    requestModel: snapshot.request_model || snapshot.requestModel || snapshot.RequestModel || '',
    model_routing_family: snapshot.model_routing_family || snapshot.modelRoutingFamily || snapshot.ModelRoutingFamily || '',
    modelRoutingFamily: snapshot.model_routing_family || snapshot.modelRoutingFamily || snapshot.ModelRoutingFamily || '',
    model_routing_version: snapshot.model_routing_version ?? snapshot.modelRoutingVersion ?? snapshot.ModelRoutingVersion ?? 0,
    modelRoutingVersion: snapshot.model_routing_version ?? snapshot.modelRoutingVersion ?? snapshot.ModelRoutingVersion ?? 0,
    route_scope_kind: snapshot.route_scope_kind || snapshot.routeScopeKind || snapshot.RouteScopeKind || 'global',
    routeScopeKind: snapshot.route_scope_kind || snapshot.routeScopeKind || snapshot.RouteScopeKind || 'global',
    route_scope_group_id: snapshot.route_scope_group_id ?? snapshot.routeScopeGroupID ?? snapshot.RouteScopeGroupID ?? 0,
    routeScopeGroupId: snapshot.route_scope_group_id ?? snapshot.routeScopeGroupID ?? snapshot.RouteScopeGroupID ?? 0,
    route_scope_group_name: snapshot.route_scope_group_name || snapshot.routeScopeGroupName || snapshot.RouteScopeGroupName || '',
    routeScopeGroupName: snapshot.route_scope_group_name || snapshot.routeScopeGroupName || snapshot.RouteScopeGroupName || '',
    route_mode: snapshot.route_mode || snapshot.routeMode || snapshot.RouteMode || 'auto',
    routeMode: snapshot.route_mode || snapshot.routeMode || snapshot.RouteMode || 'auto',
    route_target_account_id: parseEntityId(snapshot.route_target_account_id ?? snapshot.routeTargetAccountID ?? snapshot.RouteTargetAccountID ?? null),
    routeTargetAccountId: parseEntityId(snapshot.route_target_account_id ?? snapshot.routeTargetAccountID ?? snapshot.RouteTargetAccountID ?? null),
    selected_priority: Number.parseInt(snapshot.selected_priority ?? snapshot.selectedPriority ?? snapshot.SelectedPriority, 10) || 0,
    selectedPriority: Number.parseInt(snapshot.selected_priority ?? snapshot.selectedPriority ?? snapshot.SelectedPriority, 10) || 0,
    selected_tier_index: Number.parseInt(snapshot.selected_tier_index ?? snapshot.selectedTierIndex ?? snapshot.SelectedTierIndex, 10) || 0,
    selectedTierIndex: Number.parseInt(snapshot.selected_tier_index ?? snapshot.selectedTierIndex ?? snapshot.SelectedTierIndex, 10) || 0,
    selected_tier_label: snapshot.selected_tier_label || snapshot.selectedTierLabel || snapshot.SelectedTierLabel || '',
    selectedTierLabel: snapshot.selected_tier_label || snapshot.selectedTierLabel || snapshot.SelectedTierLabel || '',
    degraded_to_lower_priority: (snapshot.degraded_to_lower_priority ?? snapshot.degradedToLowerPriority ?? snapshot.DegradedToLowerPriority) === true,
    degradedToLowerPriority: (snapshot.degraded_to_lower_priority ?? snapshot.degradedToLowerPriority ?? snapshot.DegradedToLowerPriority) === true,
    selected_account_id: parseEntityId(snapshot.selected_account_id ?? snapshot.selectedAccountId ?? snapshot.SelectedAccountID ?? null),
    selectedAccountId: parseEntityId(snapshot.selected_account_id ?? snapshot.selectedAccountId ?? snapshot.SelectedAccountID ?? null),
    selected_account_name: snapshot.selected_account_name || snapshot.selectedAccountName || snapshot.SelectedAccountName || '',
    selectedAccountName: snapshot.selected_account_name || snapshot.selectedAccountName || snapshot.SelectedAccountName || '',
    final_outcome: snapshot.final_outcome || snapshot.finalOutcome || snapshot.FinalOutcome || '',
    finalOutcome: snapshot.final_outcome || snapshot.finalOutcome || snapshot.FinalOutcome || '',
    final_error: snapshot.final_error || snapshot.finalError || snapshot.FinalError || '',
    finalError: snapshot.final_error || snapshot.finalError || snapshot.FinalError || '',
    summary: snapshot.summary || snapshot.Summary || '',
    candidates
  };
};

export const getUpstreamAccounts = async () => {
  await initWails();
  if (!WailsApp) throw new Error('Wails not available');

  const method = getWailsMethod('GetUpstreamAccounts');
  const result = await method();
  const list = Array.isArray(result) ? result : (result?.accounts || result?.data || []);
  return (list || []).map(normalizeUpstreamAccount);
};

export const createUpstreamAccount = async (input) => {
  await initWails();
  if (!WailsApp) throw new Error('Wails not available');

  const method = getWailsMethod('CreateUpstreamAccount');
  const result = await method(buildUpstreamAccountPayload(input));
  return result ?? { success: true };
};

export const updateUpstreamAccount = async (id, input) => {
  await initWails();
  if (!WailsApp) throw new Error('Wails not available');

  const method = getWailsMethod('UpdateUpstreamAccount');
  const result = await method(normalizeEntityId(id), buildUpstreamAccountPayload(input));
  return result ?? { success: true };
};

export const getUpstreamAccountCredential = async (id) => {
  await initWails();
  if (!WailsApp) throw new Error('Wails not available');

  const method = getWailsMethod('GetUpstreamAccountCredential', { optional: true });
  if (!method) {
    return {
      unsupported: true,
      id: normalizeEntityId(id),
      credential_raw: '',
      credentialRaw: '',
      credential_raw_masked: '',
      credentialRawMasked: '',
      has_credential: false,
      hasCredential: false
    };
  }

  const result = await method(normalizeEntityId(id));
  return {
    id: parseEntityId(result?.id ?? result?.ID ?? normalizeEntityId(id)),
    credential_raw: result?.credential_raw || result?.credentialRaw || '',
    credentialRaw: result?.credential_raw || result?.credentialRaw || '',
    credential_raw_masked: result?.credential_raw_masked || result?.credentialRawMasked || '',
    credentialRawMasked: result?.credential_raw_masked || result?.credentialRawMasked || '',
    has_credential: (result?.has_credential ?? result?.hasCredential) === true,
    hasCredential: (result?.has_credential ?? result?.hasCredential) === true
  };
};

export const deleteUpstreamAccount = async (id) => {
  await initWails();
  if (!WailsApp) throw new Error('Wails not available');

  const method = getWailsMethod('DeleteUpstreamAccount');
  const result = await method(normalizeEntityId(id));
  return result ?? { success: true };
};

export const moveUpstreamAccountToTier = async (id, targetTier) => {
  await initWails();
  if (!WailsApp) throw new Error('Wails not available');

  const method = getWailsMethod('MoveUpstreamAccountToTier', { optional: true });
  if (!method) {
    return {
      success: false,
      unsupported: true,
      changed: false,
      message: '当前后端版本暂未提供 MoveUpstreamAccountToTier'
    };
  }

  const result = await method(normalizeEntityId(id), String(targetTier || 'primary'));
  return result ?? { success: true, changed: true };
};

export const swapUpstreamAccountGroups = async (sourceGroup, targetGroup) => {
  await initWails();
  if (!WailsApp) throw new Error('Wails not available');

  const method = getWailsMethod('SwapUpstreamAccountGroups', { optional: true });
  if (!method) {
    return {
      success: false,
      unsupported: true,
      changed: false,
      message: '当前后端版本暂未提供 SwapUpstreamAccountGroups'
    };
  }

  const result = await method(String(sourceGroup || ''), String(targetGroup || ''));
  return result ?? { success: true, changed: true };
};

export const setGroupActiveAccount = async (groupKey, id) => {
  await initWails();
  if (!WailsApp) throw new Error('Wails not available');

  const method = getWailsMethod('SetGroupActiveAccount', { optional: true });
  if (!method) {
    return {
      success: false,
      unsupported: true,
      changed: false,
      message: '当前后端版本暂未提供 SetGroupActiveAccount'
    };
  }

  const result = await method(String(groupKey || ''), normalizeEntityId(id));
  return result ?? { success: true, changed: true };
};

export const pinUpstreamAccountSelection = async (id) => {
  await initWails();
  if (!WailsApp) throw new Error('Wails not available');

  const method = getWailsMethod('PinUpstreamAccountSelection', { optional: true });
  if (!method) {
    return {
      success: false,
      unsupported: true,
      changed: false,
      message: '当前后端版本暂未提供 PinUpstreamAccountSelection'
    };
  }

  const result = await method(normalizeEntityId(id));
  return result ?? { success: true, changed: true };
};

export const enableAutomaticAccountSelection = async () => {
  await initWails();
  if (!WailsApp) throw new Error('Wails not available');

  const method = getWailsMethod('EnableAutomaticAccountSelection', { optional: true });
  if (!method) {
    return {
      success: false,
      unsupported: true,
      changed: false,
      message: '当前后端版本暂未提供 EnableAutomaticAccountSelection'
    };
  }

  const result = await method();
  return result ?? { success: true, changed: true };
};

export const toggleUpstreamAccount = async (id, enabled) => {
  await initWails();
  if (!WailsApp) throw new Error('Wails not available');

  const method = getWailsMethod('ToggleUpstreamAccount');
  const result = await method(normalizeEntityId(id), enabled !== false);
  return result ?? { success: true };
};

export const clearUpstreamAccountCooldown = async (id) => {
  await initWails();
  if (!WailsApp) throw new Error('Wails not available');
  const method = getWailsMethod('ClearUpstreamAccountCooldown', { optional: true });
  if (!method) {
    return { success: false, unsupported: true, message: '当前后端版本暂未提供 ClearUpstreamAccountCooldown' };
  }
  await method(normalizeEntityId(id));
  return { success: true };
};

export const testUpstreamAccount = async (id) => {
  await initWails();
  if (!WailsApp) throw new Error('Wails not available');

  const method = getWailsMethod('TestUpstreamAccount', { optional: true });
  if (!method) {
    return {
      success: false,
      unsupported: true,
      message: '当前后端版本暂未提供 TestUpstreamAccount'
    };
  }

  try {
    const result = await method(normalizeEntityId(id));
    if (result && typeof result === 'object') {
      return result;
    }
    return { success: true, message: '连通性测试已触发' };
  } catch (error) {
    const errorText = error?.message || String(error || '');
    if (/not found|undefined|is not a function/i.test(errorText)) {
      return {
        success: false,
        unsupported: true,
        message: '当前后端版本暂未提供 TestUpstreamAccount'
      };
    }
    throw error;
  }
};

export const getLatestAccountScheduleSnapshot = async () => {
  await initWails();
  if (!WailsApp) throw new Error('Wails not available');

  const method = getWailsMethod('GetLatestAccountScheduleSnapshot', { optional: true });
  if (!method) {
    return normalizeLatestAccountScheduleSnapshot({
      unsupported: true,
      has_snapshot: false,
      message: '当前后端版本暂未提供 GetLatestAccountScheduleSnapshot'
    });
  }

  try {
    const result = await method();
    return normalizeLatestAccountScheduleSnapshot(result || {});
  } catch (error) {
    const errorText = error?.message || String(error || '');
    if (/not found|undefined|is not a function/i.test(errorText)) {
      return normalizeLatestAccountScheduleSnapshot({
        unsupported: true,
        has_snapshot: false,
        message: '当前后端版本暂未提供 GetLatestAccountScheduleSnapshot'
      });
    }
    throw error;
  }
};

// 返回后端原始结果，字段归一化由 endpointScheduleApi.js 负责；后端未提供该方法时返回 { unsupported: true }
export const getLatestEndpointScheduleSnapshot = async () => {
  await initWails();
  if (!WailsApp) throw new Error('Wails not available');

  const method = getWailsMethod('GetLatestEndpointScheduleSnapshot', { optional: true });
  if (!method) return { unsupported: true };

  try {
    return await method();
  } catch (error) {
    const errorText = error?.message || String(error || '');
    if (/not found|undefined|is not a function/i.test(errorText)) {
      return { unsupported: true };
    }
    throw error;
  }
};

export const refreshUpstreamAccountProfile = async (id) => {
  await initWails();
  if (!WailsApp) throw new Error('Wails not available');

  const method = getWailsMethod('RefreshUpstreamAccountProfile', { optional: true });
  if (!method) {
    return {
      success: false,
      unsupported: true,
      message: '当前后端版本暂未提供 RefreshUpstreamAccountProfile'
    };
  }

  try {
    const result = await method(normalizeEntityId(id));
    if (result && typeof result === 'object') {
      return result;
    }
    return { success: true, message: '账号画像已刷新' };
  } catch (error) {
    const errorText = error?.message || String(error || '');
    if (/not found|undefined|is not a function/i.test(errorText)) {
      return {
        success: false,
        unsupported: true,
        message: '当前后端版本暂未提供 RefreshUpstreamAccountProfile'
      };
    }
    throw error;
  }
};

export const generateChatGPTOAuthLink = async () => {
  await initWails();
  const runtimeApp = typeof window !== 'undefined' ? window?.go?.main?.App : null;
  if (!WailsApp && !runtimeApp) throw new Error('Wails not available');

  const method = getWailsMethod('GenerateChatGPTOAuthLink', { optional: true });
  if (!method) {
    return {
      success: false,
      unsupported: true,
      message: '当前后端版本暂未提供 GenerateChatGPTOAuthLink'
    };
  }

  const result = await method();
  return {
    success: true,
    ...(result || {})
  };
};

export const exchangeChatGPTOAuthCallback = async (sessionId, callbackUrl) => {
  await initWails();
  const runtimeApp = typeof window !== 'undefined' ? window?.go?.main?.App : null;
  if (!WailsApp && !runtimeApp) throw new Error('Wails not available');

  const method = getWailsMethod('ExchangeChatGPTOAuthCallback', { optional: true });
  if (!method) {
    return {
      success: false,
      unsupported: true,
      message: '当前后端版本暂未提供 ExchangeChatGPTOAuthCallback'
    };
  }

  const result = await method({
    session_id: sessionId || '',
    callback_url: callbackUrl || ''
  });

  if (result && typeof result === 'object') {
    return result;
  }

  return {
    success: false,
    message: '提取 RT 失败：无响应数据'
  };
};

// ============================================
// 使用统计 API
// ============================================

/**
 * 获取使用统计（与 HTTP API 格式一致）
 * @param {Object} params - 查询参数
 * @param {string} params.period - 时间周期: "1h", "1d", "7d", "30d", "90d"
 * @param {string} params.start_date - 开始时间（优先于 period）
 * @param {string} params.end_date - 结束时间（优先于 period）
 * @param {string} params.status - 状态筛选
 * @param {string} params.model - 模型筛选
 * @param {string} params.request_family - 请求类型筛选
 * @param {string} params.upstream_name - 上游筛选
 * @returns {Promise<Object>} - 统计数据
 */
export const getUsageStats = async (params = {}) => {
  await initWails();
  if (!WailsApp) throw new Error('Wails not available');

  // 构建查询参数对象
  // 注意：前端 buildQueryParams() 返回 start_date/end_date（带下划线）
  const queryParams = {
    period: params.period || '30d',
    start_date: params.start_date || '',
    end_date: params.end_date || '',
    status: params.status || '',
    session_id: params.session_id || params.sessionId || '',
    model: params.model || '',
    request_family: params.request_family || params.requestFamily || '',
    upstream_name: params.upstream_name || params.upstreamName || '',
    source_view: params.source_view || params.sourceView || 'all'
  };
  // 请求页显式下传列表口径；概览不传，走后端按指标混合口径。
  if (typeof params.include_compaction === 'boolean') {
    queryParams.include_compaction = params.include_compaction;
  }

  console.log('📊 [Wails] GetUsageStats 参数:', queryParams);
  const data = await WailsApp.GetUsageStats(queryParams);

  // 返回与 HTTP API 一致的格式
  return mapUsageStats(data, queryParams.period);
};

// 统计展示映射独立校验：旧后端缺少中断计数时保持未知，不能伪造为零。
export const mapUsageStats = (data, period = '30d') => {
  return {
    period: data.period || period,
    total_requests: data.total_requests || 0,
    success_rate: data.success_rate || 0,
    avg_duration_ms: data.avg_duration_ms || 0,
    total_cost_usd: data.total_cost_usd || 0,
    total_tokens: data.total_tokens || 0,
    failed_requests: data.failed_requests || 0,
    interrupted_requests: Number.isFinite(data.interrupted_requests) ? data.interrupted_requests : null
  };
};

/**
 * 获取模型维度调用统计（概览页模型调用表 / Token 分布）
 * @param {Object} params - 查询参数
 * @param {string} params.period - 时间周期: "1h", "1d", "7d", "30d", "90d"（默认 1d）
 * @param {string} params.start_date - 开始时间（优先于 period）
 * @param {string} params.end_date - 结束时间（优先于 period）
 * @param {string} params.model - 模型筛选
 * @param {string} params.request_family - 请求类型筛选
 * @param {string} params.source_view - endpoint/account/subscription/all
 * @param {string} params.upstream_name - 上游筛选
 * @param {number} params.limit - Top N，默认 8
 * @returns {Promise<Object>} - { period, start_time, end_time, rows, truncated, ...汇总字段 }
 */
export const getModelUsageStats = async (params = {}) => {
  await initWails();
  if (!WailsApp) throw new Error('Wails not available');

  const queryParams = {
    period: params.period || '1d',
    start_date: params.start_date || '',
    end_date: params.end_date || '',
    model: params.model || '',
    request_family: params.request_family || params.requestFamily || '',
    source_view: params.source_view || params.sourceView || 'all',
    upstream_name: params.upstream_name || params.upstreamName || '',
    limit: Number(params.limit) || 0
  };

  const data = await WailsApp.GetModelUsageStats(queryParams);

  return {
    period: data.period || queryParams.period,
    start_time: data.start_time || '',
    end_time: data.end_time || '',
    rows: Array.isArray(data.rows) ? data.rows : [],
    truncated: data.truncated === true,
    total_requests: data.total_requests || 0,
    success_requests: data.success_requests || 0,
    failed_requests: data.failed_requests || 0,
    total_tokens: data.total_tokens || 0,
    input_tokens: data.input_tokens || 0,
    output_tokens: data.output_tokens || 0,
    cache_creation_tokens: data.cache_creation_tokens || 0,
    cache_read_tokens: data.cache_read_tokens || 0,
    total_cost_usd: data.total_cost_usd || 0,
    claude_count: data.claude_count || 0,
    codex_count: data.codex_count || 0
  };
};

export const getUsageSummary = async (startTime = '', endTime = '') => {
  await initWails();
  if (!WailsApp) throw new Error('Wails not available');

  const summary = await WailsApp.GetUsageSummary(startTime, endTime);

  return {
    total_requests: summary.total_requests || 0,
    all_time_total_requests: summary.all_time_total_requests || 0, // 全部历史请求数
    today_requests: summary.today_requests || 0,            // 今日请求数
    successful_requests: summary.success_requests || 0,
    failed_requests: summary.failed_requests || 0,
    total_input_tokens: summary.total_input_tokens || 0,
    total_output_tokens: summary.total_output_tokens || 0,
    total_cost: summary.total_cost || 0,
    today_cost: summary.today_cost || 0,                    // 今日成本
    all_time_total_cost: summary.all_time_total_cost || 0,  // 全部历史成本
    today_tokens: summary.today_tokens || 0,                // 今日 tokens
    all_time_total_tokens: summary.all_time_total_tokens || 0,  // 全部历史 tokens
    total_tokens: (summary.total_input_tokens || 0) + (summary.total_output_tokens || 0)
  };
};

// 请求列表与上游筛选候选共用的范围参数：时间 / 请求族 / 来源 / 种类
// 注意：前端 buildQueryParams() 返回 start_date/end_date（带下划线）
const buildRequestScopeParams = (params = {}) => {
  const scopeParams = {
    start_date: params.start_date || params.start_time || params.startDate || '',
    end_date: params.end_date || params.end_time || params.endDate || '',
    request_family: params.request_family || params.requestFamily || '',
    source_view: params.source_view || params.sourceView || 'all'
  };
  // 概览跳转的「仅生成」口径：只在显式收窄时下传，缺省由后端承载全部落库记录。
  if (params.include_compaction === false) {
    scopeParams.include_compaction = false;
  }
  return scopeParams;
};

// 请求页上游筛选候选：范围内实际出现过的上游，不受分页与上游 / 模型 / 状态筛选影响
export const getRequestUpstreamOptions = async (params = {}) => {
  await initWails();
  if (!WailsApp) throw new Error('Wails not available');

  const rows = await WailsApp.GetRequestUpstreamOptions(buildRequestScopeParams(params));
  return Array.isArray(rows) ? rows : [];
};

export const getRequests = async (params = {}) => {
  await initWails();
  if (!WailsApp) throw new Error('Wails not available');

  // 构建 Wails 绑定参数对象
  const queryParams = {
    page: parseInt(params.page || 1),
    page_size: parseInt(params.limit || params.pageSize || 50),
    ...buildRequestScopeParams(params),
    status: params.status || '',
    session_id: params.session_id || params.sessionId || '',
    model: params.model || '',
    upstream_name: params.upstream_name || params.upstreamName || ''
  };

  console.log('🔍 [Wails] GetRequests 参数:', queryParams);
  const result = await WailsApp.GetRequests(queryParams);
  console.log('🔍 [Wails] GetRequests 返回:', result);

  // 转换请求记录格式
  const requests = (result.requests || []).map(r => ({
    request_id: r.request_id,
    session_id: r.session_id || '',
    id: r.request_id,
    timestamp: r.timestamp,
    start_time: r.timestamp,
    request_family: r.request_family || 'other',
    request_source: r.request_source || '',
    request_kind: r.request_kind || 'legacy',
    upstream_type: r.upstream_type || 'endpoint',
    upstream_name: r.upstream_name || '',
    upstream_source_name: r.upstream_source_name || '',
    upstream_id: r.upstream_id || null,
    endpoint_name: r.endpoint || r.upstream_name || '',
    endpoint: r.endpoint || r.upstream_name || '',
    model_name: r.model,
    model: r.model,
    // v6.3 模型一致性监控原始列（一致性由前端 modelConsistency.js 派生）
    request_model: r.request_model || '',
    upstream_model: r.upstream_model || '',
    response_model: r.response_model || '',
    request_reasoning: r.request_reasoning || '',
    response_reasoning: r.response_reasoning || '',
    status: r.status,
    status_code: r.http_status,
    http_status_code: r.http_status,  // 添加 http_status_code 映射
    retry_count: r.retry_count || 0,  // 添加重试次数
    path: r.path || '',
    method: r.method || '',
    usage_known: r.usage_known !== false,
    cost_known: r.cost_known !== false,
    input_tokens: r.input_tokens,
    output_tokens: r.output_tokens,
    cache_creation_tokens: r.cache_creation_tokens,
    cache_creation_5m_tokens: r.cache_creation_5m_tokens,  // v5.0.1+
    cache_creation_1h_tokens: r.cache_creation_1h_tokens,  // v5.0.1+
    cache_read_tokens: r.cache_read_tokens,
    duration_ms: r.response_time,
    duration: r.response_time,
    first_token_ms: r.first_token_ms ?? null,
    first_token_start_at: r.first_token_start_at || '',
    first_token_at: r.first_token_at || '',
    completion_ms: r.completion_ms ?? null,
    is_streaming: r.is_streaming,
    total_cost_usd: r.cost,
    cost: r.cost,
    route_mode: r.route_mode || r.routeMode || 'auto',
    routeMode: r.route_mode || r.routeMode || 'auto',
    requested_endpoint: r.requested_endpoint || r.requestedEndpoint || '',
    requestedEndpoint: r.requested_endpoint || r.requestedEndpoint || '',
    effective_endpoint: r.effective_endpoint || r.effectiveEndpoint || '',
    effectiveEndpoint: r.effective_endpoint || r.effectiveEndpoint || '',
    fallback_reason: r.fallback_reason || r.fallbackReason || '',
    fallbackReason: r.fallback_reason || r.fallbackReason || '',
    route_decision_at: r.route_decision_at || r.routeDecisionAt || '',
    routeDecisionAt: r.route_decision_at || r.routeDecisionAt || '',
    // 错误信息字段
    failure_reason: r.failure_reason || '',
    cancel_reason: r.cancel_reason || ''
  }));

  return {
    requests,
    total: result.total,
    page: result.page,
    pageSize: result.page_size,
    totalPages: Math.ceil(result.total / result.page_size)
  };
};

/**
 * 获取单请求生命周期详情（B5.2 权威接口）。
 * 旧后端无此方法时返回 { found: false }。
 */
export const getRequestLifecycleDetail = async (requestId) => {
  await initWails();
  if (!WailsApp) throw new Error('Wails not available');

  const method = getWailsMethod('GetRequestLifecycleDetail', { optional: true });
  if (typeof method !== 'function') {
    return { found: false };
  }
  return method(requestId);
};

// ============================================
// 代理信息
// ============================================

export const getProxyURL = async () => {
  await initWails();
  if (!WailsApp) throw new Error('Wails not available');

  return await WailsApp.GetProxyURL();
};

export const isProxyRunning = async () => {
  await initWails();
  if (!WailsApp) throw new Error('Wails not available');

  return await WailsApp.IsProxyRunning();
};

// ============================================
// 图表数据 API
// ============================================

/**
 * 获取请求趋势图表数据（按 request_logs 时间分桶）
 * @param {Object} params - 查询参数
 * @param {string} params.period - 时间周期: "1h", "1d", "7d", "30d"（默认 1d）
 * @param {string} params.start_date - 开始时间（优先于 period）
 * @param {string} params.end_date - 结束时间（优先于 period）
 * @param {string} params.request_family - 请求类型筛选
 * @param {string} params.source_view - endpoint/account/subscription/all
 * @param {string} params.model - 模型筛选
 * @param {string} params.upstream_name - 上游筛选
 * @returns {Promise<Object>} - { period, granularity, start_time, end_time, points: [{time,total,success,fail}] }
 */
export const getRequestTrendData = async (params = {}) => {
  await initWails();
  if (!WailsApp) throw new Error('Wails not available');

  const queryParams = {
    period: params.period || '1d',
    start_date: params.start_date || '',
    end_date: params.end_date || '',
    model: params.model || '',
    request_family: params.request_family || params.requestFamily || '',
    source_view: params.source_view || params.sourceView || 'all',
    upstream_name: params.upstream_name || params.upstreamName || ''
  };

  const data = await WailsApp.GetRequestTrendData(queryParams);
  return {
    period: data.period || queryParams.period,
    granularity: data.granularity || 'hour',
    start_time: data.start_time || '',
    end_time: data.end_time || '',
    points: Array.isArray(data.points) ? data.points : []
  };
};

/**
 * 获取响应时间图表数据
 * @param {number} minutes - 时间范围（分钟）
 * @returns {Promise<Array>} - 图表数据点数组 [{time, avg, min, max}]
 */
export const getResponseTimeChart = async (minutes = 30) => {
  await initWails();
  if (!WailsApp) throw new Error('Wails not available');

  const data = await WailsApp.GetResponseTimeChart(minutes);
  return data || [];
};

/**
 * 获取连接活动图表数据
 * @param {number} minutes - 时间范围（分钟）
 * @returns {Promise<Array>} - 图表数据点数组 [{time, value}]
 */
export const getConnectionActivityChart = async (minutes = 60) => {
  await initWails();
  if (!WailsApp) throw new Error('Wails not available');

  const data = await WailsApp.GetConnectionActivityChart(minutes);
  // 转换为前端期望的格式 (connections 字段)
  return (data || []).map(point => ({
    time: point.time,
    timestamp: point.timestamp || point.time,
    connections: point.value
  }));
};

// ============================================
// Token 使用统计 API
// ============================================

/**
 * 获取 Token 使用统计（运行时内存数据）
 * @returns {Promise<Object>} - Token 使用数据 {input, output, cacheCreation, cacheRead}
 */
export const getTokenUsage = async () => {
  await initWails();
  if (!WailsApp) throw new Error('Wails not available');

  const data = await WailsApp.GetTokenUsage();
  return {
    input: data.input_tokens || 0,
    output: data.output_tokens || 0,
    cacheCreation: data.cache_creation_tokens || 0,
    cacheRead: data.cache_read_tokens || 0,
    total: data.total_tokens || 0
  };
};

// ============================================
// 端点健康状态图表 API
// ============================================

/**
 * 获取端点健康状态数据
 * @returns {Promise<Object>} - 健康状态数据 {healthy, unhealthy, total}
 */
export const getEndpointHealthChart = async () => {
  await initWails();
  if (!WailsApp) throw new Error('Wails not available');

  const data = await WailsApp.GetEndpointHealthChart();
  return {
    healthy: data.healthy || 0,
    unhealthy: data.unhealthy || 0,
    total: data.total || 0
  };
};

// ============================================
// 端点成本图表 API
// ============================================

/**
 * 获取当日端点成本数据
 * @returns {Promise<Array>} - 端点成本数据 [{name, tokens, cost}]
 */
export const getEndpointCosts = async () => {
  await initWails();
  if (!WailsApp) throw new Error('Wails not available');

  const data = await WailsApp.GetEndpointCosts();
  // 数据已经是 [{name, tokens, cost}] 格式
  return data || [];
};

// ============================================
// v5.0+ 端点存储管理 API (SQLite)
// ============================================

/**
 * 获取端点存储状态
 * @returns {Promise<Object>} - {enabled, storage_type, total_count, enabled_count}
 */
export const getEndpointStorageStatus = async () => {
  await initWails();
  if (!WailsApp) throw new Error('Wails not available');

  const status = await WailsApp.GetEndpointStorageStatus();
  return {
    enabled: status.enabled || false,
    storageType: status.storage_type || 'yaml',
    totalCount: status.total_count || 0,
    enabledCount: status.enabled_count || 0
  };
};

export const mapEndpointRecord = (record = {}) => ({
  id: record.id,
  name: record.name,
  url: record.url,
  tokenMasked: record.token_masked,
  apiKeyMasked: record.api_key_masked,
  headers: record.headers || {},
  priority: record.priority,
  failoverEnabled: record.failover_enabled,
  cooldownSeconds: record.cooldown_seconds,
  timeoutSeconds: record.timeout_seconds,
  supportsCountTokens: record.supports_count_tokens,
  modelRewriteRules: record.model_rewrite_rules || '',
  softFailureWindowMinutes: record.soft_failure_window_minutes ?? null,
  softFailureThreshold: record.soft_failure_threshold ?? null,
  costMultiplier: record.cost_multiplier,
  inputCostMultiplier: record.input_cost_multiplier,
  outputCostMultiplier: record.output_cost_multiplier,
  cacheCreationCostMultiplier: record.cache_creation_cost_multiplier,
  cacheCreationCostMultiplier1h: record.cache_creation_cost_multiplier_1h,
  cacheReadCostMultiplier: record.cache_read_cost_multiplier,
  availability_enabled: record.availability_enabled !== false,
  availabilityEnabled: record.availability_enabled !== false,
  createdAt: record.created_at,
  updatedAt: record.updated_at,
  healthy: record.healthy,
  never_checked: record.never_checked === true,
  neverChecked: record.never_checked === true,
  lastCheck: record.last_check,
  responseTimeMs: record.response_time_ms,
  in_cooldown: record.in_cooldown,
  inCooldown: record.in_cooldown,
  cooldown_until: record.cooldown_until,
  cooldownUntil: record.cooldown_until,
  cooldown_reason: record.cooldown_reason,
  cooldownReason: record.cooldown_reason,
  cooldown_persist_pending: record.cooldown_persist_pending === true,
  cooldownPersistPending: record.cooldown_persist_pending === true
});

export const buildEndpointRecordPayload = (input = {}, fallbackName = '') => ({
  ...normalizeSoftFailurePolicyPayload(input),
  name: input.name || fallbackName,
  url: input.url || '',
  token: input.token || '',
  api_key: input.apiKey || '',
  clear_token: input.clearToken === true,
  clear_api_key: input.clearApiKey === true,
  headers: input.headers || {},
  priority: Number.isFinite(Number(input.priority)) ? Math.max(0, parseInt(input.priority, 10)) : 1,
  failover_enabled: input.failoverEnabled !== false,
  availability_enabled: input.availabilityEnabled !== false,
  cooldown_seconds: input.cooldownSeconds ? parseInt(input.cooldownSeconds) : null,
  timeout_seconds: parseInt(input.timeoutSeconds) || 300,
  supports_count_tokens: input.supportsCountTokens || false,
  model_rewrite_rules: String(input.modelRewriteRules || '').trim(),
  cost_multiplier: parseFloat(input.costMultiplier) || 1.0,
  input_cost_multiplier: parseFloat(input.inputCostMultiplier) || 1.0,
  output_cost_multiplier: parseFloat(input.outputCostMultiplier) || 1.0,
  cache_creation_cost_multiplier: parseFloat(input.cacheCreationCostMultiplier) || 1.0,
  cache_creation_cost_multiplier_1h: parseFloat(input.cacheCreationCostMultiplier1h) || 1.0,
  cache_read_cost_multiplier: parseFloat(input.cacheReadCostMultiplier) || 1.0
});

/**
 * 获取所有端点记录（SQLite 存储）
 * @returns {Promise<Array>} - 端点记录数组
 */
export const getEndpointRecords = async () => {
  await initWails();
  if (!WailsApp) throw new Error('Wails not available');

  const records = await WailsApp.GetEndpointRecords();
  return (records || []).map(mapEndpointRecord);
};

/**
 * 获取单个端点记录
 * @param {string} name - 端点名称
 * @returns {Promise<Object>} - 端点记录
 */
export const getEndpointRecord = async (name) => {
  await initWails();
  if (!WailsApp) throw new Error('Wails not available');

  const r = await WailsApp.GetEndpointRecord(name);
  return mapEndpointRecord(r);
};

/**
 * 创建新端点
 * @param {Object} input - 端点配置
 * @returns {Promise<void>}
 */
export const createEndpointRecord = async (input) => {
  await initWails();
  if (!WailsApp) throw new Error('Wails not available');

  const record = buildEndpointRecordPayload(input);

  await WailsApp.CreateEndpointRecord(record);
  return { success: true };
};

/**
 * 更新端点配置
 * @param {string} name - 端点名称
 * @param {Object} input - 更新的配置
 * @returns {Promise<void>}
 */
export const updateEndpointRecord = async (name, input) => {
  await initWails();
  if (!WailsApp) throw new Error('Wails not available');

  const record = buildEndpointRecordPayload(input, name);

  await WailsApp.UpdateEndpointRecord(name, record);
  return { success: true };
};

/**
 * 删除端点
 * @param {string} name - 端点名称
 * @returns {Promise<void>}
 */
export const deleteEndpointRecord = async (name) => {
  await initWails();
  if (!WailsApp) throw new Error('Wails not available');

  await WailsApp.DeleteEndpointRecord(name);
  return { success: true };
};

/**
 * 设置端点硬启用状态（v8：关闭后任何模式都不会使用）
 */
export const setEndpointAvailability = async (name, enabled) => {
  await initWails();
  if (!WailsApp) throw new Error('Wails not available');
  await WailsApp.SetEndpointAvailability(name, enabled);
  return { success: true };
};

/**
 * 手动解除端点冷却：推进故障 epoch、清内存冷却与软失败计数，
 * 并同步事务化写入持久化 tombstone。落库失败时置位 pending（前端保留重试入口）。
 * @param {string} name - 端点名称
 * @returns {Promise<void>}
 */
export const clearEndpointCooldown = async (name) => {
  await initWails();
  if (!WailsApp) throw new Error('Wails not available');
  await WailsApp.ClearEndpointCooldown(name);
  return { success: true };
};

/**
 * 设置端点是否参与自动调度（v8：关闭后仍可手动优选或固定使用）
 */
export const setEndpointAutoSchedule = async (name, enabled) => {
  await initWails();
  if (!WailsApp) throw new Error('Wails not available');
  await WailsApp.SetEndpointAutoSchedule(name, enabled);
  return { success: true };
};

// ============================================
// v5.0+ 模型定价管理 API (SQLite)
// ============================================

/**
 * 获取模型定价存储状态
 * @returns {Promise<Object>} - {enabled, totalCount, hasDefault}
 */
export const getModelPricingStorageStatus = async () => {
  await initWails();
  if (!WailsApp) throw new Error('Wails not available');

  const status = await WailsApp.GetModelPricingStorageStatus();
  return {
    enabled: status.enabled || false,
    totalCount: status.total_count || 0,
    hasDefault: status.has_default || false
  };
};

/**
 * 获取所有模型定价
 * @returns {Promise<Array>} - 模型定价数组
 */
export const getModelPricings = async () => {
  await initWails();
  if (!WailsApp) throw new Error('Wails not available');

  const records = await WailsApp.GetModelPricings();
  return (records || []).map(r => ({
    id: r.id,
    modelName: r.model_name,
    displayName: r.display_name,
    description: r.description,
    inputPrice: r.input_price,
    outputPrice: r.output_price,
    cacheCreationPrice5m: r.cache_creation_price_5m,
    cacheCreationPrice1h: r.cache_creation_price_1h,
    cacheReadPrice: r.cache_read_price,
    isDefault: r.is_default,
    createdAt: r.created_at,
    updatedAt: r.updated_at
  }));
};

/**
 * 获取单个模型定价
 * @param {string} modelName - 模型名称
 * @returns {Promise<Object>} - 模型定价记录
 */
export const getModelPricing = async (modelName) => {
  await initWails();
  if (!WailsApp) throw new Error('Wails not available');

  const r = await WailsApp.GetModelPricing(modelName);
  return {
    id: r.id,
    modelName: r.model_name,
    displayName: r.display_name,
    description: r.description,
    inputPrice: r.input_price,
    outputPrice: r.output_price,
    cacheCreationPrice5m: r.cache_creation_price_5m,
    cacheCreationPrice1h: r.cache_creation_price_1h,
    cacheReadPrice: r.cache_read_price,
    isDefault: r.is_default,
    createdAt: r.created_at,
    updatedAt: r.updated_at
  };
};

/**
 * 创建模型定价
 * @param {Object} input - 定价配置
 * @returns {Promise<Object>} - {success: true}
 */
export const createModelPricing = async (input) => {
  await initWails();
  if (!WailsApp) throw new Error('Wails not available');

  // 转换为后端期望的格式
  const record = {
    model_name: input.modelName || '',
    display_name: input.displayName || '',
    description: input.description || '',
    input_price: parseFloat(input.inputPrice) || 3.0,
    output_price: parseFloat(input.outputPrice) || 15.0,
    cache_creation_price_5m: parseFloat(input.cacheCreationPrice5m) || 0,
    cache_creation_price_1h: parseFloat(input.cacheCreationPrice1h) || 0,
    cache_read_price: parseFloat(input.cacheReadPrice) || 0,
    is_default: input.isDefault || false
  };

  await WailsApp.CreateModelPricing(record);
  return { success: true };
};

/**
 * 更新模型定价
 * @param {string} modelName - 模型名称
 * @param {Object} input - 更新的配置
 * @returns {Promise<Object>} - {success: true}
 */
export const updateModelPricing = async (modelName, input) => {
  await initWails();
  if (!WailsApp) throw new Error('Wails not available');

  // 转换为后端期望的格式
  const record = {
    model_name: input.modelName || modelName,
    display_name: input.displayName || '',
    description: input.description || '',
    input_price: parseFloat(input.inputPrice) || 3.0,
    output_price: parseFloat(input.outputPrice) || 15.0,
    cache_creation_price_5m: parseFloat(input.cacheCreationPrice5m) || 0,
    cache_creation_price_1h: parseFloat(input.cacheCreationPrice1h) || 0,
    cache_read_price: parseFloat(input.cacheReadPrice) || 0,
    is_default: input.isDefault || false
  };

  await WailsApp.UpdateModelPricing(modelName, record);
  return { success: true };
};

/**
 * 删除模型定价
 * @param {string} modelName - 模型名称
 * @returns {Promise<Object>} - {success: true}
 */
export const deleteModelPricing = async (modelName) => {
  await initWails();
  if (!WailsApp) throw new Error('Wails not available');

  await WailsApp.DeleteModelPricing(modelName);
  return { success: true };
};

/**
 * 设置默认模型定价
 * @param {string} modelName - 模型名称
 * @returns {Promise<Object>} - {success: true}
 */
export const setDefaultModelPricing = async (modelName) => {
  await initWails();
  if (!WailsApp) throw new Error('Wails not available');

  await WailsApp.SetDefaultModelPricing(modelName);
  return { success: true };
};

// ============================================
// v5.1+ 系统设置管理 API (SQLite)
// ============================================

/**
 * 获取设置存储状态
 * @returns {Promise<Object>} - {enabled, totalCount, categoryCount, isInitialized}
 */
export const getSettingsStorageStatus = async () => {
  await initWails();
  if (!WailsApp) throw new Error('Wails not available');

  const status = await WailsApp.GetSettingsStorageStatus();
  return {
    enabled: status.enabled || false,
    totalCount: status.total_count || 0,
    categoryCount: status.category_count || 0,
    isInitialized: status.is_initialized || false
  };
};

/**
 * 获取所有设置分类
 * @returns {Promise<Array>} - 分类信息数组
 */
export const getSettingCategories = async () => {
  await initWails();
  if (!WailsApp) throw new Error('Wails not available');

  const categories = await WailsApp.GetSettingCategories();
  return (categories || []).map(c => ({
    name: c.name,
    label: c.label,
    description: c.description,
    icon: c.icon,
    order: c.order
  }));
};

/**
 * 获取所有设置
 * @returns {Promise<Array>} - 设置数组
 */
export const getAllSettings = async () => {
  await initWails();
  if (!WailsApp) throw new Error('Wails not available');

  const settings = await WailsApp.GetAllSettings();
  return (settings || []).map(s => ({
    id: s.id,
    category: s.category,
    key: s.key,
    value: s.value,
    value_type: s.value_type,
    label: s.label,
    description: s.description,
    display_order: s.display_order,
    requires_restart: s.requires_restart,
    secret_configured: s.secret_configured || false,
    created_at: s.created_at,
    updated_at: s.updated_at
  }));
};

/**
 * 获取指定分类的设置
 * @param {string} category - 分类名称
 * @returns {Promise<Array>} - 设置数组
 */
export const getSettingsByCategory = async (category) => {
  await initWails();
  if (!WailsApp) throw new Error('Wails not available');

  const settings = await WailsApp.GetSettingsByCategory(category);
  return (settings || []).map(s => ({
    id: s.id,
    category: s.category,
    key: s.key,
    value: s.value,
    value_type: s.value_type,
    label: s.label,
    description: s.description,
    display_order: s.display_order,
    requires_restart: s.requires_restart,
    secret_configured: s.secret_configured || false,
    created_at: s.created_at,
    updated_at: s.updated_at
  }));
};

/**
 * 获取单个设置
 * @param {string} category - 分类名称
 * @param {string} key - 设置键
 * @returns {Promise<Object>} - 设置记录
 */
export const getSetting = async (category, key) => {
  await initWails();
  if (!WailsApp) throw new Error('Wails not available');

  const s = await WailsApp.GetSetting(category, key);
  return {
    id: s.id,
    category: s.category,
    key: s.key,
    value: s.value,
    value_type: s.value_type,
    label: s.label,
    description: s.description,
    display_order: s.display_order,
    requires_restart: s.requires_restart,
    secret_configured: s.secret_configured || false,
    created_at: s.created_at,
    updated_at: s.updated_at
  };
};

/**
 * 更新单个设置
 * @param {string} category - 分类名称
 * @param {string} key - 设置键
 * @param {string} value - 新值
 * @returns {Promise<Object>} - {success: true}
 */
export const updateSetting = async (category, key, value) => {
  await initWails();
  if (!WailsApp) throw new Error('Wails not available');

  await WailsApp.UpdateSetting({
    category,
    key,
    value
  });
  return { success: true };
};

/**
 * 批量更新设置
 * @param {Object|Array} input - 设置对象 {settings: [...]} 或设置数组 [{category, key, value}]
 * @returns {Promise<Object>} - {success: true}
 */
export const batchUpdateSettings = async (input) => {
  await initWails();
  if (!WailsApp) throw new Error('Wails not available');

  // 兼容两种调用方式：对象 {settings: [...]} 或数组 [...]
  const settingsArray = Array.isArray(input) ? input : (input.settings || []);

  await WailsApp.BatchUpdateSettings({
    settings: settingsArray.map(s => ({
      category: s.category,
      key: s.key,
      value: s.value
    }))
  });
  return { success: true };
};

/**
 * 重置分类设置为默认值
 * @param {string} category - 分类名称
 * @returns {Promise<Object>} - {success: true}
 */
export const resetCategorySettings = async (category) => {
  await initWails();
  if (!WailsApp) throw new Error('Wails not available');

  await WailsApp.ResetCategorySettings(category);
  return { success: true };
};

const normalizeCodexModel = (model = {}) => ({
  id: model.id || model.ID || '',
  object: model.object || model.Object || 'model',
  owned_by: model.owned_by || model.ownedBy || model.OwnedBy || 'openai',
  source: model.source || model.Source || 'custom',
  enabled: model.enabled !== undefined ? Boolean(model.enabled) : Boolean(model.Enabled),
  deprecated: model.deprecated !== undefined ? Boolean(model.deprecated) : Boolean(model.Deprecated),
  display_name: model.display_name || model.displayName || model.DisplayName || model.id || model.ID || '',
  description: model.description || model.Description || '',
  capabilities_json: model.capabilities_json ?? model.CapabilitiesJSON ?? '',
  // 只读：后端对已保存能力的校验结果，保存时会被忽略
  capabilities_error: model.capabilities_error ?? model.CapabilitiesError ?? ''
});

const normalizeCodexModelCatalog = (catalog = {}) => {
  const models = Array.isArray(catalog.models || catalog.Models)
    ? (catalog.models || catalog.Models).map(normalizeCodexModel)
    : [];
  return {
    enabled: catalog.enabled !== undefined ? Boolean(catalog.enabled) : Boolean(catalog.Enabled),
    mode: catalog.mode || catalog.Mode || 'local',
    models,
    effective_count: catalog.effective_count ?? catalog.effectiveCount ?? catalog.EffectiveCount ?? models.filter(model => model.enabled).length
  };
};

export const getCodexModelCatalog = async () => {
  await initWails();
  const method = getWailsMethod('GetCodexModelCatalog');
  return normalizeCodexModelCatalog(await method());
};

export const saveCodexModelCatalog = async (catalog) => {
  await initWails();
  const method = getWailsMethod('SaveCodexModelCatalog');
  return normalizeCodexModelCatalog(await method({
    enabled: catalog?.enabled === true,
    mode: catalog?.mode || 'local',
    models: (catalog?.models || []).map(normalizeCodexModel)
  }));
};

export const mergeDefaultCodexModelCatalog = async () => {
  await initWails();
  const method = getWailsMethod('MergeDefaultCodexModelCatalog');
  return normalizeCodexModelCatalog(await method());
};

export const resetCodexModelCatalog = async () => {
  await initWails();
  const method = getWailsMethod('ResetCodexModelCatalog');
  return normalizeCodexModelCatalog(await method());
};

/**
 * 获取端口信息
 * @returns {Promise<Object>} - {preferred_port, actual_port, is_default, was_occupied}
 */
export const getPortInfo = async () => {
  await initWails();
  if (!WailsApp) throw new Error('Wails not available');

  const info = await WailsApp.GetPortInfo();
  return {
    preferred_port: info.preferred_port,
    actual_port: info.actual_port,
    is_default: info.is_default,
    was_occupied: info.was_occupied
  };
};

/**
 * 更新首选端口（需要重启生效）
 * @param {number} port - 新端口号
 * @returns {Promise<Object>} - {success: true}
 */
export const updatePreferredPort = async (port) => {
  await initWails();
  if (!WailsApp) throw new Error('Wails not available');

  await WailsApp.UpdatePreferredPort(port);
  return { success: true };
};

/**
 * 检查端口是否可用
 * @param {number} port - 端口号
 * @returns {Promise<boolean>} - 是否可用
 */
export const checkPortAvailable = async (port) => {
  await initWails();
  if (!WailsApp) throw new Error('Wails not available');

  return await WailsApp.CheckPortAvailable(port);
};

// ============================================
// 隐私保护 (v6.1+)
// ============================================

const PRIVACY_SCOPE_KEYS = ['paths', 'upstream_types', 'endpoint_names', 'account_ids', 'provider_types'];

// 解析 scope JSON 为带默认空数组的对象（容错：非法 JSON 返回全空）
export const parsePrivacyScope = (scopeJson) => {
  const scope = {};
  for (const key of PRIVACY_SCOPE_KEYS) {
    scope[key] = [];
  }
  if (!scopeJson || typeof scopeJson !== 'string') {
    return scope;
  }
  try {
    const parsed = JSON.parse(scopeJson);
    if (parsed && typeof parsed === 'object') {
      for (const key of PRIVACY_SCOPE_KEYS) {
        if (Array.isArray(parsed[key])) {
          scope[key] = parsed[key];
        }
      }
    }
  } catch {
    // 非法 scope JSON 按不限处理，由后端在保存时校验
  }
  return scope;
};

// 将 UI scope 对象编码为 scope_json（空维度省略，保持后端语义“空=不限”）
export const encodePrivacyScope = (scope = {}) => {
  const payload = {};
  for (const key of PRIVACY_SCOPE_KEYS) {
    const values = scope[key];
    if (Array.isArray(values) && values.length > 0) {
      payload[key] = key === 'account_ids'
        ? values.map((v) => Number(v)).filter((v) => Number.isFinite(v))
        : values.map((v) => String(v).trim()).filter(Boolean);
    }
  }
  return JSON.stringify(payload);
};

export const normalizePrivacySettings = (settings = {}) => ({
  mode: settings.mode ?? settings.Mode ?? 'disabled',
  scan_max_bytes: Number(settings.scan_max_bytes ?? settings.ScanMaxBytes ?? 4194304),
  over_limit_action: settings.over_limit_action ?? settings.OverLimitAction ?? 'scan_prefix',
  on_error: settings.on_error ?? settings.OnError ?? 'fail_open',
  version: Number(settings.version ?? settings.Version ?? 0),
  status: settings.status ?? settings.Status ?? 'ok',
  compile_error: settings.compile_error ?? settings.CompileError ?? '',
  enabled_rules: Number(settings.enabled_rules ?? settings.EnabledRules ?? 0),
  updated_at: settings.updated_at ?? settings.UpdatedAt ?? ''
});

export const normalizePrivacyRule = (rule = {}) => {
  const scopeJson = rule.scope_json ?? rule.ScopeJSON ?? '{}';
  return {
    id: Number(rule.id ?? rule.ID ?? 0),
    enabled: Boolean(rule.enabled ?? rule.Enabled ?? false),
    name: rule.name ?? rule.Name ?? '',
    description: rule.description ?? rule.Description ?? '',
    priority: Number(rule.priority ?? rule.Priority ?? 100),
    match_type: rule.match_type ?? rule.MatchType ?? 'regex',
    pattern: rule.pattern ?? rule.Pattern ?? '',
    placeholder: rule.placeholder ?? rule.Placeholder ?? '',
    action: rule.action ?? rule.Action ?? 'redact',
    scope_json: scopeJson,
    scope: parsePrivacyScope(scopeJson),
    source: rule.source ?? rule.Source ?? 'custom',
    compile_error: rule.compile_error ?? rule.CompileError ?? '',
    created_at: rule.created_at ?? rule.CreatedAt ?? '',
    updated_at: rule.updated_at ?? rule.UpdatedAt ?? ''
  };
};

export const normalizePrivacyExactSecret = (secret = {}) => ({
  id: Number(secret.id ?? secret.ID ?? 0),
  enabled: Boolean(secret.enabled ?? secret.Enabled ?? false),
  name: secret.name ?? secret.Name ?? '',
  category: secret.category ?? secret.Category ?? 'custom',
  placeholder: secret.placeholder ?? secret.Placeholder ?? '',
  source_type: secret.source_type ?? secret.SourceType ?? 'manual',
  source_ref: secret.source_ref ?? secret.SourceRef ?? '',
  description: secret.description ?? secret.Description ?? '',
  masked_value: secret.masked_value ?? secret.MaskedValue ?? '',
  value_length: Number(secret.value_length ?? secret.ValueLength ?? 0),
  value_hash_short: secret.value_hash_short ?? secret.ValueHashShort ?? '',
  created_at: secret.created_at ?? secret.CreatedAt ?? '',
  updated_at: secret.updated_at ?? secret.UpdatedAt ?? ''
});

export const normalizePrivacyImportCandidate = (candidate = {}) => ({
  source_type: candidate.source_type ?? candidate.SourceType ?? '',
  source_ref: candidate.source_ref ?? candidate.SourceRef ?? '',
  name: candidate.name ?? candidate.Name ?? '',
  category: candidate.category ?? candidate.Category ?? 'custom',
  masked_value: candidate.masked_value ?? candidate.MaskedValue ?? '',
  value_length: Number(candidate.value_length ?? candidate.ValueLength ?? 0),
  value_hash_short: candidate.value_hash_short ?? candidate.ValueHashShort ?? '',
  already_exists: Boolean(candidate.already_exists ?? candidate.AlreadyExists ?? false)
});

// 将 UI 规则对象转换为后端 PrivacyRuleInput
export const buildPrivacyRulePayload = (input = {}) => ({
  enabled: Boolean(input.enabled),
  name: String(input.name ?? '').trim(),
  description: String(input.description ?? '').trim(),
  priority: Number.isFinite(Number(input.priority)) ? Number(input.priority) : 100,
  match_type: String(input.match_type ?? input.matchType ?? 'regex').toLowerCase(),
  pattern: String(input.pattern ?? ''),
  placeholder: String(input.placeholder ?? ''),
  action: String(input.action ?? 'redact').toLowerCase(),
  scope_json: typeof input.scope_json === 'string' && input.scope_json
    ? input.scope_json
    : encodePrivacyScope(input.scope)
});

export const buildPrivacyExactSecretPayload = (input = {}) => ({
  enabled: input.enabled !== false,
  name: String(input.name ?? '').trim(),
  secret_value: String(input.secret_value ?? input.secretValue ?? ''),
  category: String(input.category ?? 'custom').trim(),
  placeholder: String(input.placeholder ?? ''),
  source_type: String(input.source_type ?? input.sourceType ?? 'manual').trim(),
  source_ref: String(input.source_ref ?? input.sourceRef ?? '').trim(),
  description: String(input.description ?? '').trim()
});

export const getPrivacySettings = async () => {
  await initWails();
  if (!WailsApp) throw new Error('Wails not available');

  const method = getWailsMethod('GetPrivacySettings');
  return normalizePrivacySettings(await method());
};

export const updatePrivacySettings = async (input = {}) => {
  await initWails();
  if (!WailsApp) throw new Error('Wails not available');

  const method = getWailsMethod('UpdatePrivacySettings');
  const result = await method({
    mode: String(input.mode ?? 'disabled'),
    scan_max_bytes: Number(input.scan_max_bytes ?? input.scanMaxBytes ?? 4194304),
    over_limit_action: String(input.over_limit_action ?? input.overLimitAction ?? 'scan_prefix'),
    on_error: String(input.on_error ?? input.onError ?? 'fail_open')
  });
  return normalizePrivacySettings(result);
};

export const getPrivacyRules = async () => {
  await initWails();
  if (!WailsApp) throw new Error('Wails not available');

  const method = getWailsMethod('ListPrivacyRules');
  const result = await method();
  return (Array.isArray(result) ? result : []).map(normalizePrivacyRule);
};

export const listPrivacyExactSecrets = async () => {
  await initWails();
  if (!WailsApp) throw new Error('Wails not available');

  const method = getWailsMethod('ListPrivacyExactSecrets');
  const result = await method();
  return (Array.isArray(result) ? result : []).map(normalizePrivacyExactSecret);
};

export const createPrivacyExactSecret = async (input) => {
  await initWails();
  if (!WailsApp) throw new Error('Wails not available');

  const method = getWailsMethod('CreatePrivacyExactSecret');
  return normalizePrivacyExactSecret(await method(buildPrivacyExactSecretPayload(input)));
};

export const updatePrivacyExactSecret = async (id, input) => {
  await initWails();
  if (!WailsApp) throw new Error('Wails not available');

  const method = getWailsMethod('UpdatePrivacyExactSecret');
  return normalizePrivacyExactSecret(await method(normalizeEntityId(id), buildPrivacyExactSecretPayload(input)));
};

export const deletePrivacyExactSecret = async (id) => {
  await initWails();
  if (!WailsApp) throw new Error('Wails not available');

  const method = getWailsMethod('DeletePrivacyExactSecret');
  await method(normalizeEntityId(id));
  return { success: true };
};

export const clearPrivacyExactSecrets = async (confirmText) => {
  await initWails();
  if (!WailsApp) throw new Error('Wails not available');

  const method = getWailsMethod('ClearPrivacyExactSecrets');
  await method(String(confirmText ?? ''));
  return { success: true };
};

export const listPrivacySecretImportCandidates = async () => {
  await initWails();
  if (!WailsApp) throw new Error('Wails not available');

  const method = getWailsMethod('ListPrivacySecretImportCandidates');
  const result = await method();
  return (Array.isArray(result) ? result : []).map(normalizePrivacyImportCandidate);
};

export const importPrivacySecretCandidate = async (input = {}) => {
  await initWails();
  if (!WailsApp) throw new Error('Wails not available');

  const method = getWailsMethod('ImportPrivacySecretCandidate');
  const result = await method({
    source_type: String(input.source_type ?? '').trim(),
    source_ref: String(input.source_ref ?? '').trim(),
    name: String(input.name ?? '').trim(),
    category: String(input.category ?? '').trim(),
    placeholder: String(input.placeholder ?? ''),
    description: String(input.description ?? '').trim(),
    secret_value: String(input.secret_value ?? '')
  });
  return normalizePrivacyExactSecret(result);
};

export const createPrivacyRule = async (input) => {
  await initWails();
  if (!WailsApp) throw new Error('Wails not available');

  const method = getWailsMethod('CreatePrivacyRule');
  return normalizePrivacyRule(await method(buildPrivacyRulePayload(input)));
};

export const updatePrivacyRule = async (id, input) => {
  await initWails();
  if (!WailsApp) throw new Error('Wails not available');

  const method = getWailsMethod('UpdatePrivacyRule');
  return normalizePrivacyRule(await method(normalizeEntityId(id), buildPrivacyRulePayload(input)));
};

export const deletePrivacyRule = async (id) => {
  await initWails();
  if (!WailsApp) throw new Error('Wails not available');

  const method = getWailsMethod('DeletePrivacyRule');
  await method(normalizeEntityId(id));
  return { success: true };
};

export const reorderPrivacyRules = async (orders = []) => {
  await initWails();
  if (!WailsApp) throw new Error('Wails not available');

  const method = getWailsMethod('ReorderPrivacyRules');
  await method(orders.map((item) => ({
    id: Number(item.id),
    priority: Number(item.priority)
  })));
  return { success: true };
};

// 测试面板：文本只在内存中处理，不写 localStorage、不落日志
export const testPrivacyRules = async (input = {}) => {
  await initWails();
  if (!WailsApp) throw new Error('Wails not available');

  const method = getWailsMethod('TestPrivacyRules');
  const result = await method({
    text: String(input.text ?? ''),
    path: String(input.path ?? ''),
    upstream_type: String(input.upstream_type ?? input.upstreamType ?? ''),
    endpoint_name: String(input.endpoint_name ?? input.endpointName ?? ''),
    account_id: Number(input.account_id ?? input.accountId ?? 0),
    provider_type: String(input.provider_type ?? input.providerType ?? '')
  });
  return {
    original_length: Number(result?.original_length ?? 0),
    hit_count: Number(result?.hit_count ?? 0),
    changed: Boolean(result?.changed),
    replaced_text: result?.replaced_text ?? '',
    rule_hits: Array.isArray(result?.rule_hits) ? result.rule_hits : [],
    scan_duration_ms: Number(result?.scan_duration_ms ?? 0)
  };
};

export const listPrivacyPresets = async () => {
  await initWails();
  if (!WailsApp) throw new Error('Wails not available');

  const method = getWailsMethod('ListPrivacyPresets');
  const result = await method();
  return Array.isArray(result) ? result : [];
};

export const importPrivacyPreset = async (presetId) => {
  await initWails();
  if (!WailsApp) throw new Error('Wails not available');

  const method = getWailsMethod('ImportPrivacyPreset');
  const result = await method(String(presetId));
  return (Array.isArray(result) ? result : []).map(normalizePrivacyRule);
};

export const exportPrivacyRules = async () => {
  await initWails();
  if (!WailsApp) throw new Error('Wails not available');

  const method = getWailsMethod('ExportPrivacyRules');
  return await method();
};

export const getPrivacyRuntimeStats = async () => {
  await initWails();
  if (!WailsApp) throw new Error('Wails not available');

  const method = getWailsMethod('GetPrivacyRuntimeStats');
  const result = await method();
  return {
    scan_count: Number(result?.scan_count ?? 0),
    hit_count: Number(result?.hit_count ?? 0),
    blocked_count: Number(result?.blocked_count ?? 0),
    truncated_count: Number(result?.truncated_count ?? 0),
    rule_stats: Array.isArray(result?.rule_stats) ? result.rule_stats : []
  };
};

// ============================================
// Codex 订阅代理专用配置（专用设置页独占，不走通用设置保存/重置）
//
// 访问密钥明文只在初始化与轮换的返回结果里出现一次，其余读取只返回是否已配置；
// 这里只做字段归一化，不做任何本地持久化。
// ============================================

const normalizeCodexSubscriptionProxyConfig = (config = {}) => ({
  initialized: config.initialized === true,
  enabled: config.enabled === true,
  mode: config.mode || 'subscription',
  listenHost: config.listen_host || '127.0.0.1',
  listenPort: Number(config.listen_port ?? 28088),
  wsCompression: config.ws_compression || 'auto',
  revision: Number(config.revision ?? 0),
  accessConfigured: config.access_configured === true
});

const normalizeCodexSubscriptionProxySaveResult = (result = {}) => ({
  config: normalizeCodexSubscriptionProxyConfig(result.config),
  accessSecret: result.access_secret || '',
  accessUrl: result.access_url || ''
});

export const normalizeCodexSubscriptionProxyStatus = (status = {}) => ({
  state: status.state || 'stopped',
  enabled: status.enabled === true,
  mode: status.mode || '',
  revision: Number(status.revision ?? 0),
  listenAddr: status.listen_addr || '',
  activeRequests: Number(status.active_requests ?? 0),
  activeWebSockets: Number(status.active_websockets ?? 0),
  lastError: status.last_error || '',
  accessConfigured: status.access_configured === true
});

export const getCodexSubscriptionProxyConfig = async () => {
  await initWails();
  if (!WailsApp) throw new Error('Wails not available');

  return normalizeCodexSubscriptionProxyConfig(await WailsApp.GetCodexSubscriptionProxyConfig());
};

export const saveCodexSubscriptionProxyConfig = async (input = {}, expectedRevision = 0) => {
  await initWails();
  if (!WailsApp) throw new Error('Wails not available');

  const result = await WailsApp.SaveCodexSubscriptionProxyConfig({
    enabled: input.enabled === true,
    mode: String(input.mode ?? 'subscription'),
    listen_host: String(input.listen_host ?? input.listenHost ?? '127.0.0.1'),
    listen_port: Number(input.listen_port ?? input.listenPort ?? 28088),
    ws_compression: String(input.ws_compression ?? input.wsCompression ?? 'auto')
  }, Number(expectedRevision ?? 0));
  return normalizeCodexSubscriptionProxySaveResult(result);
};

export const setCodexSubscriptionProxyEnabled = async (enabled, expectedRevision = 0) => {
  await initWails();
  if (!WailsApp) throw new Error('Wails not available');

  const result = await WailsApp.SetCodexSubscriptionProxyEnabled(enabled === true, Number(expectedRevision ?? 0));
  return normalizeCodexSubscriptionProxySaveResult(result);
};

export const rotateCodexSubscriptionProxyAccess = async (expectedRevision = 0) => {
  await initWails();
  if (!WailsApp) throw new Error('Wails not available');

  return normalizeCodexSubscriptionProxySaveResult(
    await WailsApp.RotateCodexSubscriptionProxyAccess(Number(expectedRevision ?? 0))
  );
};

// 入口状态的前端形状；claude-code-entry:update 事件推送的是后端原始字段，也用它归一
export const normalizeClaudeCodeEntryInfo = (info = {}) => ({
  enabled: info.enabled === true,
  listenPort: Number(info.listen_port) || 0,
  mode: info.mode || 'subscription',
  baseUrl: info.base_url || '',
  running: info.running === true,
  error: info.error || ''
});

/**
 * 获取 Claude Code 专用入口的配置与运行态
 * @returns {Promise<{enabled:boolean, listenPort:number, mode:string, baseUrl:string, running:boolean, error:string}>}
 */
export const getClaudeCodeEntryStatus = async () => {
  await initWails();
  if (!WailsApp) throw new Error('Wails not available');

  return normalizeClaudeCodeEntryInfo(await WailsApp.GetClaudeCodeEntryStatus());
};

/**
 * 保存 Claude Code 专用入口配置并立即应用；监听失败时由返回值的 error 字段说明原因
 * @param {{enabled:boolean, listenPort:number, mode:string}} input
 */
export const saveClaudeCodeEntrySettings = async (input = {}) => {
  await initWails();
  if (!WailsApp) throw new Error('Wails not available');

  return normalizeClaudeCodeEntryInfo(await WailsApp.SaveClaudeCodeEntrySettings({
    enabled: input.enabled === true,
    listen_port: Number(input.listenPort),
    mode: String(input.mode || 'subscription')
  }));
};

/**
 * 只切换 Claude Code 入口的接入模式（subscription / endpoint），启用状态与端口沿用已保存配置
 * @param {string} mode
 */
export const setClaudeCodeEntryMode = async (mode) => {
  await initWails();
  if (!WailsApp) throw new Error('Wails not available');

  return normalizeClaudeCodeEntryInfo(await WailsApp.SetClaudeCodeEntryMode(String(mode || '')));
};

/**
 * 获取订阅直通额度状态（后端原始字段，与 claude-code-entry:rate-limit 事件同形）；代理未就绪时为 null
 */
export const getClaudeSubscriptionRateLimit = async () => {
  await initWails();
  if (!WailsApp) throw new Error('Wails not available');

  return WailsApp.GetClaudeSubscriptionRateLimit();
};

/**
 * 手动查询官方订阅额度；冷却中、正在查询或没有登录凭据时后端拒绝，查询失败写在 last_error
 */
export const refreshClaudeSubscriptionRateLimit = async () => {
  await initWails();
  if (!WailsApp) throw new Error('Wails not available');

  return WailsApp.RefreshClaudeSubscriptionRateLimit();
};

export const getCodexSubscriptionProxyStatus = async () => {
  await initWails();
  if (!WailsApp) throw new Error('Wails not available');

  return normalizeCodexSubscriptionProxyStatus(await WailsApp.GetCodexSubscriptionProxyStatus());
};
