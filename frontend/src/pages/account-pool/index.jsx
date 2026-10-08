// ============================================
// Account Pool 页面 - 账号管理
// 2026-03-22
// 2026-10-07 桌面化：左侧账号列表（范围条 + 表格 + 状态栏）、右侧常驻检查器（原详情抽屉）
// ============================================

import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { createPortal } from 'react-dom';
import { isWailsEnvironment, openExternalURL as openWailsExternalURL } from '@utils/wailsApi.js';
import { ErrorMessage, LoadingSpinner } from '@components/ui';
import {
  enableAutomaticAccountSelection,
  moveUpstreamAccountToTier,
  pinUpstreamAccountSelection,
  refreshUpstreamAccountProfile,
  setGroupActiveAccount,
  swapUpstreamAccountGroups,
  testUpstreamAccount,
  toggleUpstreamAccount
} from '@utils/api.js';
import {
  AccountDetailsDrawer,
  AccountFormDialog,
  AccountInventorySection,
  DeleteAccountDialog,
  NoticeToast,
  PageHeader,
  SchedulerDrawer
} from './components';
import CodexModelRoutingDrawer from './components/CodexModelRoutingDrawer.jsx';
import {
  buildAccountPoolDashboardModel,
  isValidAccountId,
  resolveAccountId
} from './utils.js';
import { partitionRowsByTargetTier } from './utils/batchMove.js';
import {
  useAccountPoolAccounts,
  useAccountPoolActions,
  useAccountPoolForm,
  useLatestScheduleSnapshot,
  useNotice
} from './hooks';
import { useTimezone } from '@contexts/TimezoneContext.jsx';

const openExternalURL = (url = '') => {
  const target = String(url || '').trim();
  if (!target) return false;

  if (isWailsEnvironment()) {
    void openWailsExternalURL(target).catch(() => {
      window.open(target, '_blank', 'noopener,noreferrer');
    });
    return true;
  }

  const opened = window.open(target, '_blank', 'noopener,noreferrer');
  return opened !== null;
};

const normalizeBatchActionLabel = (label = '批量操作') => String(label || '批量操作').trim();

const buildBatchResultMessage = (actionLabel, successCount, failedCount, skippedCount) => {
  const label = normalizeBatchActionLabel(actionLabel);

  if (successCount > 0 && failedCount === 0 && skippedCount === 0) {
    return `已对 ${successCount} 个账号完成${label}`;
  }

  if (successCount > 0 && failedCount === 0 && skippedCount > 0) {
    return `${label}完成 ${successCount} 个，跳过 ${skippedCount} 个（已在目标组）`;
  }

  if (successCount > 0) {
    return `${label}完成 ${successCount} 个，失败 ${failedCount} 个${skippedCount > 0 ? `，跳过 ${skippedCount} 个` : ''}`;
  }

  if (skippedCount > 0 && failedCount === 0) {
    return `${label}已跳过 ${skippedCount} 个账号，所选账号已在目标组`;
  }

  return `${label}失败，失败 ${failedCount} 个${skippedCount > 0 ? `，跳过 ${skippedCount} 个` : ''}`;
};

