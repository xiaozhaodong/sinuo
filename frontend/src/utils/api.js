// ============================================
// API 服务层
// 2025-11-28 (Updated 2025-12-04 for Wails support)
// ============================================

import { API_ENDPOINTS, ERROR_MESSAGES } from './constants.js';
import * as WailsApi from './wailsApi.js';
import { normalizeRequest } from '@pages/requests/utils/normalizeRequest.js';
import { formatTimestamp as formatConfiguredTimestamp } from './timezone.js';

// 检测是否在 Wails 环境中运行
export const isWailsEnvironment = WailsApi.isWailsEnvironment;

// 请求超时配置
const DEFAULT_TIMEOUT = 30000;

// 带超时的 fetch 包装器
const fetchWithTimeout = async (url, options = {}, timeout = DEFAULT_TIMEOUT) => {
  const controller = new AbortController();
  const timeoutId = setTimeout(() => controller.abort(), timeout);

  try {
    const response = await fetch(url, {
      ...options,
      signal: controller.signal,
      headers: {
        'Content-Type': 'application/json',
        ...options.headers
      }
    });

    clearTimeout(timeoutId);

    if (!response.ok) {
      let errorMessage = ERROR_MESSAGES.SERVER_ERROR;
      try {
        const errorData = await response.json();
        errorMessage = errorData.message || errorData.error || errorMessage;
      } catch {
        errorMessage = `HTTP ${response.status}: ${response.statusText}`;
      }
      throw new Error(errorMessage);
    }

    const contentType = response.headers.get('content-type');
    if (contentType && contentType.includes('application/json')) {
      return await response.json();
    }
    return await response.text();
  } catch (error) {
    clearTimeout(timeoutId);
    if (error.name === 'AbortError') {
      throw new Error(ERROR_MESSAGES.REQUEST_TIMEOUT);
    }
    if (error instanceof TypeError && error.message.includes('fetch')) {
      throw new Error(ERROR_MESSAGES.NETWORK_ERROR);
    }
    throw error;
  }
};

// ============================================
// 系统状态 API
// ============================================

export const fetchStatus = async () => {
  // Wails 环境使用绑定
  if (isWailsEnvironment()) {
    return await WailsApi.getSystemStatus();
  }
  return await fetchWithTimeout(API_ENDPOINTS.STATUS);
};

export const fetchConnections = async () => {
  // Wails 环境使用绑定
  if (isWailsEnvironment()) {
    return await WailsApi.getUsageSummary();
  }
  return await fetchWithTimeout(API_ENDPOINTS.CONNECTIONS);
};

// ============================================
// 端点管理 API
// ============================================

export const fetchEndpoints = async () => {
  // Wails 环境使用绑定
  if (isWailsEnvironment()) {
    return await WailsApi.getEndpoints();
  }

  const data = await fetchWithTimeout(API_ENDPOINTS.ENDPOINTS);
  // 返回完整结构，包含 total, healthy 等统计信息
  // API 返回格式: { endpoints: [...], total: N, healthy: N }
  if (data.endpoints) {
    return {
      endpoints: data.endpoints,
      total: data.total ?? data.endpoints.length,
      healthy: data.healthy ?? data.endpoints.filter(e => e.status === 'healthy').length
    };
  }
  // 如果 API 直接返回数组
  const endpoints = Array.isArray(data) ? data : [];
  return {
    endpoints,
    total: endpoints.length,
    healthy: endpoints.filter(e => e.status === 'healthy').length
  };
};

export const checkEndpointHealth = async (endpointName) => {
  // Wails 环境使用绑定
  if (isWailsEnvironment()) {
    return await WailsApi.triggerHealthCheck(endpointName);
  }
  return await fetchWithTimeout(`${API_ENDPOINTS.ENDPOINT_HEALTH}/${endpointName}`, {
    method: 'POST'
  });
};

export const checkAllEndpointsHealth = async () => {
  // Wails 环境：使用后端批量健康检查 API
  if (isWailsEnvironment()) {
    const result = await WailsApi.batchHealthCheckAll();
    return {
      success: result.success,
      message: result.message || '批量连通性测试完成',
      total: result.total,
      healthy_count: result.healthy_count,
      unhealthy_count: result.unhealthy_count
    };
  }
  return await fetchWithTimeout(API_ENDPOINTS.ENDPOINT_HEALTH, {
    method: 'POST'
  });
};

/**
 * 更新端点优先级
 * @param {string} endpointName - 端点名称
 * @param {number} priority - 新优先级
 */
export const updateEndpointPriority = async (endpointName, priority) => {
  if (!endpointName) throw new Error('端点名称不能为空');
  if (priority < 1) throw new Error('优先级必须大于等于1');

  // Wails 环境使用绑定
  if (isWailsEnvironment()) {
    await WailsApi.setEndpointPriority(endpointName, parseInt(priority));
    return { success: true, message: `端点 ${endpointName} 优先级已更新为 ${priority}` };
  }

  const data = await fetchWithTimeout(`/api/v1/endpoints/${encodeURIComponent(endpointName)}/priority`, {
    method: 'POST',
    body: JSON.stringify({ priority: parseInt(priority) })
  });

  if (!data.success) {
    throw new Error(data.error || '优先级更新失败');
  }

  return data;
};

