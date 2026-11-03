export const sessionKey = (row) => row ? JSON.stringify([row.request_family, row.session_id]) : null;
export const isKnown = (value) => typeof value === 'number' && Number.isFinite(value);
export const isInputSpike = (point) => isKnown(point?.full_input) && isKnown(point?.previous)
  && point.previous > 0 && point.full_input >= point.previous * 2 && point.full_input - point.previous >= 10000;
export const formatSessionNumber = (value) => isKnown(value) ? value.toLocaleString() : '—';
export const formatSessionCost = (value) => isKnown(value) ? `$${value.toFixed(4)}` : '—';
export const formatSessionRatio = (value) => isKnown(value) ? `${(value * 100).toFixed(1)}%` : '—';

// 分通道淘汰旧响应：排行、走势和定位不能互相抢占，也不能在卸载后回写。
export const createSessionQueryController = () => {
  const versions = new Map();
  return {
    cancel(channel) { versions.set(channel, (versions.get(channel) || 0) + 1); },
    async run(channel, task, apply, fail) {
      const version = (versions.get(channel) || 0) + 1;
      versions.set(channel, version);
      try {
        const result = await task();
        if (versions.get(channel) !== version) return false;
        apply(result);
        return true;
      } catch (error) {
        if (versions.get(channel) !== version) return false;
        if (fail) fail(error);
        else throw error;
        return false;
      }
    }
  };
};

export const sessionDrillQuery = (scope, row, requestID) => ({
  ...scope, session_id: row.session_id, request_family: row.request_family,
  session_missing: false, target_request_id: requestID
});

export const requireLocatedRequest = (result, requestID) => {
  const row = result.requests.find((item) => item.requestId === requestID);
  if (!row) throw new Error('定位期间请求列表已变化，请刷新后重试');
  return row;
};
