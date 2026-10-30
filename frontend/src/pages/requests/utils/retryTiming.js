import { parseTimestampOrNull } from './lifecycle.js';

// 列表只展示可信的新口径：进行中须收到重试事件，结束后须有首次取连接偏移。
export const visibleRetryCount = (request, running) => {
  const count = request.retryCount;
  const trusted = running
    ? Boolean(parseTimestampOrNull(request.retryStartedAt))
    : Number.isFinite(request.upstreamFirstDispatchMs);
  return trusted && Number.isFinite(count) && count > 0 ? count : 0;
};

// 重试的取连接晚于上次写完时，首响位回到等待点；新写完到达后恢复跑秒。
export const firstResponseWait = (request) => {
  const sent = parseTimestampOrNull(request.firstTokenStartAt);
  const retry = parseTimestampOrNull(request.retryStartedAt);
  if (retry && (!sent || retry > sent)) {
    return { timestamp: request.retryStartedAt, hint: '上一次尝试未成功，正在重试（尚未发到上游）' };
  }
  return sent ? null : { timestamp: request.timestamp };
};

// 详情旧数据隐藏不可信的次数，保留换号或故障转移的独立说明。
export const retryPropertyVisible = (request, accountSwitchCount, isAccountRequest) => (
  Number.isFinite(request.upstreamFirstDispatchMs) || accountSwitchCount > 0 ||
  (!isAccountRequest && Boolean(request.requestedEndpoint && request.effectiveEndpoint &&
    request.requestedEndpoint !== request.effectiveEndpoint))
);