export const fetchClaudeRoutingState = async () => {
  if (isWailsEnvironment()) {
    return await WailsApi.getClaudeRoutingState();
  }
  return {
    unsupported: true,
    mode: 'auto',
    endpoint_name: '',
    endpointName: '',
    fallback_enabled: true,
    fallbackEnabled: true
  };
};

export const setClaudeRoutingOverride = async (input) => {
  if (isWailsEnvironment()) {
    return await WailsApi.setClaudeRoutingOverride(input);
  }
  return {
    unsupported: true,
    message: '当前运行环境暂不支持 Claude 路由切换'
  };
};

export const clearClaudeRoutingOverride = async () => {
  if (isWailsEnvironment()) {
    return await WailsApi.clearClaudeRoutingOverride();
  }
  return {
    unsupported: true,
    message: '当前运行环境暂不支持 Claude 路由切换'
  };
};

export const clearNegativeHitCache = async (endpointName = '') => {
  if (isWailsEnvironment()) {
    return await WailsApi.clearNegativeHitCache(endpointName);
  }
  return {
    unsupported: true,
    message: '当前运行环境暂不支持清理路由缓存'
  };
};

export const fetchModelRoutingSettings = async () => {
  if (isWailsEnvironment()) return await WailsApi.getModelRoutingSettings();
  return {
    unsupported: true,
    claude_enabled: false,
    codex_enabled: false,
    status: 'unsupported',
    version: 0,
    binding_count: 0,
    claude_status: 'unsupported',
    codex_status: 'unsupported',
    claude_version: 0,
    codex_version: 0,
    claude_binding_count: 0,
    codex_binding_count: 0
  };
};

export const updateModelRoutingSettings = async (input) => {
  if (isWailsEnvironment()) return await WailsApi.updateModelRoutingSettings(input);
  return { unsupported: true, message: '当前运行环境暂不支持模型路由配置' };
};

export const fetchModelRouteBindings = async () => {
  if (isWailsEnvironment()) return await WailsApi.listModelRouteBindings();
  return [];
};

export const createModelRouteBinding = async (input) => {
  if (isWailsEnvironment()) return await WailsApi.createModelRouteBinding(input);
  throw new Error('当前运行环境暂不支持模型路由配置');
};

export const updateModelRouteBinding = async (assignmentId, input) => {
  if (isWailsEnvironment()) return await WailsApi.updateModelRouteBinding(assignmentId, input);
  throw new Error('当前运行环境暂不支持模型路由配置');
};

export const deleteModelRouteBinding = async (assignmentId) => {
  if (isWailsEnvironment()) return await WailsApi.deleteModelRouteBinding(assignmentId);
  throw new Error('当前运行环境暂不支持模型路由配置');
};

export const testModelRouting = async (model) => {
  if (isWailsEnvironment()) return await WailsApi.testModelRouting(model);
  return { unsupported: true, model, assigned: false, scope_kind: 'global', members: [] };
};

export const testCodexModelRouting = async (model) => {
  if (isWailsEnvironment()) return await WailsApi.testCodexModelRouting(model);
  return { unsupported: true, model, assigned: false, scope_kind: 'global', accounts: [] };
};

export const fetchCodexModelRouteBindings = async () => {
  if (isWailsEnvironment()) return await WailsApi.listCodexModelRouteBindings();
  return [];
};

export const createCodexModelRouteBinding = async (input) => {
  if (isWailsEnvironment()) return await WailsApi.createCodexModelRouteBinding(input);
  throw new Error('当前运行环境暂不支持 Codex 模型路由配置');
};

export const deleteCodexModelRouteBinding = async (id) => {
  if (isWailsEnvironment()) return await WailsApi.deleteCodexModelRouteBinding(id);
  throw new Error('当前运行环境暂不支持 Codex 模型路由配置');
};

export const updateCodexModelRouteBinding = async (id, input) => {
  if (isWailsEnvironment()) return await WailsApi.updateCodexModelRouteBinding(id, input);
  throw new Error('当前运行环境暂不支持 Codex 模型路由配置');
};

// ============================================
// 使用统计 API
// ============================================

export const fetchUsageStats = async (params = {}) => {
  const queryInput = {
    ...params,
    source_view: params.source_view || params.sourceView || 'all'
  };

  // Wails 环境使用新的 GetUsageStats 绑定（与 HTTP API 格式一致）
  // 传递完整筛选参数
  if (isWailsEnvironment()) {
    return await WailsApi.getUsageStats(queryInput);
  }

  const queryParams = new URLSearchParams();
  Object.entries(queryInput).forEach(([key, value]) => {
    if (value !== null && value !== undefined && value !== '') {
      queryParams.append(key, value.toString());
    }
  });

  const url = queryParams.toString()
    ? `${API_ENDPOINTS.USAGE_STATS}?${queryParams.toString()}`
    : API_ENDPOINTS.USAGE_STATS;

  return await fetchWithTimeout(url);
};

