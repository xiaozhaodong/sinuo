import { UNSETTLED_STATUSES } from './constants.js';

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

const listDollars = new Intl.NumberFormat('en-US', {
  useGrouping: false, minimumFractionDigits: 3, maximumFractionDigits: 3
});
const detailDollars = new Intl.NumberFormat('en-US', { useGrouping: false, maximumFractionDigits: 6 });
const tinyDollars = new Intl.NumberFormat('en-US', { useGrouping: false, maximumSignificantDigits: 6 });
const preciseDollars = new Intl.NumberFormat('en-US', { useGrouping: false, maximumSignificantDigits: 17 });
const listCostThreshold = 0.001;
const detailCostThreshold = 0.000001;

export const getSettledRequestCost = (request) => {
  if (UNSETTLED_STATUSES.has(request.status) || request.costKnown === false) return null;
  return Number.isFinite(request.cost) ? request.cost : null;
};

export const formatRequestListCost = (cost) =>
  cost > 0 && cost < listCostThreshold ? `<$${listCostThreshold}` : `$${listDollars.format(cost)}`;

export const formatRequestDetailCost = (cost, { expandTiny = false } = {}) => {
  const tiny = cost > 0 && cost < detailCostThreshold;
  if (tiny && !expandTiny) return `<$${detailCostThreshold}`;
  const rounded = (tiny ? tinyDollars : detailDollars).format(cost);
  const threshold = tiny ? detailCostThreshold : listCostThreshold;
  // 金额实际低于阈值、舍入却恰好触阈时，正文保留上界，悬停展开可区分的精度。
  if (cost < threshold && Number(rounded) === threshold) {
    return expandTiny ? `$${preciseDollars.format(cost)}` : `<$${threshold}`;
  }
  return `$${rounded}`;
};
