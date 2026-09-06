// ============================================
// Account Pool 行级操作 Hook
// 2026-03-07
// ============================================

import { useCallback, useState } from 'react';
import {
  deleteUpstreamAccount,
  clearUpstreamAccountCooldown,
  moveUpstreamAccountToTier,
  refreshUpstreamAccountProfile,
  testUpstreamAccount,
  toggleUpstreamAccount
} from '@utils/api.js';
import { buildManualSwitchSuccessMessage, isValidAccountId, resolveAccountId } from '../utils.js';

const useAccountPoolActions = ({ loadData, loadLatestScheduleSnapshot, showNotice }) => {
  const [busyKey, setBusyKey] = useState('');
  const [deleteTarget, setDeleteTarget] = useState(null);

  const runRowAction = useCallback(async (key, action, successText, { skipRefresh = false } = {}) => {
    setBusyKey(key);
    try {
      const result = await action();

      if (result?.unsupported) {
        showNotice('info', result.message || '当前后端版本暂不支持该操作');
      } else if (result?.success === false) {
        showNotice('error', result.message || '操作失败');
      } else if (successText) {
        const text = typeof successText === 'function' ? successText(result) : successText;
        if (text) {
          showNotice('success', text);
        }
      }

      if (!skipRefresh) {
        await loadData({ silent: true });
      }
      return result;
    } catch (err) {
      showNotice('error', err.message || '操作失败');
      return null;
    } finally {
      setBusyKey('');
    }
  }, [loadData, showNotice]);

  const handleDeleteAccount = useCallback((account) => {
    const accountId = resolveAccountId(account);
    if (!isValidAccountId(accountId)) {
      showNotice('error', '账号缺少 ID，无法删除');
      return;
    }
    setDeleteTarget(account);
  }, [showNotice]);

  const closeDeleteDialog = useCallback(() => {
    if (deleteTarget) {
      const deleteId = resolveAccountId(deleteTarget);
      if (busyKey === `account-delete-${deleteId}`) return;
    }
    setDeleteTarget(null);
  }, [busyKey, deleteTarget]);

  const handleConfirmDeleteAccount = useCallback(async (event) => {
    event.preventDefault();
    if (!deleteTarget) return;

    const accountId = resolveAccountId(deleteTarget);
    if (!isValidAccountId(accountId)) {
      showNotice('error', '账号缺少 ID，无法删除');
      setDeleteTarget(null);
      return;
    }

    const result = await runRowAction(
      `account-delete-${accountId}`,
      () => deleteUpstreamAccount(accountId),
      `已删除账号「${deleteTarget.account_name || deleteTarget.accountName}」`
    );

    if (result && result.success !== false && !result.unsupported) {
      setDeleteTarget(null);
    }
  }, [deleteTarget, runRowAction, showNotice]);

  const handleToggleAccount = useCallback(async (account) => {
    const accountId = resolveAccountId(account);
    if (!isValidAccountId(accountId)) {
      showNotice('error', '账号缺少 ID，无法切换状态');
      return;
    }

    const nextEnabled = !account.enabled;
    await runRowAction(
      `account-toggle-${accountId}`,
      () => toggleUpstreamAccount(accountId, nextEnabled),
      nextEnabled ? `已启用「${account.account_name || account.accountName}」` : `已停用「${account.account_name || account.accountName}」`
    );
  }, [runRowAction, showNotice]);

  const handleClearAccountCooldown = useCallback(async (account) => {
    const accountId = resolveAccountId(account);
    if (!isValidAccountId(accountId)) {
      showNotice('error', '账号缺少 ID，无法解除冷却');
      return;
    }
    await runRowAction(
      `account-cooldown-${accountId}`,
      () => clearUpstreamAccountCooldown(accountId),
      `已解除账号「${account.account_name || account.accountName || accountId}」的冷却`
    );
  }, [runRowAction, showNotice]);

  const handleMoveAccountToTier = useCallback(async (account, targetTier) => {
    const accountId = resolveAccountId(account);
    if (!isValidAccountId(accountId)) {
      showNotice('error', '账号缺少 ID，无法调整组别');
      return;
    }

    setBusyKey(`account-switch-${accountId}`);
    try {
      const normalizedTargetTier = targetTier === 'cold'
        ? 'cold'
        : targetTier === 'backup'
          ? 'backup'
          : 'primary';
      const result = await moveUpstreamAccountToTier(accountId, normalizedTargetTier);

      if (result?.unsupported) {
        showNotice('info', result.message || '当前后端版本暂不支持账号改组');
        return;
      }

      if (result?.changed !== true) {
        if (normalizedTargetTier === 'cold') {
          showNotice('info', '该账号已在冷备');
        } else if (normalizedTargetTier === 'backup') {
          showNotice('info', '该账号已在备组');
        } else {
          showNotice('info', '该账号已在主组');
        }
        return;
      }

      const accountName = account.account_name || account.accountName || `账号 ${accountId}`;
      showNotice(
        'success',
        result?.message || buildManualSwitchSuccessMessage(accountName, normalizedTargetTier)
      );
      await Promise.all([
        loadData({ silent: true }),
        loadLatestScheduleSnapshot({ silent: true })
      ]);
    } catch (err) {
      showNotice('error', err.message || '账号改组失败');
    } finally {
      setBusyKey('');
    }
  }, [loadData, loadLatestScheduleSnapshot, showNotice]);

  const handleTestAccount = useCallback(async (account) => {
    const accountId = resolveAccountId(account);
    if (!isValidAccountId(accountId)) {
      showNotice('error', '账号缺少 ID，无法测试');
      return;
    }

    await runRowAction(
      `account-test-${accountId}`,
      () => testUpstreamAccount(accountId),
      (result) => result?.message || `已触发账号「${account.account_name || account.accountName}」连通性测试`
    );
  }, [runRowAction, showNotice]);

  const handleRefreshAccountProfile = useCallback(async (account) => {
    const accountId = resolveAccountId(account);
    if (!isValidAccountId(accountId)) {
      showNotice('error', '账号缺少 ID，无法刷新账号信息');
      return;
    }

    await runRowAction(
      `account-profile-${accountId}`,
      () => refreshUpstreamAccountProfile(accountId),
      (result) => result?.message || `已刷新账号「${account.account_name || account.accountName}」的信息`
    );
  }, [runRowAction, showNotice]);

  const handleRefreshAll = useCallback(async () => {
    setBusyKey('reload');
    try {
      await Promise.all([
        loadData({ silent: true }),
        loadLatestScheduleSnapshot({ silent: true })
      ]);
    } finally {
      setBusyKey('');
    }
  }, [loadData, loadLatestScheduleSnapshot]);

  return {
    busyKey,
    deleteTarget,
    closeDeleteDialog,
    handleConfirmDeleteAccount,
    handleClearAccountCooldown,
    handleDeleteAccount,
    handleMoveAccountToTier,
    handleRefreshAccountProfile,
    handleRefreshAll,
    handleTestAccount,
    handleToggleAccount
  };
};

export default useAccountPoolActions;