/**
 * 获取模型维度调用统计（概览页模型调用表 / Token 分布）。
 * 目前只有桌面端 Wails 绑定提供该聚合接口，HTTP 环境无对应服务。
 * @param {Object} params - period/start_date/end_date/model/request_family/source_view/upstream_name/limit
 */
export const fetchModelUsageStats = async (params = {}) => {
  if (isWailsEnvironment()) {
    return await WailsApi.getModelUsageStats(params);
  }
  throw new Error('当前运行环境暂不支持模型调用统计');
};

export const fetchRequests = async (params = {}) => {
  const queryInput = {
    ...params,
    source_view: params.source_view || params.sourceView || 'all'
  };

  // Wails 环境使用绑定
  if (isWailsEnvironment()) {
    // 直接传递所有参数给 wailsApi
    const data = await WailsApi.getRequests(queryInput);

    // 对 Wails 数据也进行规范化处理
    const requests = data.requests || [];
    const normalizedRequests = Array.isArray(requests) ? requests.map(normalizeRequest) : [];

    return {
      requests: normalizedRequests,
      total: data.total || normalizedRequests.length,
      page: data.page || 1,
      pageSize: data.pageSize || parseInt(queryInput.limit || queryInput.pageSize || 50),
      totalPages: data.totalPages || Math.ceil((data.total || 0) / (data.pageSize || parseInt(queryInput.limit || queryInput.pageSize || 50)))
    };
  }

  const queryParams = new URLSearchParams();
  Object.entries(queryInput).forEach(([key, value]) => {
    if (value !== null && value !== undefined && value !== '') {
      queryParams.append(key, value.toString());
    }
  });

  // 确保固定排序参数
  if (!queryParams.has('sort_by')) {
    queryParams.set('sort_by', 'start_time');
  }
  if (!queryParams.has('sort_order')) {
    queryParams.set('sort_order', 'desc');
  }

  const url = queryParams.toString()
    ? `${API_ENDPOINTS.USAGE_REQUESTS}?${queryParams.toString()}`
    : API_ENDPOINTS.USAGE_REQUESTS;

  const data = await fetchWithTimeout(url);

  const requests = data.requests || data.data || data || [];
  const normalizedRequests = Array.isArray(requests) ? requests.map(normalizeRequest) : [];

  return {
    requests: normalizedRequests,
    total: data.total || normalizedRequests.length,
    page: data.page || 1,
    pageSize: data.pageSize || data.limit || 50,
    totalPages: data.totalPages || Math.ceil((data.total || 0) / (data.pageSize || data.limit || 50))
  };
};

/**
 * 获取请求页上游筛选候选（查询范围内实际出现过的上游）。
 * 目前只有桌面端 Wails 绑定提供；HTTP 环境返回 null，由调用方退回按当前页生成候选。
 * @param {Object} params - 与 fetchRequests 相同；只用 start_date/end_date/request_family/source_view/include_compaction
 * @returns {Promise<Array|null>} [{ request_family, upstream_type, upstream_name }]
 */
export const fetchRequestUpstreamOptions = async (params = {}) => {
  if (!isWailsEnvironment()) {
    return null;
  }
  return await WailsApi.getRequestUpstreamOptions(params);
};

/**
 * 安全解析 JSON 字符串，失败返回 null。
 */
export const safeParseJson = (value) => {
  if (typeof value !== 'string' || !value.trim()) {
    return null;
  }
  try {
    return JSON.parse(value);
  } catch {
    return null;
  }
};

/**
 * 获取单请求生命周期详情（B5.2 权威接口，桌面专用）。
 * 返回解析好的对象：权威行、分段计时偏移、调度快照与隐私摘要。
 */
export const fetchRequestLifecycleDetail = async (requestId) => {
  if (!isWailsEnvironment()) {
    return { found: false, request: null, upstreamWriteMs: null, upstreamDispatchMs: null, upstreamConnMs: null, scheduleSnapshot: null, privacyScan: null };
  }
  const detail = await WailsApi.getRequestLifecycleDetail(requestId);
  return {
    found: detail?.found === true,
    request: detail?.request ? normalizeRequest(detail.request) : null,
    upstreamWriteMs: detail?.upstream_write_ms ?? null,
    upstreamDispatchMs: detail?.upstream_dispatch_ms ?? null,
    upstreamConnMs: detail?.upstream_conn_ms ?? null,
    scheduleSnapshot: safeParseJson(detail?.schedule_snapshot_json),
    privacyScan: safeParseJson(detail?.privacy_scan_json)
  };
};

/**
 * 获取模型列表（用于筛选器）
 * v5.0: 从 model_pricing 表获取，而不是从使用记录
 * @returns {Promise<Array>} - 模型名称数组
 */
