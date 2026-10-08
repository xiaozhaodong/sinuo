// Claude 专属的行内策略补丁：单成员自动指定目标，自动模式清空目标。
export const claudeRouteModePatch = (binding, routeMode) => ({
  route_mode: routeMode,
  route_target: routeMode === 'auto' ? '' : binding.route_target || (binding.endpoints.length === 1 ? binding.endpoints[0].name : '')
});
