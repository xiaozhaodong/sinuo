// ============================================
// lifecycleDetailController - 详情拉取纯函数控制器（方案 F3）
// 2026-08-13
// 只负责调度与代际保护，不依赖 React；可配 node --test。
// 规格：
//   - 打开即加载：open 后立即 triggerNow（历史请求不会再收到事件，缺这步永远不拉取）
//   - 切换 requestId 立即加载新请求，不等事件
//   - 代际保护：(requestId, isOpen) 变化递增 generation，旧慢响应丢弃
//   - 事件过滤：仅 data?.request_id === requestId 时 schedule
//   - 连发事件由 scheduler 合并（250ms debounce / 1s max wait / single-flight）
//   - 关闭后晚到响应不回写
// ============================================

import { createRealtimeRefreshScheduler } from './realtimeRefreshScheduler.js';

export const createLifecycleDetailController = ({
  fetchDetail,
  subscribe = null,
  onChange,
  schedulerFactory = createRealtimeRefreshScheduler,
  debounceMs = 250,
  maxWaitMs = 1000
}) => {
  let generation = 0;
  let isOpen = false;
  let requestId = null;
  let unsubscribe = null;
  let scheduler = null;

  const teardown = () => {
    if (scheduler) {
      scheduler.cancel();
      scheduler = null;
    }
    if (unsubscribe) {
      unsubscribe();
      unsubscribe = null;
    }
  };

  const open = (nextRequestId) => {
    teardown();
    generation += 1;
    const gen = generation;
    isOpen = true;
    requestId = nextRequestId;

    if (!nextRequestId) {
      return;
    }

    let appliedRevision = 0;
    let hasSnapshot = false;

    const reload = async () => {
      try {
        const result = await fetchDetail(nextRequestId);
        // 代际保护：请求切换 / 关闭后的晚到响应一律丢弃。
        if (generation !== gen || !isOpen || requestId !== nextRequestId) {
          return;
        }
        const fetchedRevision = Number(result?.requestRevision ?? result?.request_revision ?? 0);
        if (result?.found && fetchedRevision < appliedRevision) return;
        hasSnapshot = result?.found === true;
        appliedRevision = hasSnapshot && Number.isFinite(fetchedRevision) ? fetchedRevision : 0;
        onChange(result);
      } catch (error) {
        if (generation !== gen || !isOpen || requestId !== nextRequestId) {
          return;
        }
        if (!hasSnapshot) onChange({ found: false, request: null, error });
      }
    };

    scheduler = schedulerFactory({ refresh: reload, debounceMs, maxWaitMs });

    if (typeof subscribe === 'function') {
      unsubscribe = subscribe((data) => {
        // 事件过滤：其它请求的事件直接忽略。
        if (generation !== gen || !isOpen || requestId !== nextRequestId || data?.request_id !== nextRequestId) {
          return;
        }
        scheduler?.schedule();
      });
    }

    // 打开即加载，同时校准订阅建立前的事件窗口。
    scheduler.triggerNow();
  };

  const close = () => {
    teardown();
    generation += 1;
    isOpen = false;
    requestId = null;
  };

  return { open, close };
};

export default createLifecycleDetailController;