export const fetchModels = async () => {
  // Wails 环境：从 model_pricing 表获取
  if (isWailsEnvironment()) {
    try {
      const pricings = await WailsApi.getModelPricings();
      // 返回模型名称数组，保持向后兼容
      return (pricings || []).map(p => ({
        model_name: p.modelName,
        name: p.modelName,
        display_name: p.displayName || p.modelName
      }));
    } catch (err) {
      console.warn('获取模型定价失败，降级到使用记录:', err);
    }
  }

  // HTTP 环境或 Wails 降级：从使用记录获取
  const data = await fetchWithTimeout(API_ENDPOINTS.USAGE_MODELS);
  if (data.success && data.data) return data.data;
  if (data.models) return data.models;
  if (Array.isArray(data)) return data;
  return [];
};

// ============================================
// 配置 API
// ============================================

export const fetchConfig = async () => {
  if (!isWailsEnvironment()) {
    throw new Error('当前配置界面仅支持 Wails 桌面环境');
  }
  return await WailsApi.getConfig();
};

// ============================================
// 图表数据 API
// ============================================

/**
 * 将 Chart.js 格式转换为 Recharts 格式
 * Chart.js: { labels: [...], datasets: [{data: [...]}, ...] }
 * Recharts: [{ time: '...', total: N, success: N, fail: N }, ...]
 */
const transformChartJsToRecharts = (chartJsData, keyMapping) => {
  if (!chartJsData?.labels || !chartJsData?.datasets) {
    return [];
  }

  const { labels, datasets } = chartJsData;
  return labels.map((label, index) => {
    const point = { time: label };
    datasets.forEach((dataset, datasetIndex) => {
      const key = keyMapping[datasetIndex] || `value${datasetIndex}`;
      point[key] = dataset.data?.[index] ?? 0;
    });
    return point;
  });
};

/**
 * 获取请求趋势数据（按 request_logs 时间分桶）
 * 失败时抛出错误，由调用方呈现局部错误状态（概览页快照需要区分「无数据」与「查询失败」）
 * @param {Object} params - period/start_date/end_date/model/request_family/source_view/upstream_name
 * @returns {Promise<Object>} - { period, granularity, start_time, end_time, points }
 */
export const fetchRequestTrendData = async (params = {}) => {
  // Wails 环境使用绑定
  if (isWailsEnvironment()) {
    return await WailsApi.getRequestTrendData(params);
  }

  throw new Error('当前运行环境暂不支持请求趋势查询');
};

/**
 * 获取响应时间数据
 * @param {number} minutes - 时间范围（分钟），默认 30
 */
export const fetchResponseTimeData = async (minutes = 30) => {
  // Wails 环境使用绑定
  if (isWailsEnvironment()) {
    return await WailsApi.getResponseTimeChart(minutes);
  }

  const data = await fetchWithTimeout(`/api/v1/chart/response-times?minutes=${minutes}`);
  // 转换为 Recharts 格式: avg, min, max
  return transformChartJsToRecharts(data, ['avg', 'min', 'max']);
};

/**
 * 获取 Token 使用数据
 */
export const fetchTokenUsageData = async () => {
  try {
    // Wails 环境使用绑定
    if (isWailsEnvironment()) {
      return await WailsApi.getTokenUsage();
    }

    const data = await fetchWithTimeout('/api/v1/tokens/usage');
    const current = data.current || data;
    return {
      input: current.input_tokens || 0,
      output: current.output_tokens || 0,
      cacheCreation: current.cache_creation_tokens || 0,
      cacheRead: current.cache_read_tokens || 0
    };
  } catch (error) {
    console.error('获取 Token 使用数据失败:', error);
    return { input: 0, output: 0, cacheCreation: 0, cacheRead: 0 };
  }
};

/**
 * 获取端点健康状态数据
 */
export const fetchEndpointHealthData = async () => {
  try {
    // Wails 环境使用绑定
    if (isWailsEnvironment()) {
      return await WailsApi.getEndpointHealthChart();
    }

    const data = await fetchWithTimeout('/api/v1/chart/endpoint-health');
    // 返回 { healthy: N, unhealthy: N } 或原始 Chart.js 格式
    if (data.labels && data.datasets) {
      const [healthy, unhealthy] = data.datasets[0]?.data || [0, 0];
      return { healthy, unhealthy };
    }
    return data;
  } catch (error) {
    console.error('获取端点健康状态数据失败:', error);
    return { healthy: 0, unhealthy: 0 };
  }
};

/**
 * 获取端点成本数据
 * 失败时抛出错误，由调用方呈现局部错误状态。
 */
