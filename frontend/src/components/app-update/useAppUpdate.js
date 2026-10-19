import { useCallback, useEffect, useState } from 'react';
import { getAppUpdateStatus, checkAppUpdate, skipAppUpdateVersion } from '@utils/api.js';
import { isWailsEnvironment, openExternalURL, subscribeToEvent } from '@utils/wailsApi.js';
import { resolveErrorMessage } from '@utils/errors.js';
import { acceptAppUpdateStatus, emptyAppUpdateStatus } from './appUpdateView.js';

const useAppUpdate = () => {
  const [status, setStatus] = useState(emptyAppUpdateStatus);
  const [notice, setNotice] = useState('');
  const accept = useCallback((incoming) => setStatus((current) => acceptAppUpdateStatus(current, incoming)), []);

  useEffect(() => {
    let cancelled = false;
    const unsubscribe = isWailsEnvironment()
      ? subscribeToEvent('app-update:status', (incoming) => { if (!cancelled) accept(incoming); })
      : null;
    getAppUpdateStatus().then((incoming) => { if (!cancelled) accept(incoming); })
      .catch((error) => { if (!cancelled) setNotice(resolveErrorMessage(error, '读取更新状态失败')); });
    return () => { cancelled = true; unsubscribe?.(); };
  }, [accept]);

  const check = useCallback(async () => {
    setNotice('');
    try {
      accept(await checkAppUpdate());
    } catch (error) {
      setNotice(resolveErrorMessage(error, '检查更新失败'));
      // Wails 在返回 error 时不会携带另一个返回值，重新读取完整状态。
      try { accept(await getAppUpdateStatus()); } catch (readError) { setNotice(resolveErrorMessage(readError, '读取更新状态失败')); }
    }
  }, [accept]);

  const skip = useCallback(async (version) => {
    setNotice('');
    try {
      await skipAppUpdateVersion(version);
      accept(await getAppUpdateStatus());
    } catch (error) { setNotice(resolveErrorMessage(error, '保存跳过版本失败')); }
  }, [accept]);

  const openRelease = useCallback(async () => {
    if (!status.release_url) return;
    try { await openExternalURL(status.release_url); } catch (error) { setNotice(resolveErrorMessage(error, '打开下载页面失败')); }
  }, [status.release_url]);

  return { status, notice, check, skip, openRelease };
};

export default useAppUpdate;
