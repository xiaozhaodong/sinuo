// ============================================
// Account Pool 页面 - 账号管理
// 2026-03-22
// 2026-10-07 桌面化：左侧账号列表（范围条 + 表格 + 状态栏）、右侧常驻检查器（原详情抽屉）
// ============================================

import { useCallback, useMemo, useState } from 'react';
import { createPortal } from 'react-dom';
import { isWailsEnvironment, openExternalURL as openWailsExternalURL } from '@utils/wailsApi.js';
import { ErrorMessage, LoadingSpinner } from '@components/ui';
import {
  moveUpstreamAccountToTier,
  refreshUpstreamAccountProfile,
  testUpstreamAccount,
  toggleUpstreamAccount
} from '@utils/api.js';
import { AccountDetailsDrawer, AccountFormDialog, AccountInventorySection, DeleteAccountDialog, PageHeader } from './components';
import { buildAccountPoolDashboardModel, isValidAccountId, resolveAccountId } from './utils.js';
import { partitionRowsByTargetTier } from './utils/batchMove.js';
import { useAccountPoolActions, useAccountPoolForm, useNotice } from './hooks';
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

const AccountPoolPage = ({ embedded = false, active = true, headerTarget = null, poolData, inventoryViewRequest = null }) => {
  const { timezone } = useTimezone();
  const { showNotice } = useNotice({ enabled: active });
  const { accounts, loading, error, loadData, latestScheduleSnapshot, loadLatestScheduleSnapshot } = poolData;

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
    />
  );

  return (
    <div className="motion-page flex min-h-0 min-w-0 flex-1 flex-col bg-surface">
      {headerTarget
        ? (active && createPortal(header, headerTarget))
        : (!embedded && <div className="shrink-0 border-b border-line px-4 py-2">{header}</div>)}

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
