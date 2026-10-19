import { createElement, Fragment, useCallback, useEffect, useRef, useState } from 'react';
import { getAppUpdateStatus, checkAppUpdate, skipAppUpdateVersion, downloadAppUpdate, cancelAppUpdateDownload, restartToUpdate, cancelAppUpdateRestart } from '@utils/api.js';
import { isWailsEnvironment, openExternalURL, subscribeToEvent } from '@utils/wailsApi.js';
import { resolveErrorMessage } from '@utils/errors.js';
import useConfirm from '@hooks/useConfirm.js';
import { acceptAppUpdateStatus, emptyAppUpdateStatus } from './appUpdateView.js';
import { runAppUpdateRestart } from './appUpdateRestart.js';
import AppUpdateRestartDialog from './AppUpdateRestartDialog.jsx';

const useAppUpdate = () => {
  const [status, setStatus] = useState(emptyAppUpdateStatus);
  const [notice, setNotice] = useState('');
  const { confirm, confirmDialog } = useConfirm();
  const [requestPrompt, setRequestPrompt] = useState(null);
  const [pendingOperations, setPendingOperations] = useState(0);
  const restartBusy = useRef(false);
  const accept = useCallback((incoming) => setStatus((current) => acceptAppUpdateStatus(current, incoming)), []);
  const runOperation = useCallback(async (operation) => {
    setPendingOperations((count) => count + 1);
    try { return await operation(); }
    finally { setPendingOperations((count) => count - 1); }
  }, []);

  useEffect(() => {
    let cancelled = false;
    const unsubscribe = isWailsEnvironment()
      ? subscribeToEvent('app-update:status', (incoming) => { if (!cancelled) accept(incoming); })
      : null;
    getAppUpdateStatus().then((incoming) => { if (!cancelled) accept(incoming); })
      .catch((error) => { if (!cancelled) setNotice(resolveErrorMessage(error, '读取更新状态失败')); });
    return () => { cancelled = true; unsubscribe?.(); };
  }, [accept]);

  const check = useCallback(() => runOperation(async () => {
    setNotice('');
    try {
      accept(await checkAppUpdate());
    } catch (error) {
      setNotice(resolveErrorMessage(error, '检查更新失败'));
      // Wails 在返回 error 时不会携带另一个返回值，重新读取完整状态。
      try { accept(await getAppUpdateStatus()); } catch (readError) { setNotice(resolveErrorMessage(readError, '读取更新状态失败')); }
    }
  }), [accept, runOperation]);

  const skip = useCallback((version) => runOperation(async () => {
    setNotice('');
    try {
      await skipAppUpdateVersion(version);
      accept(await getAppUpdateStatus());
    } catch (error) { setNotice(resolveErrorMessage(error, '保存跳过版本失败')); }
  }), [accept, runOperation]);

  const openRelease = useCallback(() => runOperation(async () => {
    if (!status.release_url) return;
    try { await openExternalURL(status.release_url); } catch (error) { setNotice(resolveErrorMessage(error, '打开下载页面失败')); }
  }), [status.release_url, runOperation]);

  const operate = useCallback((operation) => runOperation(async () => {
    setNotice('');
    try { await operation(); accept(await getAppUpdateStatus()); }
    catch (error) { setNotice(resolveErrorMessage(error, '更新操作失败')); }
  }), [accept, runOperation]);
  const download = useCallback(() => operate(downloadAppUpdate), [operate]);
  const cancelDownload = useCallback(() => operate(cancelAppUpdateDownload), [operate]);
  const cancelRestart = useCallback(() => operate(cancelAppUpdateRestart), [operate]);
  const chooseRequests = useCallback((activeRequests) => new Promise((resolve) => {
    setRequestPrompt((previous) => { previous?.resolve(''); return { activeRequests, resolve }; });
  }), []);
  const settleRequests = useCallback((mode) => {
    setRequestPrompt((previous) => { previous?.resolve(mode); return null; });
  }, []);
  const restart = useCallback(async () => {
    if (restartBusy.current) return;
    restartBusy.current = true;
    try {
      await operate(() => runAppUpdateRestart({
        readStatus: getAppUpdateStatus,
        confirmIdle: () => confirm({ title: '完成更新', message: '重启 Sinuo 以完成更新？', confirmText: '重启更新' }),
        chooseRequests, restart: restartToUpdate
      }));
    } finally { restartBusy.current = false; }
  }, [chooseRequests, confirm, operate]);
  const dialogs = createElement(Fragment, null, confirmDialog, requestPrompt
    ? createElement(AppUpdateRestartDialog, { activeRequests: requestPrompt.activeRequests, onChoose: settleRequests }) : null);

  return {
    status, notice, check, skip, openRelease, download, cancelDownload, restart, cancelRestart, dialogs,
    operationPending: pendingOperations > 0
  };
};

export default useAppUpdate;
