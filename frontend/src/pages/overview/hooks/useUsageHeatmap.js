import { useCallback, useEffect, useRef, useState } from 'react';
import { fetchUsageHeatmap } from '@utils/api.js';

// 一年用量单独加载；仅挂载和显式刷新触发，不依赖概览的短时间范围。
const useUsageHeatmap = () => {
  const [data, setData] = useState(null);
  const [error, setError] = useState(null);
  const [loading, setLoading] = useState(true);
  const requestIdRef = useRef(0);
  const mountedRef = useRef(false);

  const reload = useCallback(async () => {
    if (!mountedRef.current) return;
    const requestId = ++requestIdRef.current;
    setLoading(true);
    setError(null);
    try {
      const next = await fetchUsageHeatmap({ weeks: 53 });
      if (mountedRef.current && requestIdRef.current === requestId) setData(next);
    } catch (err) {
      if (mountedRef.current && requestIdRef.current === requestId) setError(err);
    } finally {
      if (mountedRef.current && requestIdRef.current === requestId) setLoading(false);
    }
  }, []);

  useEffect(() => {
    mountedRef.current = true;
    reload();
    return () => {
      mountedRef.current = false;
      requestIdRef.current += 1;
    };
  }, [reload]);

  return { data, error, loading, reload };
};

export default useUsageHeatmap;
