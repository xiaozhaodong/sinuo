// Claude 端点策略的简短标签：请求页 Claude 按钮与接入面板共用。
// 模型路由优先；否则按全局路由模式显示自动调度（附最近命中端点）或手动目标。

import { isWebToolsBinding } from '../../endpoints/utils/modelRouting.js';

/**
 * @param {{modelRoutingEnabled?:boolean, bindings?:Array, routingState?:object}} state
 * @returns {string}
 */
export const resolveClaudeRouteLabel = ({ modelRoutingEnabled = false, bindings = [], routingState = {}, compact = false, hasAvailableEndpoints } = {}) => {
  if (modelRoutingEnabled) {
    const modelCount = bindings.filter((binding) => !isWebToolsBinding(binding)).length;
    return compact ? `${modelCount} 模型` : `模型路由 · ${modelCount} 模型`;
  }
  if (hasAvailableEndpoints === false) return '无可用端点';
  const state = routingState || {};
  const mode = state.mode || 'auto';
  if (mode === 'auto') {
    const effective = state.lastEffectiveEndpoint || state.last_effective_endpoint || '';
    return effective ? `Auto · ${effective}` : 'Auto';
  }
  return state.endpointName || state.endpoint_name || '未选择';
};
