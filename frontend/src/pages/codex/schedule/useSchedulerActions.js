import { useCallback, useState } from 'react';
import {
  enableAutomaticAccountSelection,
  pinUpstreamAccountSelection,
  setGroupActiveAccount,
  swapUpstreamAccountGroups
} from '@utils/api.js';
import { isValidAccountId, resolveAccountId } from '@pages/account-pool/utils.js';

const useSchedulerActions = ({ loadData, loadLatestScheduleSnapshot, showNotice }) => {
  const [schedulerBusyKey, setSchedulerBusyKey] = useState('');

  const handleSchedulerSetActiveAccount = useCallback(async (account, group) => {
    const groupKey = String(group?.key || '').trim().toLowerCase();
    const accountId = resolveAccountId(account);
    if (!groupKey || !isValidAccountId(accountId)) {
      showNotice('error', '缺少组别或账号 ID，无法设置组内首选账号');
      return;
    }

    const busyToken = `group-active-${groupKey}-${accountId}`;
    setSchedulerBusyKey(busyToken);
    try {
      const result = await setGroupActiveAccount(groupKey, accountId);
      if (result?.unsupported) {
        showNotice('info', result.message || '当前后端版本暂不支持设置组内首选账号');
        return;
      }
      if (result?.changed !== true) {
        showNotice('info', result?.message || '当前组首选账号未发生变化');
        return;
      }

      showNotice('success', result?.message || '已更新当前组首选账号');
      await Promise.all([
        loadData({ silent: true }),
        loadLatestScheduleSnapshot({ silent: true })
      ]);
    } catch (err) {
      showNotice('error', err.message || '设置组内首选账号失败');
    } finally {
      setSchedulerBusyKey('');
    }
  }, [loadData, loadLatestScheduleSnapshot, showNotice]);

  const handleSchedulerPinAccount = useCallback(async (account) => {
    const accountId = resolveAccountId(account);
    const accountName = account?.account_name || account?.accountName || '目标账号';
    if (!isValidAccountId(accountId)) {
      showNotice('error', '缺少账号 ID，无法设为手动账号');
      return;
    }

    setSchedulerBusyKey(`scheduler-pin-${accountId}`);
    try {
      const result = await pinUpstreamAccountSelection(accountId);
      if (result?.unsupported) {
        showNotice('info', result.message || '当前后端版本暂不支持固定账号');
        return;
      }
      if (result?.changed !== true) {
        showNotice('info', result?.message || `「${accountName}」已经是当前手动账号`);
        return;
      }

      showNotice('success', result?.message || `已将「${accountName}」设为手动账号`);
      await Promise.all([
        loadData({ silent: true }),
        loadLatestScheduleSnapshot({ silent: true })
      ]);
    } catch (err) {
      showNotice('error', err.message || '设置手动账号失败');
    } finally {
      setSchedulerBusyKey('');
    }
  }, [loadData, loadLatestScheduleSnapshot, showNotice]);

  const handleSchedulerEnableAutoSelection = useCallback(async () => {
    setSchedulerBusyKey('scheduler-enable-auto');
    try {
      const result = await enableAutomaticAccountSelection();
      if (result?.unsupported) {
        showNotice('info', result.message || '当前后端版本暂不支持启用编排');
        return;
      }
      if (result?.changed !== true) {
        showNotice('info', result?.message || '当前已按编排自动调度');
        return;
      }

      showNotice('success', result?.message || '已启用编排自动调度');
      await Promise.all([
        loadData({ silent: true }),
        loadLatestScheduleSnapshot({ silent: true })
      ]);
    } catch (err) {
      showNotice('error', err.message || '启用编排失败');
    } finally {
      setSchedulerBusyKey('');
    }
  }, [loadData, loadLatestScheduleSnapshot, showNotice]);

  const handleSwapSchedulerGroup = useCallback(async (group, action) => {
    const sourceGroup = String(group?.key || '').trim().toLowerCase();
    const targetGroup = String(action?.targetGroupKey || '').trim().toLowerCase();
    if (!sourceGroup || !targetGroup) {
      showNotice('error', '缺少整组交换目标，无法执行编排动作');
      return;
    }

    const busyToken = `group-swap-${sourceGroup}-${action?.key || 'swap'}`;
    setSchedulerBusyKey(busyToken);
    try {
      const result = await swapUpstreamAccountGroups(sourceGroup, targetGroup);
      if (result?.unsupported) {
        showNotice('info', result.message || '当前后端版本暂不支持整组交换');
        return;
      }
      if (result?.changed !== true) {
        showNotice('info', result?.message || '当前整组交换未产生变化');
        return;
      }

      showNotice('success', result?.message || '已完成相邻组整组交换');
      await Promise.all([
        loadData({ silent: true }),
        loadLatestScheduleSnapshot({ silent: true })
      ]);
    } catch (err) {
      showNotice('error', err.message || '整组交换失败');
    } finally {
      setSchedulerBusyKey('');
    }
  }, [loadData, loadLatestScheduleSnapshot, showNotice]);

  return {
    busyKey: schedulerBusyKey,
    setActiveAccount: handleSchedulerSetActiveAccount,
    pinAccount: handleSchedulerPinAccount,
    enableAutoSelection: handleSchedulerEnableAutoSelection,
    swapGroup: handleSwapSchedulerGroup
  };
};

export default useSchedulerActions;

