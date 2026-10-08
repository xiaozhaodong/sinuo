// 请求列表、详情和生命周期共用的展示分类。保留原始 status，不能由模糊 EOF 推断续接。
export const getRequestPresentation = (request = {}) => {
  const reason = request.failureReason ?? request.failure_reason ?? '';
  const source = request.requestSource ?? request.request_source ?? '';
  const isInterrupted = request.status === 'cancelled'
    && source === 'codex_subscription_proxy'
    && reason === 'subscription_client_disconnected';

  return {
    status: isInterrupted ? 'interrupted' : request.status,
    isInterrupted,
    isNeutral: request.status === 'cancelled',
    failureReason: reason,
    cancelReason: request.cancelReason ?? request.cancel_reason ?? '',
    heading: request.status === 'cancelled' ? '结束原因' : '错误信息',
    description: isInterrupted
      ? '本次响应尚未完成时，客户端连接已结束。消息插入、取消或客户端重连都可能触发这种情况，具体触发原因未确认。'
      : ''
  };
};

// 路由模式展示名：详情面板「概要」与生命周期「调度」共用；未知值与缺省都按自动
const ROUTE_MODE_LABELS = {
  manual_fixed: '严格固定',
  manual_preferred: '手动优选'
};

export const formatRouteMode = (mode) => ROUTE_MODE_LABELS[mode] || '自动';
