// useWailsLogs.helpers.js - 日志缓冲区纯状态辅助函数

export const createInitialLogBuffer = () => ({
  logs: [],
  // 单调递增的已接收实时日志总数；缓冲区满 maxLogs 截断时仍持续累加
  receivedCount: 0,
});

export function filterLogItems(logList, levelFilter = null) {
  const items = Array.isArray(logList) ? logList : [];
  if (!levelFilter) return items;
  return items.filter((log) => log.level === levelFilter);
}

/**
 * 合并一批实时日志并按 maxLogs 截断，同时累计实际到达的日志条数
 */
export function appendLogBatch(prevBuffer, batchLogs, maxLogs, levelFilter = null) {
  const incoming = filterLogItems(batchLogs, levelFilter);
  if (incoming.length === 0) {
    return prevBuffer;
  }

  const combined = [...prevBuffer.logs, ...incoming];
  const nextLogs = maxLogs > 0 && combined.length > maxLogs
    ? combined.slice(-maxLogs)
    : combined;

  return {
    logs: nextLogs,
    receivedCount: prevBuffer.receivedCount + incoming.length,
  };
}

/**
 * 用历史日志快照替换当前缓冲区（用于首次加载或手动刷新），不把历史快照误计为实时增量
 */
export function replaceRecentLogs(prevBuffer, recentLogs, maxLogs, levelFilter = null) {
  const filtered = filterLogItems(recentLogs, levelFilter);
  const nextLogs = maxLogs > 0 && filtered.length > maxLogs
    ? filtered.slice(-maxLogs)
    : filtered;

  return {
    logs: nextLogs,
    receivedCount: prevBuffer.receivedCount,
  };
}
