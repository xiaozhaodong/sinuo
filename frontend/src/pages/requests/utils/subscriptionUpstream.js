// 订阅直通请求的识别：请求记录只有上游端点名，按当前端点列表的认证方式判断。
// 局限：端点改过认证方式或被删除后，历史记录按当前配置显示。

/**
 * 从端点列表中取出订阅直通端点名
 * @param {Array<{name:string, authMode?:string}>} endpoints
 * @returns {Set<string>}
 */
export const buildSubscriptionEndpointNames = (endpoints = []) => new Set(
  (Array.isArray(endpoints) ? endpoints : [])
    .filter((endpoint) => endpoint?.authMode === 'client_oauth' && endpoint?.name)
    .map((endpoint) => endpoint.name)
);

/**
 * 请求是否经订阅直通端点转发
 * @param {Object} request
 * @param {Set<string>} subscriptionEndpointNames
 */
export const isSubscriptionRequest = (request, subscriptionEndpointNames) => Boolean(
  request
  && request.requestFamily === 'claude'
  && request.upstreamType === 'endpoint'
  && subscriptionEndpointNames?.has(request.upstreamName)
);