export const fetchEndpointCostsData = async () => {
  // Wails 环境使用绑定
  if (isWailsEnvironment()) {
    return await WailsApi.getEndpointCosts();
  }

  const data = await fetchWithTimeout('/api/v1/chart/endpoint-costs');
  // 转换为 Recharts 格式: { name, tokens, cost }
  if (data.labels && data.datasets) {
    // 查找 Token 数据集（label 包含 "Token" 或 "token"）
    const tokensDataset = data.datasets.find(d =>
      d.label?.toLowerCase().includes('token')
    );
    // 查找成本数据集（label 包含 "成本" 或 "Cost" 或 "USD"）
    const costDataset = data.datasets.find(d =>
      d.label?.includes('成本') || d.label?.toLowerCase().includes('cost') || d.label?.includes('USD')
    );

    const tokensData = tokensDataset?.data || [];
    const costData = costDataset?.data || [];

    return data.labels.map((name, i) => ({
      name,
      tokens: tokensData[i] || 0,
      cost: costData[i] || 0
    }));
  }
  return [];
};

/**
 * 获取连接活动数据
 * @param {number} minutes - 时间范围（分钟），默认 60
 */
export const fetchConnectionActivityData = async (minutes = 60) => {
  // Wails 环境使用绑定
  if (isWailsEnvironment()) {
    return await WailsApi.getConnectionActivityChart(minutes);
  }

  const data = await fetchWithTimeout(`/api/v1/chart/connection-activity?minutes=${minutes}`);
  return transformChartJsToRecharts(data, ['connections']);
};

// ============================================
// 导出工具函数
// ============================================

export const formatUptime = (seconds) => {
  if (typeof seconds !== 'number' || seconds <= 0) return seconds;

  const hours = Math.floor(seconds / 3600);
  const minutes = Math.floor((seconds % 3600) / 60);
  const secs = Math.floor(seconds % 60);

  let result = '';
  if (hours > 0) result += `${hours}h `;
  if (minutes > 0) result += `${minutes}m `;
  if (secs > 0 || result === '') result += `${secs}s`;

  return result.trim();
};

export const formatNumber = (num) => {
  if (num >= 1000000) return (num / 1000000).toFixed(1) + 'M';
  if (num >= 1000) return (num / 1000).toFixed(1) + 'K';
  return num.toString();
};

export const formatCost = (cost) => {
  if (cost >= 1) return `$${cost.toFixed(2)}`;
  if (cost >= 0.01) return `$${cost.toFixed(3)}`;
  return `$${cost.toFixed(4)}`;
};

export const formatDuration = (ms) => {
  if (ms >= 1000) return `${(ms / 1000).toFixed(2)}s`;
  return `${ms}ms`;
};

export const formatTimestamp = (timestamp, timezone) => formatConfiguredTimestamp(timestamp, timezone);

// ============================================
// v5.0+ 端点存储 API (SQLite)
// ============================================

/**
 * 获取端点存储状态
 * @returns {Promise<Object>} - {enabled, storageType, totalCount, enabledCount}
 */
export const fetchEndpointStorageStatus = async () => {
  // Wails 环境使用绑定
  if (isWailsEnvironment()) {
    return await WailsApi.getEndpointStorageStatus();
  }
  // HTTP 环境暂不支持
  return { enabled: false, storageType: 'yaml', totalCount: 0, enabledCount: 0 };
};

/**
 * 获取所有端点记录（SQLite 存储）
 * @returns {Promise<Array>} - 端点记录数组
 */
export const fetchEndpointRecords = async () => {
  // Wails 环境使用绑定
  if (isWailsEnvironment()) {
    return await WailsApi.getEndpointRecords();
  }
  // HTTP 环境暂不支持
  return [];
};

/**
 * 创建端点记录
 * @param {Object} input - 端点配置
 * @returns {Promise<Object>}
 */
export const createEndpoint = async (input) => {
  // Wails 环境使用绑定
  if (isWailsEnvironment()) {
    return await WailsApi.createEndpointRecord(input);
  }
  throw new Error('HTTP 环境暂不支持端点存储功能');
};

/**
 * 更新端点配置
 * @param {string} name - 端点名称
 * @param {Object} input - 更新的配置
 * @returns {Promise<Object>}
 */
export const updateEndpoint = async (name, input) => {
  // Wails 环境使用绑定
  if (isWailsEnvironment()) {
    return await WailsApi.updateEndpointRecord(name, input);
  }
  throw new Error('HTTP 环境暂不支持端点存储功能');
};

/**
 * 删除端点
 * @param {string} name - 端点名称
 * @returns {Promise<Object>}
 */
export const deleteEndpoint = async (name) => {
  // Wails 环境使用绑定
  if (isWailsEnvironment()) {
    return await WailsApi.deleteEndpointRecord(name);
  }
  throw new Error('HTTP 环境暂不支持端点存储功能');
};

/**
 * v8：设置端点硬启用状态（关闭后任何模式都不会使用）
 */
export const setEndpointAvailability = async (name, enabled) => {
  if (isWailsEnvironment()) {
    return await WailsApi.setEndpointAvailability(name, enabled);
  }
  throw new Error('HTTP 环境暂不支持端点存储功能');
};

/**
 * v8：设置端点是否参与自动调度（关闭后仍可手动优选或固定使用）
 */
export const setEndpointAutoSchedule = async (name, enabled) => {
  if (isWailsEnvironment()) {
    return await WailsApi.setEndpointAutoSchedule(name, enabled);
  }
  throw new Error('HTTP 环境暂不支持端点存储功能');
};

