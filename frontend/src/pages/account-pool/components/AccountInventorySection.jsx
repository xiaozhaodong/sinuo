// ============================================
// 账号资产工作台
// 2026-03-21 (Updated 2026-08-06:表格/网格双视图)
// 2026-10-07 桌面化：筛选范围条 + 贴边列表 + 底部状态栏（计数与分页）；
//            单击选中显示右侧检查器，↑↓ 切换，右键 / 行尾「更多」弹出操作菜单
// ============================================

import { useMemo, useRef, useState } from 'react';
import { Users } from 'lucide-react';
import ViewModeSwitcher from '@components/ViewModeSwitcher.jsx';
import { StatusBar, StatusItem, StatusSpacer } from '@components/desktop/StatusBar.jsx';
import StatusPager from '@components/desktop/StatusPager.jsx';
import useContextMenu from '@components/desktop/useContextMenu.jsx';
import useListKeyboard from '@components/desktop/useListKeyboard.js';
import ListEmpty from '@components/desktop/ListEmpty.jsx';
import AccountDetailsDrawer from './AccountDetailsDrawer.jsx';
import AccountInventoryFilters from './AccountInventoryFilters.jsx';
import AccountInventoryGrid from './AccountInventoryGrid.jsx';
import AccountInventoryTable from './AccountInventoryTable.jsx';
import useAccountPoolDashboardState, { DEFAULT_INVENTORY_PAGE_SIZES } from '../hooks/useAccountPoolDashboardState.js';

const VIEW_MODE_STORAGE_KEY = 'accountPool.viewMode';
const readStoredViewMode = () => {
  try {
    return window.localStorage.getItem(VIEW_MODE_STORAGE_KEY) === 'grid' ? 'grid' : 'table';
  } catch {
    return 'table';
  }
};

const TIER_ACTIONS = [
  { key: 'primary', label: '主组' },
  { key: 'backup', label: '备组' },
  { key: 'cold', label: '冷备' }
];

const accountOf = (row) => row.raw || row.detail?.rawAccount || row;

// 状态栏计数：按全部账号（不受筛选影响）
const countByState = (rows = []) => rows.reduce((counts, row) => {
  counts.total += 1;
  if (row.stateTone === 'emerald') counts.available += 1;
  if (row.stateTone === 'amber') counts.cooldown += 1;
  if (row.stateTone === 'rose') counts.authFailed += 1;
  return counts;
}, { total: 0, available: 0, cooldown: 0, authFailed: 0 });

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
  activeRowId = null,
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
  const selectedRowId = shouldRenderInlineDrawer
    ? (drawerRow ? String(drawerRow.id) : null)
    : activeRowId;

  const [viewMode, setViewMode] = useState(readStoredViewMode);
  const changeViewMode = (mode) => {
    setViewMode(mode);
    try {
      window.localStorage.setItem(VIEW_MODE_STORAGE_KEY, mode);
    } catch { /* 隐私模式等场景下写不进去就只在内存里生效 */ }
  };

  const listRef = useRef(null);
  const { contextMenu, openContextMenu, openMenuAt } = useContextMenu();
  const counts = useMemo(() => countByState(state.rows), [state.rows]);
  const visibleRowsById = useMemo(
    () => new Map(state.visibleRows.map((row) => [String(row.id), row])),
    [state.visibleRows]
  );
  const visibleIds = useMemo(() => [...visibleRowsById.keys()], [visibleRowsById]);
  const handleListKeyDown = useListKeyboard({
    ids: visibleIds,
    selectedId: selectedRowId,
    onSelect: (id) => handleOpenDetails(visibleRowsById.get(id)),
    containerRef: listRef
  });

  const handleRowContextMenu = (event, row) => {
    handleOpenDetails(row);
    const account = accountOf(row);
    const busy = Boolean(busyKey);
    openContextMenu(event, [
      { label: '测试连通性', disabled: busy, onSelect: () => onTestAccount?.(account) },
      { label: '刷新画像', disabled: busy, onSelect: () => onRefreshAccountProfile?.(account) },
      row.stateKey === 'cooldown' && { label: '解除冷却', disabled: busy, onSelect: () => onClearAccountCooldown?.(account) },
      { label: row.enabled === false ? '启用账号' : '停用账号', disabled: busy, onSelect: () => onToggleAccount?.(account) },
      '-',
      ...TIER_ACTIONS.map((tier) => ({
        label: row.groupKey === tier.key ? `已在${tier.label}` : `移到${tier.label}`,
        disabled: busy || row.groupKey === tier.key,
        onSelect: () => onMoveAccountToTier?.(account, tier.key)
      })),
      '-',
      { label: '编辑…', onSelect: () => onEditAccount?.(account) },
      { label: '删除…', danger: true, onSelect: () => onDeleteAccount?.(account) }
    ]);
  };

  return (
    <section aria-label="账号资产" className="flex min-w-0 flex-1">
      <div className="flex min-w-0 flex-1 flex-col">
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
          onOpenMenu={openMenuAt}
          viewSwitcher={<ViewModeSwitcher value={viewMode} onChange={changeViewMode} compact />}
        />

        <div
          ref={listRef}
          tabIndex={0}
          onKeyDown={handleListKeyDown}
          aria-label="账号列表"
          className={`min-h-0 flex-1 overflow-auto focus:outline-none ${viewMode === 'grid' ? 'bg-pane' : ''}`}
        >
          {state.visibleRows.length === 0 ? (
            <ListEmpty icon={Users} title="暂无匹配账号" description="调整搜索词或筛选条件后再试，或点击工具栏的 + 新增账号" />
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
              activeRowId={selectedRowId}
              selectedRowIds={state.selection.selectedRowIds}
              allVisibleSelected={state.selection.allVisibleSelected}
              onToggleAllRows={state.selection.toggleAllVisibleRows}
              onToggleRow={state.selection.toggleRowSelection}
              onRowClick={handleOpenDetails}
              onRowContextMenu={handleRowContextMenu}
              onToggleAccount={onToggleAccount}
            />
          )}
        </div>

        <StatusBar>
          <StatusItem label="账号" value={counts.total} />
          <StatusItem label="可用" value={counts.available} />
          <StatusItem label="冷却中" value={counts.cooldown} valueClassName={counts.cooldown > 0 ? 'text-warn' : 'text-fg-body'} />
          <StatusItem label="鉴权失效" value={counts.authFailed} valueClassName={counts.authFailed > 0 ? 'text-danger' : 'text-fg-body'} />
          <StatusSpacer />
          {state.pagination.totalCount > 0 && (
            <StatusPager
              currentPage={state.pagination.currentPage}
              pageSize={state.pagination.pageSize}
              total={state.pagination.totalCount}
              pageSizeOptions={DEFAULT_INVENTORY_PAGE_SIZES}
              unit="个"
              onPageChange={state.pagination.goToPage}
              onPageSizeChange={state.pagination.setPageSize}
              onOpenMenu={openMenuAt}
            />
          )}
        </StatusBar>
      </div>

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

      {contextMenu}
    </section>
  );
};

export default AccountInventorySection;
