import { useCallback, useMemo, useState } from 'react';
import { createPortal } from 'react-dom';
import { AlertTriangle, RefreshCw } from 'lucide-react';
import { ErrorMessage, LoadingSpinner } from '@components/ui';
import { ToolbarButton, ToolbarSeparator } from '@components/layout/ToolbarButton.jsx';
import SegmentedControl from '@components/desktop/SegmentedControl.jsx';
import { StatusBar, StatusItem, StatusSpacer } from '@components/desktop/StatusBar.jsx';
import useContextMenu from '@components/desktop/useContextMenu.jsx';
import { useTimezone } from '@contexts/TimezoneContext.jsx';
import { useNotice } from '@pages/account-pool/hooks';
import { buildAccountPoolDashboardModel } from '@pages/account-pool/utils.js';
import useSchedulerActions from './useSchedulerActions.js';
import SchedulerGroupsTable from './SchedulerGroupsTable.jsx';
import ScheduleCandidatesTable from './ScheduleCandidatesTable.jsx';
import SchedulerInspector from './SchedulerInspector.jsx';
import { buildStatusCounts, buildCandidateRows, countCandidates, buildAccountActionState } from './schedulerView.js';

const VIEW_OPTIONS = [{ value: 'arrange', label: '编排' }, { value: 'recent', label: '最近调度' }];

const ScheduleStatus = ({ view, groups, rows, snapshot, snapshotUnsupported }) => {
  const { formatListTimestamp } = useTimezone();
  const groupsCount = buildStatusCounts(groups);
  const candidates = countCandidates(rows);
  const requestId = snapshot?.requestId || snapshot?.request_id;
  const selectedAccountName = snapshot?.selectedAccountName || snapshot?.selected_account_name;
  const updatedAt = snapshot?.updatedAt || snapshot?.updated_at || snapshot?.capturedAt || snapshot?.captured_at;
  return (
    <StatusBar>
      {view === 'arrange' ? (
        <>
          <StatusItem label="主组" value={groupsCount.primary} /><StatusItem label="备组" value={groupsCount.backup} /><StatusItem label="冷备" value={groupsCount.cold} />
          <StatusItem label="可用" value={groupsCount.available} />
          <StatusItem label="不可用" value={groupsCount.unavailable} valueClassName={groupsCount.unavailable > 0 ? 'text-warn' : 'text-fg-body'} />
        </>
      ) : (
        <>
          <StatusItem label="候选" value={candidates.total} />
          <StatusItem label="跳过" value={candidates.skipped} valueClassName={candidates.skipped > 0 ? 'text-warn' : 'text-fg-body'} />
          {selectedAccountName && <StatusItem label="命中" value={selectedAccountName} />}
        </>
      )}
      <StatusSpacer />
      {view === 'recent' && requestId && <span className="selectable font-mono" title={requestId}>{requestId}</span>}
      {!snapshotUnsupported && <StatusItem label="5s 自动刷新" />}
      <StatusItem label="更新于" value={updatedAt ? formatListTimestamp(updatedAt) : '—'} />
    </StatusBar>
  );
};

const ScheduleContent = ({ poolData, view, scheduler, rows, actions, onViewInInventory, onRowContextMenu }) => {
  if (poolData.loading) return <LoadingSpinner text="加载账号数据..." />;
  if (poolData.error && poolData.accounts.length === 0) return <ErrorMessage title="账号数据加载失败" message={poolData.error} onRetry={() => poolData.loadData()} />;
  if (view === 'recent') return <ScheduleCandidatesTable snapshot={poolData.latestScheduleSnapshot} snapshotUnsupported={poolData.snapshotUnsupported} rows={rows} />;
  return <SchedulerGroupsTable groups={scheduler.groups} summary={scheduler.summary} actions={actions} onViewInInventory={onViewInInventory} onRowContextMenu={onRowContextMenu} />;
};

const useScheduleMenu = (actions, onViewInInventory) => {
  const { contextMenu, openContextMenu } = useContextMenu();
  const onRowContextMenu = (event, account, group) => {
    const state = buildAccountActionState(account, group, actions.busyKey);
    openContextMenu(event, [
      { label: '设为本组首选', disabled: state.preferredDisabled, onSelect: () => actions.setActiveAccount(account.rawAccount, group) },
      { label: '设为全局固定', disabled: state.pinDisabled, onSelect: () => actions.pinAccount(account.rawAccount) },
      '-',
      { label: '在账号池中查看本组', onSelect: () => onViewInInventory(group.key) }
    ]);
  };
  return { contextMenu, onRowContextMenu };
};

const CodexSchedulePage = ({ active = true, headerTarget = null, poolData, onViewInInventory }) => {
  const { timezone } = useTimezone();
  const { showNotice } = useNotice({ enabled: active });
  const { accounts, loading, error, loadData, latestScheduleSnapshot, loadLatestScheduleSnapshot, snapshotUnsupported } = poolData;
  const [view, setView] = useState('arrange');
  const [refreshing, setRefreshing] = useState(false);
  const { scheduler } = useMemo(() => buildAccountPoolDashboardModel({ accounts, latestScheduleSnapshot, timezone }), [accounts, latestScheduleSnapshot, timezone]);
  const rows = useMemo(() => buildCandidateRows(latestScheduleSnapshot), [latestScheduleSnapshot]);
  const actions = useSchedulerActions({ loadData, loadLatestScheduleSnapshot, showNotice });
  const { contextMenu, onRowContextMenu } = useScheduleMenu(actions, onViewInInventory);
  const refresh = useCallback(async () => {
    setRefreshing(true);
    try {
      await Promise.all([loadData({ silent: true }), loadLatestScheduleSnapshot({ silent: true })]);
    } finally {
      setRefreshing(false);
    }
  }, [loadData, loadLatestScheduleSnapshot]);
  const toolbar = (
    <div className="flex items-center gap-1">
      <SegmentedControl options={VIEW_OPTIONS} value={view} onChange={setView} ariaLabel="调度视图" />
      <ToolbarSeparator /><ToolbarButton icon={RefreshCw} title="刷新" loading={loading || refreshing} onClick={refresh} />
    </div>
  );
  return (
    <div className="motion-page flex min-h-0 min-w-0 flex-1 bg-surface">
      {headerTarget && active && createPortal(toolbar, headerTarget)}
      <div className="flex min-w-0 flex-1 flex-col">
        {!headerTarget && active && <div className="flex shrink-0 justify-end border-b border-line px-3 py-2">{toolbar}</div>}
        {error && accounts.length > 0 && <div role="alert" className="selectable flex shrink-0 items-center gap-2 border-b border-danger-line bg-danger-soft px-4 py-1.5 text-xs text-danger"><AlertTriangle size={13} />{error}</div>}
        <div className="min-h-0 flex-1 overflow-auto">
          <ScheduleContent poolData={poolData} view={view} scheduler={scheduler} rows={rows} actions={actions} onViewInInventory={onViewInInventory} onRowContextMenu={onRowContextMenu} />
        </div>
        <ScheduleStatus view={view} groups={scheduler.groups} rows={rows} snapshot={latestScheduleSnapshot} snapshotUnsupported={snapshotUnsupported} />
      </div>
      <SchedulerInspector summary={scheduler.summary} snapshot={latestScheduleSnapshot} snapshotUnsupported={snapshotUnsupported} busyKey={actions.busyKey} enableAutoSelection={actions.enableAutoSelection} view={view} onViewCandidates={() => setView('recent')} />
      {active && contextMenu}
    </div>
  );
};

export default CodexSchedulePage;