// ============================================
// v6.0+ 账号池 API
// ============================================

const normalizeUpstreamAccountList = (data) => {
  const list = Array.isArray(data)
    ? data
    : (data?.accounts || data?.upstream_accounts || data?.data || []);

  return (Array.isArray(list) ? list : []).map(WailsApi.normalizeUpstreamAccount);
};

const normalizeEntityIdForUrl = (id) => encodeURIComponent(String(id));

export const fetchUpstreamAccounts = async () => {
  if (isWailsEnvironment()) {
    return await WailsApi.getUpstreamAccounts();
  }

  const data = await fetchWithTimeout('/api/v1/upstream-accounts');
  return normalizeUpstreamAccountList(data);
};

export const fetchLatestAccountScheduleSnapshot = async () => {
  if (isWailsEnvironment()) {
    return await WailsApi.getLatestAccountScheduleSnapshot();
  }

  return {
    unsupported: true,
    has_snapshot: false,
    hasSnapshot: false,
    candidates: [],
    message: '仅桌面版支持最近一次调度结果面板'
  };
};

export const createUpstreamAccount = async (input) => {
  const payload = WailsApi.buildUpstreamAccountPayload(input);

  if (isWailsEnvironment()) {
    return await WailsApi.createUpstreamAccount(payload);
  }

  return await fetchWithTimeout('/api/v1/upstream-accounts', {
    method: 'POST',
    body: JSON.stringify(payload)
  });
};

export const updateUpstreamAccount = async (id, input) => {
  const payload = WailsApi.buildUpstreamAccountPayload(input);

  if (isWailsEnvironment()) {
    return await WailsApi.updateUpstreamAccount(id, payload);
  }

  return await fetchWithTimeout(`/api/v1/upstream-accounts/${normalizeEntityIdForUrl(id)}`, {
    method: 'PUT',
    body: JSON.stringify(payload)
  });
};

export const fetchUpstreamAccountCredential = async (id) => {
  if (isWailsEnvironment()) {
    return await WailsApi.getUpstreamAccountCredential(id);
  }

  return await fetchWithTimeout(`/api/v1/upstream-accounts/${normalizeEntityIdForUrl(id)}/credential`);
};

export const deleteUpstreamAccount = async (id) => {
  if (isWailsEnvironment()) {
    return await WailsApi.deleteUpstreamAccount(id);
  }

  return await fetchWithTimeout(`/api/v1/upstream-accounts/${normalizeEntityIdForUrl(id)}`, {
    method: 'DELETE'
  });
};

export const moveUpstreamAccountToTier = async (id, targetTier) => {
  if (isWailsEnvironment()) {
    return await WailsApi.moveUpstreamAccountToTier(id, targetTier);
  }

  return await fetchWithTimeout(`/api/v1/upstream-accounts/${normalizeEntityIdForUrl(id)}/tier`, {
    method: 'POST',
    body: JSON.stringify({ target_tier: targetTier || 'primary' })
  });
};

export const swapUpstreamAccountGroups = async (sourceGroup, targetGroup) => {
  if (isWailsEnvironment()) {
    return await WailsApi.swapUpstreamAccountGroups(sourceGroup, targetGroup);
  }

  try {
    return await fetchWithTimeout('/api/v1/upstream-accounts/groups/swap', {
      method: 'POST',
      body: JSON.stringify({
        source_group: sourceGroup || '',
        target_group: targetGroup || ''
      })
    });
  } catch (error) {
    const errorText = error?.message || String(error || '');
    if (/404|not found/i.test(errorText)) {
      return {
        success: false,
        unsupported: true,
        changed: false,
        message: '当前后端版本暂未提供 SwapUpstreamAccountGroups'
      };
    }
    throw error;
  }
};

export const setGroupActiveAccount = async (groupKey, id) => {
  if (isWailsEnvironment()) {
    return await WailsApi.setGroupActiveAccount(groupKey, id);
  }

  try {
    return await fetchWithTimeout('/api/v1/upstream-accounts/groups/active-account', {
      method: 'POST',
      body: JSON.stringify({
        group_key: groupKey || '',
        account_id: normalizeEntityIdForUrl(id)
      })
    });
  } catch (error) {
    const errorText = error?.message || String(error || '');
    if (/404|not found/i.test(errorText)) {
      return {
        success: false,
        unsupported: true,
        changed: false,
        message: '当前后端版本暂未提供 SetGroupActiveAccount'
      };
    }
    throw error;
  }
};

export const pinUpstreamAccountSelection = async (id) => {
  if (isWailsEnvironment()) {
    return await WailsApi.pinUpstreamAccountSelection(id);
  }

  try {
    return await fetchWithTimeout(`/api/v1/upstream-accounts/${normalizeEntityIdForUrl(id)}/pin`, {
      method: 'POST'
    });
  } catch (error) {
    const errorText = error?.message || String(error || '');
    if (/404|not found/i.test(errorText)) {
      return {
        success: false,
        unsupported: true,
        changed: false,
        message: '当前后端版本暂未提供 PinUpstreamAccountSelection'
      };
    }
    throw error;
  }
};