const AccountPoolPage = ({ embedded = false, active = true, headerTarget = null, modelRoutingOpenRequest = null, onModelRoutingOpenRequestHandled }) => {
  const { timezone } = useTimezone();
  const { notice, showNotice, closeNotice } = useNotice();
  const { accounts, loading, error, loadData } = useAccountPoolAccounts();
  const {
    latestScheduleSnapshot,
    snapshotUnsupported,
    loadLatestScheduleSnapshot
  } = useLatestScheduleSnapshot({ showNotice });

  const {
    accountModalOpen,
    accountSubmitting,
    accountCredentialLoading,
    editingAccount,
    accountForm,
    setAccountForm,
    oauthActionLoading,
    oauthSectionExpanded,
    setOauthSectionExpanded,
    oauthSession,
    oauthCallbackURL,
    setOauthCallbackURL,
    resetOAuthWorkflow,
    handleGenerateOAuthLink,
    handleExtractRTFromCallback,
    closeAccountModal,
    openCreateAccount,
    openEditAccount,
    submitAccountForm
  } = useAccountPoolForm({ loadData, showNotice });

  const {
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
  } = useAccountPoolActions({
    loadData,
    loadLatestScheduleSnapshot,
    showNotice
  });

  const [sharedDrawerRowId, setSharedDrawerRowId] = useState(null);
  const [schedulerDrawerOpen, setSchedulerDrawerOpen] = useState(false);
  const [codexModelRoutingOpen, setCodexModelRoutingOpen] = useState(false);
  const [inventoryViewRequest, setInventoryViewRequest] = useState(null);
  const [schedulerBusyKey, setSchedulerBusyKey] = useState('');
  const consumedModelRoutingRequestRef = useRef(null);

  useEffect(() => {
    const requestId = modelRoutingOpenRequest?.requestId;
    if (!active || requestId === undefined || requestId === consumedModelRoutingRequestRef.current) return;
    consumedModelRoutingRequestRef.current = requestId;
    setCodexModelRoutingOpen(true);
    onModelRoutingOpenRequestHandled?.();
  }, [active, modelRoutingOpenRequest, onModelRoutingOpenRequestHandled]);

  const dashboardModel = useMemo(
    () => buildAccountPoolDashboardModel({ accounts, latestScheduleSnapshot, timezone }),
    [accounts, latestScheduleSnapshot, timezone]
  );

  const rowById = useMemo(
    () => new Map((dashboardModel.inventory.rows || []).map((row) => [String(row.id), row])),
    [dashboardModel.inventory.rows]
  );
  const sharedDrawerRow = sharedDrawerRowId ? rowById.get(String(sharedDrawerRowId)) || null : null;

  const openSharedDrawer = useCallback((target) => {
    if (!target) return;

    if (typeof target === 'object' && target.detail) {
      setSharedDrawerRowId(String(target.id));
      return;
    }

    const accountId = typeof target === 'object' ? resolveAccountId(target) : target;
    if (!isValidAccountId(accountId)) {
      return;
    }

    if (rowById.has(String(accountId))) {
      setSharedDrawerRowId(String(accountId));
    }
  }, [rowById]);

  const closeSharedDrawer = useCallback(() => {
    setSharedDrawerRowId(null);
  }, []);

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

  const handleViewGroupInInventory = useCallback((groupKey) => {
    setSchedulerDrawerOpen(false);
    setInventoryViewRequest({
      type: 'focus-group',
      groupKey,
      requestId: Date.now()
    });
  }, []);

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

  const executeBatchAction = useCallback(async (payload) => {
    const ids = Array.isArray(payload?.ids) ? payload.ids : [];
    let rows = Array.isArray(payload?.rows) ? payload.rows : ids.map((id) => rowById.get(String(id))).filter(Boolean);
    const actionKey = String(payload?.actionKey || '').trim().toLowerCase();
    const actionLabel = normalizeBatchActionLabel(payload?.actionLabel);
    const meta = payload?.meta || {};

    if (!rows.length) {
      return { success: false, message: '请先选择至少一个账号', keepSelection: true };
    }

    let successCount = 0;
    let failedCount = 0;
    let skippedCount = 0;

    const isMoveTierAction = actionKey === 'batch-move-tier' || actionKey === 'move-backup' || actionKey === 'move-cold-standby' || actionKey === 'move-primary';
    const normalizedTargetTier = String(meta.targetTier || (actionKey === 'move-backup' ? 'backup' : actionKey === 'move-cold-standby' ? 'cold' : 'primary')).trim().toLowerCase();

    if (isMoveTierAction) {
      const partition = partitionRowsByTargetTier(rows, normalizedTargetTier);
      rows = partition.eligibleRows;
      skippedCount += partition.skippedRows.length;

      if (!rows.length) {
        const message = buildBatchResultMessage(actionLabel, 0, 0, skippedCount);
        showNotice('info', message);
        return {
          success: false,
          message,
          keepSelection: true
        };
      }
    }

    for (const row of rows) {
      const account = row?.raw || row?.detail?.rawAccount || row;
      const accountId = resolveAccountId(account);

      if (!isValidAccountId(accountId)) {
        failedCount += 1;
        continue;
      }

      try {
        let result = null;

        if (actionKey === 'batch-test' || actionKey === 'test') {
          result = await testUpstreamAccount(accountId);
        } else if (actionKey === 'batch-refresh-profile' || actionKey === 'refresh-profile') {
          result = await refreshUpstreamAccountProfile(accountId);
        } else if (actionKey === 'batch-toggle' || actionKey === 'toggle-enabled') {
          result = await toggleUpstreamAccount(accountId, meta.enabled !== false);
        } else if (isMoveTierAction) {
          result = await moveUpstreamAccountToTier(accountId, normalizedTargetTier === 'cold' ? 'cold' : normalizedTargetTier === 'backup' ? 'backup' : 'primary');
          if (result?.changed === false) {
            skippedCount += 1;
            continue;
          }
        } else {
          skippedCount += 1;
          continue;
        }

        if (result?.unsupported) {
          skippedCount += 1;
          continue;
        }

        if (result?.success === false) {
          failedCount += 1;
          continue;
        }

        successCount += 1;
      } catch {
        failedCount += 1;
      }
    }

    if (successCount > 0) {
      await Promise.all([
        loadData({ silent: true }),
        loadLatestScheduleSnapshot({ silent: true })
      ]);
    }

    const message = buildBatchResultMessage(actionLabel, successCount, failedCount, skippedCount);
    const success = successCount > 0 && failedCount === 0;
    const partialSuccess = successCount > 0 && failedCount > 0;

    showNotice(success || partialSuccess ? 'success' : 'info', message);

    return {
      success: success || partialSuccess,
      message,
      keepSelection: !success
    };
  }, [loadData, loadLatestScheduleSnapshot, rowById, showNotice]);

  if (error && !loading) {
    return (
      <div className="p-5">
        <ErrorMessage
          title="账号数据加载失败"
          message={error}
          onRetry={() => loadData()}
        />
      </div>
    );
  }

  if (loading) {
    return <LoadingSpinner text="加载账号数据..." />;
  }

  const header = (
    <PageHeader
      embedded={embedded}
      loading={busyKey === 'reload'}
      onCreate={openCreateAccount}
      onRefresh={handleRefreshAll}
      onOpenScheduler={() => setSchedulerDrawerOpen(true)}
      onOpenCodexModelRouting={() => setCodexModelRoutingOpen(true)}
    />
  );

  return (
    <div className="motion-page flex min-h-0 min-w-0 flex-1 flex-col bg-surface">
      {headerTarget
        ? (active && createPortal(header, headerTarget))
        : (!embedded && <div className="shrink-0 border-b border-line px-4 py-2">{header}</div>)}

      <NoticeToast notice={active ? notice : null} onClose={closeNotice} />

      <div className="flex min-h-0 flex-1">
        <AccountInventorySection
          inventory={dashboardModel.inventory}
          busyKey={busyKey}
          externalViewRequest={inventoryViewRequest}
          onBatchAction={executeBatchAction}
          onOpenDetails={openSharedDrawer}
          onToggleAccount={handleToggleAccount}
          onClearAccountCooldown={handleClearAccountCooldown}
          onTestAccount={handleTestAccount}
          onRefreshAccountProfile={handleRefreshAccountProfile}
          onMoveAccountToTier={handleMoveAccountToTier}
          onDeleteAccount={handleDeleteAccount}
          onEditAccount={openEditAccount}
          activeRowId={sharedDrawerRow ? String(sharedDrawerRow.id) : null}
        />

        <AccountDetailsDrawer
          open={active && Boolean(sharedDrawerRow)}
          row={sharedDrawerRow}
          busyKey={busyKey}
          onClose={closeSharedDrawer}
          onToggleAccount={handleToggleAccount}
          onClearAccountCooldown={handleClearAccountCooldown}
          onTestAccount={handleTestAccount}
          onRefreshAccountProfile={handleRefreshAccountProfile}
          onMoveAccountToTier={handleMoveAccountToTier}
          onDeleteAccount={handleDeleteAccount}
          onEditAccount={openEditAccount}
        />
      </div>

      <SchedulerDrawer
        open={active && schedulerDrawerOpen}
        onClose={() => setSchedulerDrawerOpen(false)}
        scheduler={dashboardModel.scheduler}
        latestScheduleSnapshot={latestScheduleSnapshot}
        snapshotUnsupported={snapshotUnsupported}
        onSwapGroup={handleSwapSchedulerGroup}
        onSetActiveAccount={handleSchedulerSetActiveAccount}
        onPinAccountSelection={handleSchedulerPinAccount}
        onEnableAutoSelection={handleSchedulerEnableAutoSelection}
        onViewInInventory={handleViewGroupInInventory}
        busyKey={schedulerBusyKey}
      />

      <CodexModelRoutingDrawer
        open={active && codexModelRoutingOpen}
        onClose={() => setCodexModelRoutingOpen(false)}
        accounts={accounts}
        showNotice={showNotice}
      />

      <AccountFormDialog
        open={active && accountModalOpen}
        editingAccount={editingAccount}
        accountSubmitting={accountSubmitting}
        accountCredentialLoading={accountCredentialLoading}
        accountForm={accountForm}
        setAccountForm={setAccountForm}
        onClose={closeAccountModal}
        onSubmit={submitAccountForm}
        oauthSectionExpanded={oauthSectionExpanded}
        setOauthSectionExpanded={setOauthSectionExpanded}
        oauthActionLoading={oauthActionLoading}
        oauthSession={oauthSession}
        oauthCallbackURL={oauthCallbackURL}
        setOauthCallbackURL={setOauthCallbackURL}
        onGenerateOAuthLink={handleGenerateOAuthLink}
        onExtractRTFromCallback={handleExtractRTFromCallback}
        onResetOAuthWorkflow={resetOAuthWorkflow}
        showNotice={showNotice}
        openExternalURL={openExternalURL}
      />

      <DeleteAccountDialog
        deleteTarget={active ? deleteTarget : null}
        busyKey={busyKey}
        onClose={closeDeleteDialog}
        onSubmit={handleConfirmDeleteAccount}
      />
    </div>
  );
};

export default AccountPoolPage;
