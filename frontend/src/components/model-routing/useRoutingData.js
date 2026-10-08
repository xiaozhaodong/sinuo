import { useCallback, useEffect, useRef, useState } from 'react';
import { resolveErrorMessage } from '@utils/errors.js';

// 协议配置提供读取 API；加载、事件刷新与过期响应屏障共用一套生命周期。
export const useRoutingData = ({ active, ready, fetchData, subscribe }) => {
  const [data, setData] = useState({ settings: null, bindings: [], members: [] });
  const [loading, setLoading] = useState(ready);
  const [readFailed, setReadFailed] = useState(false);
  const [error, setError] = useState('');
  const requestRef = useRef(0);
  const load = useCallback(async () => {
    if (!ready) return;
    const request = ++requestRef.current;
    setLoading(true);
    setError('');
    try {
      const next = await fetchData();
      if (request !== requestRef.current) return;
      setData(next);
      setReadFailed(false);
    } catch (failure) {
      if (request !== requestRef.current) return;
      setReadFailed(true);
      setError(resolveErrorMessage(failure, '加载模型路由失败'));
    } finally {
      if (request === requestRef.current) setLoading(false);
    }
  }, [ready, fetchData]);
  useEffect(() => {
    if (!active) return undefined;
    const requests = requestRef;
    void load();
    const unsubscribe = ready ? subscribe('model-routing:update', load) : undefined;
    return () => { ++requests.current; unsubscribe?.(); };
  }, [active, load, ready, subscribe]);
  return { ...data, ready, loading, readFailed, error, setError, load };
};
