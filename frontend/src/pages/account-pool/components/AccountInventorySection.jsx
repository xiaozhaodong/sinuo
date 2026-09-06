// ============================================
// 账号资产工作台
// 2026-03-21 (Updated 2026-08-06:表格/网格双视图)
// ============================================

import { useState } from 'react';
import { Database } from 'lucide-react';
import { Button, CustomSelect, EmptyState } from '@components/ui';
import ViewModeSwitcher from '@components/ViewModeSwitcher.jsx';
import AccountDetailsDrawer from './AccountDetailsDrawer.jsx';
import AccountInventoryFilters from './AccountInventoryFilters.jsx';
import AccountInventoryGrid from './AccountInventoryGrid.jsx';
import AccountInventoryTable from './AccountInventoryTable.jsx';
import useAccountPoolDashboardState from '../hooks/useAccountPoolDashboardState.js';

const VIEW_MODE_STORAGE_KEY = 'accountPool.viewMode';
const readStoredViewMode = () => {
  try {
    return window.localStorage.getItem(VIEW_MODE_STORAGE_KEY) === 'grid' ? 'grid' : 'table';
  } catch {
    return 'table';
  }
};

const AccountInventorySection = ({
  inventory = {},
  onBatchAction,
  onOpenDetails,
  onToggleAccount,
  onClearAccountCooldown,
  onTestAccount,
  onRefreshAccountProfile,
  onMoveAccountToTier,
  onDeleteAccount,
  onEditAccount,
  busyKey = '',
  externalViewRequest = null,
  inlineDrawerOpen = false,
  inlineDrawerRow = null,
  onInlineDrawerClose
}) => {
  const state = useAccountPoolDashboardState({ inventory, onBatchAction, externalViewRequest });
  const handleOpenDetails = onOpenDetails || state.drawer.openDetails;
  const shouldRenderInlineDrawer = !onOpenDetails;
  const drawerOpen = shouldRenderInlineDrawer ? state.drawer.open : inlineDrawerOpen;
  const drawerRow = shouldRenderInlineDrawer ? state.drawer.activeRow : inlineDrawerRow;
  const handleCloseDrawer = shouldRenderInlineDrawer ? state.drawer.closeDetails : onInlineDrawerClose;

  const [viewMode, setViewMode] = useState(readStoredViewMode);
  const changeViewMode = (mode) => {
    setViewMode(mode);
    try {
      window.localStorage.setItem(VIEW_MODE_STORAGE_KEY, mode);
    } catch { /* 隐私模式等场景下写不进去就只在内存里生效 */ }
  };

  return (
    <section className="overflow-hidden rounded-2xl border border-line bg-surface shadow-sm">
      <AccountInventoryFilters
        searchTerm={state.toolbar.searchTerm}
        onSearchTermChange={state.toolbar.setSearchTerm}
        filterConfigs={state.filterConfigs}
        filterValues={state.toolbar.filterValues}
        onFilterChange={state.toolbar.setFilterValue}
        onResetFilters={state.toolbar.resetFilters}
        resultCount={state.toolbar.resultCount}
        selectedCount={state.selection.selectedCount}
        batchActions={state.batch.actions}
        batchFeedback={state.batch.feedback}
        onBatchAction={state.batch.runBatchAction}
        viewSwitcher={<ViewModeSwitcher value={viewMode} onChange={changeViewMode} compact />}
      />

      {state.visibleRows.length === 0 ? (
        <EmptyState
          icon={Database}
          title="暂无匹配账号"
          description="调整搜索词、保存视图或筛选条件后再试。"
        />
      ) : viewMode === 'grid' ? (
        <AccountInventoryGrid
          rows={state.visibleRows}
          busyKey={busyKey}
          selectedRowIds={state.selection.selectedRowIds}
          allVisibleSelected={state.selection.allVisibleSelected}
          selectedCount={state.selection.selectedCount}
          onToggleAllRows={state.selection.toggleAllVisibleRows}
          onToggleRow={state.selection.toggleRowSelection}
          onRowClick={handleOpenDetails}
          onToggleAccount={onToggleAccount}
          onClearAccountCooldown={onClearAccountCooldown}
          onTestAccount={onTestAccount}
          onRefreshAccountProfile={onRefreshAccountProfile}
          onEditAccount={onEditAccount}
          onDeleteAccount={onDeleteAccount}
        />
      ) : (
        <AccountInventoryTable
          rows={state.visibleRows}
          busyKey={busyKey}
          selectedRowIds={state.selection.selectedRowIds}
          selectedCount={state.selection.selectedCount}
          allVisibleSelected={state.selection.allVisibleSelected}
          onToggleAllRows={state.selection.toggleAllVisibleRows}
          onToggleRow={state.selection.toggleRowSelection}
          onRowClick={handleOpenDetails}
          onToggleAccount={onToggleAccount}
          onClearAccountCooldown={onClearAccountCooldown}
          onTestAccount={onTestAccount}
          onRefreshAccountProfile={onRefreshAccountProfile}
          onEditAccount={onEditAccount}
          onDeleteAccount={onDeleteAccount}
        />
      )}

      {state.pagination.totalCount > 0 ? (
        <div className="flex flex-col gap-3 border-t border-line-soft px-5 py-4 sm:flex-row sm:items-center sm:justify-between">
          <div className="text-xs text-fg-muted">
            第 {state.pagination.currentPage} / {state.pagination.totalPages} 页，共 {state.pagination.totalCount} 个账号
          </div>

          <div className="flex flex-wrap items-center gap-2">
            <span className="text-xs text-fg-subtle">每页</span>
            <div className="w-28">
              <CustomSelect
                size="md"
                options={state.pagination.pageSizeOptions}
                value={String(state.pagination.pageSize)}
                onChange={state.pagination.setPageSize}
                className="w-full"
              />
            </div>
            <Button
              type="button"
              variant="ghost"
              size="sm"
              disabled={!state.pagination.hasPreviousPage}
              onClick={state.pagination.goToPreviousPage}
            >
              上一页
            </Button>
            <Button
              type="button"
              variant="ghost"
              size="sm"
              disabled={!state.pagination.hasNextPage}
              onClick={state.pagination.goToNextPage}
            >
              下一页
            </Button>
          </div>
        </div>
      ) : null}

      {shouldRenderInlineDrawer ? (
        <AccountDetailsDrawer
          open={drawerOpen}
          row={drawerRow}
          busyKey={busyKey}
          onClose={handleCloseDrawer}
          onToggleAccount={onToggleAccount}
          onClearAccountCooldown={onClearAccountCooldown}
          onTestAccount={onTestAccount}
          onRefreshAccountProfile={onRefreshAccountProfile}
          onMoveAccountToTier={onMoveAccountToTier}
          onDeleteAccount={onDeleteAccount}
          onEditAccount={onEditAccount}
        />
      ) : null}
    </section>
  );
};

export default AccountInventorySection;