export const enableAutomaticAccountSelection = async () => {
  if (isWailsEnvironment()) {
    return await WailsApi.enableAutomaticAccountSelection();
  }

  try {
    return await fetchWithTimeout('/api/v1/upstream-accounts/auto', {
      method: 'POST'
    });
  } catch (error) {
    const errorText = error?.message || String(error || '');
    if (/404|not found/i.test(errorText)) {
      return {
        success: false,
        unsupported: true,
        changed: false,
        message: '当前后端版本暂未提供 EnableAutomaticAccountSelection'
      };
    }
    throw error;
  }
};

export const toggleUpstreamAccount = async (id, enabled) => {
  if (isWailsEnvironment()) {
    return await WailsApi.toggleUpstreamAccount(id, enabled);
  }

  return await fetchWithTimeout(`/api/v1/upstream-accounts/${normalizeEntityIdForUrl(id)}/toggle`, {
    method: 'POST',
    body: JSON.stringify({ enabled: enabled !== false })
  });
};

export const clearUpstreamAccountCooldown = async (id) => {
  if (isWailsEnvironment()) {
    return await WailsApi.clearUpstreamAccountCooldown(id);
  }
  return await fetchWithTimeout(`/api/v1/upstream-accounts/${normalizeEntityIdForUrl(id)}/cooldown/clear`, { method: 'POST' });
};

export const testUpstreamAccount = async (id) => {
  if (isWailsEnvironment()) {
    return await WailsApi.testUpstreamAccount(id);
  }

  try {
    return await fetchWithTimeout(`/api/v1/upstream-accounts/${normalizeEntityIdForUrl(id)}/test`, {
      method: 'POST'
    });
  } catch (error) {
    const errorText = error?.message || String(error || '');
    if (/404|not found/i.test(errorText)) {
      return {
        success: false,
        unsupported: true,
        message: '当前后端版本暂未提供 TestUpstreamAccount'
      };
    }
    throw error;
  }
};

