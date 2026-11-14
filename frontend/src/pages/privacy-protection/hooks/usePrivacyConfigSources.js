import { useCallback, useEffect, useRef, useState } from 'react';
import { resolveErrorMessage } from '@utils/errors.js';
import { fetchConfigSources } from '../utils/configSources.js';

const usePrivacyConfigSources = () => {
  const [state, setState] = useState(null);
  const [readError, setReadError] = useState('');
  const [actionError, setActionError] = useState('');
  const [busy, setBusy] = useState(false);
  const active = useRef(false);
  const pendingAction = useRef(false);
  const revision = useRef(0);

  const reload = useCallback(async () => {
    const current = ++revision.current;
    try {
      const next = await fetchConfigSources();
      if (active.current && current === revision.current) { setState(next); setReadError(''); }
    } catch (err) {
      if (active.current && current === revision.current) setReadError(resolveErrorMessage(err, '读取配置来源失败'));
    }
  }, []);

  useEffect(() => {
    active.current = true;
    let stopped = false;
    let timer;
    const poll = async () => {
      if (!document.hidden) await reload();
      if (!stopped) timer = setTimeout(poll, 2000);
    };
    poll();
    return () => {
      stopped = true;
      active.current = false;
      // 请求序号不是 DOM 引用；卸载时使所有在途读取失效。
      // eslint-disable-next-line react-hooks/exhaustive-deps
      revision.current++;
      clearTimeout(timer);
    };
  }, [reload]);

  const run = useCallback(async (task) => {
    if (pendingAction.current) return false;
    pendingAction.current = true; setBusy(true); setActionError('');
    try {
      await task();
      await reload();
      return true;
    } catch (err) {
      if (active.current) setActionError(resolveErrorMessage(err));
      await reload();
      return false;
    } finally {
      pendingAction.current = false;
      if (active.current) setBusy(false);
    }
  }, [reload]);

  return { state, error: actionError || readError, busy, reload, run };
};

export default usePrivacyConfigSources;
