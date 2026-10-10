// ============================================
// Account Pool 最近一次调度快照 Hook
// 2026-03-07
// ============================================

import { useCallback, useEffect, useRef, useState } from 'react';
import { fetchLatestAccountScheduleSnapshot } from '@utils/api.js';

const scheduleSnapshotPollIntervalMs = 5000;

const isPageVisible = () => typeof document === 'undefined' || document.visibilityState === 'visible';

const useLatestScheduleSnapshot = ({ showNotice, enabled = true }) => {
  const wasEnabled = useRef(enabled);
  const [latestScheduleSnapshot, setLatestScheduleSnapshot] = useState({ hasSnapshot: false, candidates: [] });
  const [snapshotUnsupported, setSnapshotUnsupported] = useState(false);

  const loadLatestScheduleSnapshot = useCallback(async ({ silent = false } = {}) => {
    try {
      const snapshot = await fetchLatestAccountScheduleSnapshot();
      if (snapshot?.unsupported) {
        setSnapshotUnsupported(true);
        setLatestScheduleSnapshot({
          hasSnapshot: false,
          has_snapshot: false,
          candidates: [],
          message: snapshot.message || ''
        });
        return;
      }
      setSnapshotUnsupported(false);
      setLatestScheduleSnapshot(snapshot && typeof snapshot === 'object'
        ? snapshot
        : { hasSnapshot: false, has_snapshot: false, candidates: [] });
    } catch (err) {
      if (!silent) {
        showNotice('error', err.message || '加载最近一次调度结果失败');
      }
    }
  }, [showNotice]);

  useEffect(() => {
    // 异步初始加载：setState 发生在 await 之后，非同步级联渲染，规则误报。
    // eslint-disable-next-line react-hooks/set-state-in-effect
    loadLatestScheduleSnapshot({ silent: true });
  }, [loadLatestScheduleSnapshot]);

  useEffect(() => {
    if (enabled && !wasEnabled.current) {
      // 切回账号池或调度编排时立即静默刷新；不等待下一个轮询周期。
      // eslint-disable-next-line react-hooks/set-state-in-effect
      loadLatestScheduleSnapshot({ silent: true });
    }
    wasEnabled.current = enabled;
  }, [enabled, loadLatestScheduleSnapshot]);

  useEffect(() => {
    if (!enabled || snapshotUnsupported || typeof document === 'undefined') {
      return undefined;
    }

    const handleVisibilityChange = () => {
      if (document.visibilityState === 'visible') {
        loadLatestScheduleSnapshot({ silent: true });
      }
    };

    document.addEventListener('visibilitychange', handleVisibilityChange);
    return () => document.removeEventListener('visibilitychange', handleVisibilityChange);
  }, [enabled, loadLatestScheduleSnapshot, snapshotUnsupported]);

  useEffect(() => {
    if (!enabled || snapshotUnsupported) {
      return undefined;
    }

    const timer = setInterval(() => {
      if (!isPageVisible()) {
        return;
      }
      loadLatestScheduleSnapshot({ silent: true });
    }, scheduleSnapshotPollIntervalMs);
    return () => clearInterval(timer);
  }, [enabled, loadLatestScheduleSnapshot, snapshotUnsupported]);

  return {
    latestScheduleSnapshot,
    snapshotUnsupported,
    loadLatestScheduleSnapshot
  };
};

export default useLatestScheduleSnapshot;