export const refreshUpstreamAccountProfile = async (id) => {
  if (isWailsEnvironment()) {
    return await WailsApi.refreshUpstreamAccountProfile(id);
  }

  try {
    return await fetchWithTimeout(`/api/v1/upstream-accounts/${normalizeEntityIdForUrl(id)}/profile/refresh`, {
      method: 'POST'
    });
  } catch (error) {
    const errorText = error?.message || String(error || '');
    if (/404|not found/i.test(errorText)) {
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
  if (isWailsEnvironment()) {
    return await WailsApi.generateChatGPTOAuthLink();
  }
  return {
    success: false,
    unsupported: true,
    message: '仅桌面版支持此功能'
  };
};

export const exchangeChatGPTOAuthCallback = async (sessionId, callbackUrl) => {
  if (isWailsEnvironment()) {
    return await WailsApi.exchangeChatGPTOAuthCallback(sessionId, callbackUrl);
  }
  return {
    success: false,
    unsupported: true,
    message: '仅桌面版支持此功能'
  };
};

// ============================================
// v5.1+ 系统设置 API (SQLite)
// ============================================

/**
 * 获取设置分类列表
 * @returns {Promise<Array>} - 分类信息数组
 */
export const fetchSettingCategories = async () => {
  // Wails 环境使用绑定
  if (isWailsEnvironment()) {
    return await WailsApi.getSettingCategories();
  }
  // HTTP 环境
  const data = await fetchWithTimeout('/api/v1/settings/categories');
  return data.categories || data || [];
};

/**
 * 获取所有设置
 * @returns {Promise<Array>} - 设置记录数组
 */
export const fetchAllSettings = async () => {
  // Wails 环境使用绑定
  if (isWailsEnvironment()) {
    return await WailsApi.getAllSettings();
  }
  // HTTP 环境
  const data = await fetchWithTimeout('/api/v1/settings');
  return data.settings || data || [];
};

/**
 * 获取指定分类的设置
 * @param {string} category - 分类名称
 * @returns {Promise<Array>} - 设置记录数组
 */
export const fetchSettingsByCategory = async (category) => {
  // Wails 环境使用绑定
  if (isWailsEnvironment()) {
    return await WailsApi.getSettingsByCategory(category);
  }
  // HTTP 环境
  const data = await fetchWithTimeout(`/api/v1/settings/${encodeURIComponent(category)}`);
  return data.settings || data || [];
};

/**
 * 批量更新设置
 * @param {Array} settings - [{category, key, value}, ...]
 * @returns {Promise<Object>}
 */
export const updateSettings = async (settings) => {
  // Wails 环境使用绑定
  if (isWailsEnvironment()) {
    return await WailsApi.batchUpdateSettings({ settings });
  }
  // HTTP 环境
  return await fetchWithTimeout('/api/v1/settings', {
    method: 'PUT',
    body: JSON.stringify({ settings })
  });
};

/**
 * 重置分类设置为默认值
 * @param {string} category - 分类名称
 * @returns {Promise<Object>}
 */
export const resetSettings = async (category) => {
  // Wails 环境使用绑定
  if (isWailsEnvironment()) {
    return await WailsApi.resetCategorySettings(category);
  }
  // HTTP 环境
  return await fetchWithTimeout(`/api/v1/settings/${encodeURIComponent(category)}/reset`, {
    method: 'POST'
  });
};

/**
 * 获取端口信息
 * @returns {Promise<Object>} - {preferred_port, actual_port, was_occupied}
 */
export const fetchPortInfo = async () => {
  // Wails 环境使用绑定
  if (isWailsEnvironment()) {
    return await WailsApi.getPortInfo();
  }
  // HTTP 环境
  const data = await fetchWithTimeout('/api/v1/settings/port');
  return data;
};

// ============================================
// 隐私保护 (v6.1+，仅桌面版)
// ============================================

const privacyDesktopOnlyError = () => new Error('隐私保护功能仅在桌面版可用');

export const fetchPrivacySettings = async () => {
  if (isWailsEnvironment()) {
    return await WailsApi.getPrivacySettings();
  }
  throw privacyDesktopOnlyError();
};

export const updatePrivacySettings = async (input) => {
  if (isWailsEnvironment()) {
    return await WailsApi.updatePrivacySettings(input);
  }
  throw privacyDesktopOnlyError();
};

export const fetchPrivacyRules = async () => {
  if (isWailsEnvironment()) {
    return await WailsApi.getPrivacyRules();
  }
  throw privacyDesktopOnlyError();
};

export const createPrivacyRule = async (input) => {
  if (isWailsEnvironment()) {
    return await WailsApi.createPrivacyRule(input);
  }
  throw privacyDesktopOnlyError();
};

export const fetchPrivacyExactSecrets = async () => {
  if (isWailsEnvironment()) {
    return await WailsApi.listPrivacyExactSecrets();
  }
  throw privacyDesktopOnlyError();
};

export const createPrivacyExactSecret = async (input) => {
  if (isWailsEnvironment()) {
    return await WailsApi.createPrivacyExactSecret(input);
  }
  throw privacyDesktopOnlyError();
};

export const updatePrivacyExactSecret = async (id, input) => {
  if (isWailsEnvironment()) {
    return await WailsApi.updatePrivacyExactSecret(id, input);
  }
  throw privacyDesktopOnlyError();
};

export const deletePrivacyExactSecret = async (id) => {
  if (isWailsEnvironment()) {
    return await WailsApi.deletePrivacyExactSecret(id);
  }
  throw privacyDesktopOnlyError();
};

export const clearPrivacyExactSecrets = async (confirmText) => {
  if (isWailsEnvironment()) {
    return await WailsApi.clearPrivacyExactSecrets(confirmText);
  }
  throw privacyDesktopOnlyError();
};

export const fetchPrivacySecretImportCandidates = async () => {
  if (isWailsEnvironment()) {
    return await WailsApi.listPrivacySecretImportCandidates();
  }
  throw privacyDesktopOnlyError();
};

export const importPrivacySecretCandidate = async (input) => {
  if (isWailsEnvironment()) {
    return await WailsApi.importPrivacySecretCandidate(input);
  }
  throw privacyDesktopOnlyError();
};

export const updatePrivacyRule = async (id, input) => {
  if (isWailsEnvironment()) {
    return await WailsApi.updatePrivacyRule(id, input);
  }
  throw privacyDesktopOnlyError();
};

export const deletePrivacyRule = async (id) => {
  if (isWailsEnvironment()) {
    return await WailsApi.deletePrivacyRule(id);
  }
  throw privacyDesktopOnlyError();
};

export const reorderPrivacyRules = async (orders) => {
  if (isWailsEnvironment()) {
    return await WailsApi.reorderPrivacyRules(orders);
  }
  throw privacyDesktopOnlyError();
};

export const testPrivacyRules = async (input) => {
  if (isWailsEnvironment()) {
    return await WailsApi.testPrivacyRules(input);
  }
  throw privacyDesktopOnlyError();
};

export const fetchPrivacyPresets = async () => {
  if (isWailsEnvironment()) {
    return await WailsApi.listPrivacyPresets();
  }
  throw privacyDesktopOnlyError();
};

export const importPrivacyPreset = async (presetId) => {
  if (isWailsEnvironment()) {
    return await WailsApi.importPrivacyPreset(presetId);
  }
  throw privacyDesktopOnlyError();
};

export const exportPrivacyRules = async () => {
  if (isWailsEnvironment()) {
    return await WailsApi.exportPrivacyRules();
  }
  throw privacyDesktopOnlyError();
};

export const fetchPrivacyRuntimeStats = async () => {
  if (isWailsEnvironment()) {
    return await WailsApi.getPrivacyRuntimeStats();
  }
  throw privacyDesktopOnlyError();
};
