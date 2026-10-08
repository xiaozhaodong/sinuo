// ============================================
// Claude 端点页（嵌在「Claude 管理」的「端点」页签）
// 2026-10-07 桌面化：统计卡 / 宽表格 / 网格卡片 → 贴边表格 + 右侧检查器 + 状态栏；行操作改为右键菜单
// ============================================
import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { createPortal } from 'react-dom';
import { Activity, AlertTriangle, Database, Plus, RefreshCw, Route } from 'lucide-react';
import { ErrorMessage, LoadingSpinner } from '@components/ui';
import { ToolbarButton } from '@components/layout/ToolbarButton.jsx';
import useEndpointsData from '@hooks/useEndpointsData.js';
import { DeleteConfirmDialog, EndpointForm, EndpointScheduleDrawer, resolveSnapshotOutcome } from './components';
import EndpointsTable from './components/EndpointsTable.jsx';
import EndpointInspector from './components/EndpointInspector.jsx';
import { StatusBar, StatusItem, StatusSpacer } from '@components/desktop/StatusBar.jsx';
import useContextMenu from '@components/desktop/useContextMenu.jsx';
import useListKeyboard from '@components/desktop/useListKeyboard.js';
import ListEmpty from '@components/desktop/ListEmpty.jsx';
import { copyTextToClipboard } from '@pages/requests/components/clipboard.js';
import { fetchLatestEndpointScheduleSnapshot } from '@utils/endpointScheduleApi.js';
import { resolveErrorMessage, toError } from '@utils/errors.js';
import { useTimezone } from '@contexts/TimezoneContext.jsx';
import {
  clearClaudeRoutingOverride,
  createEndpointRecord,
  deleteEndpointRecord,
  getClaudeRoutingState,
  getModelRoutingSettings,
  isWailsEnvironment,
  setClaudeRoutingOverride,
  subscribeToEvent,
  updateEndpointRecord
} from '@utils/wailsApi.js';

const initialRoutingState = { mode: 'auto', endpointName: '', fallbackEnabled: true };

const ROUTE_MODE_LABELS = { auto: '自动调度', manual_preferred: '手动优选', manual_fixed: '手动固定' };

// embedded：嵌在「Claude 管理」页签内，标题交给外层，操作按钮渲染到 headerTarget；
// active=false（切到其他页签）时暂停快照轮询、收起抽屉与弹窗
const EndpointsPage = ({ embedded = false, active = true, headerTarget = null, onOpenModelRouting }) => {
  const { formatTimestamp } = useTimezone();
  const { endpoints, loading, error, stats, lastUpdate, refresh, testEndpoint, testAllEndpoints, setAvailability, setAutoSchedule, clearCooldown } = useEndpointsData();
  const [routingState, setRoutingState] = useState(initialRoutingState);
  const [modelRoutingSettings, setModelRoutingSettings] = useState({ claude_enabled: false, binding_count: 0 });
  const [snapshot, setSnapshot] = useState({ hasSnapshot: false, decisions: [] });
  const [snapshotUnsupported, setSnapshotUnsupported] = useState(false);
  const [drawerOpen, setDrawerOpen] = useState(false);
  const [editingEndpoint, setEditingEndpoint] = useState(undefined);
  const [formOpen, setFormOpen] = useState(false);
  const [deleteTarget, setDeleteTarget] = useState(null);
  const [busyKey, setBusyKey] = useState('');
  const [actionError, setActionError] = useState('');
  // 检查器当前端点；未选或已被删除时落到第一行
  const [selectedName, setSelectedName] = useState(null);
  const listRef = useRef(null);
  const { contextMenu, openContextMenu } = useContextMenu();

  const loadRouting = useCallback(async () => {
    if (!isWailsEnvironment()) return;
    // 两套路由状态分别容错：模型路由读取失败不能连带丢掉全局路由状态，
    // 否则页面会错误地显示初始的 Auto。任一失败在这里汇总后抛给调用方。
    const [routingResult, modelRoutingResult] = await Promise.allSettled([getClaudeRoutingState(), getModelRoutingSettings()]);
    if (routingResult.status === 'fulfilled') setRoutingState(routingResult.value);
    if (modelRoutingResult.status === 'fulfilled') setModelRoutingSettings(modelRoutingResult.value);
    const failure = [routingResult, modelRoutingResult].find((result) => result.status === 'rejected');
    if (failure) throw toError(failure.reason);
  }, []);

  const loadSnapshot = useCallback(async () => {
    const result = await fetchLatestEndpointScheduleSnapshot();
    setSnapshot(result || { hasSnapshot: false, decisions: [] });
    setSnapshotUnsupported(result?.unsupported === true);
  }, []);

  useEffect(() => {
    loadRouting().catch((loadError) => setActionError(resolveErrorMessage(loadError)));
    loadSnapshot().catch(() => {});
  }, [loadRouting, loadSnapshot]);

  useEffect(() => {
    if (!isWailsEnvironment()) return undefined;
    const unsubscribe = subscribeToEvent('claude-routing:update', (state) => {
      if (state && typeof state === 'object') setRoutingState(state);
    });
    const unsubscribeModelRouting = subscribeToEvent('model-routing:update', (state) => {
      if (state && typeof state === 'object') setModelRoutingSettings(state);
    });
    return () => {
      unsubscribe?.();
      unsubscribeModelRouting?.();
    };
  }, []);

  useEffect(() => {
    if (snapshotUnsupported || !active) return undefined;
    const timer = window.setInterval(() => loadSnapshot().catch(() => {}), 5000);
    return () => window.clearInterval(timer);
  }, [active, loadSnapshot, snapshotUnsupported]);

  const run = useCallback(async (key, operation) => {
    setBusyKey(key);
    setActionError('');
    try {
      const result = await operation();
      await Promise.all([refresh(), loadRouting(), loadSnapshot()]);
      return result;
    } catch (runError) {
      setActionError(resolveErrorMessage(runError, '操作失败'));
      throw runError;
    } finally {
      setBusyKey('');
    }
  }, [loadRouting, loadSnapshot, refresh]);

  const setRouting = (mode, endpointName) => run(`route:${endpointName || 'auto'}`, async () => {
    if (mode === 'auto') return clearClaudeRoutingOverride();
    return setClaudeRoutingOverride({ mode, endpointName, fallbackEnabled: mode !== 'manual_fixed' });
  });

  const saveEndpoint = async (payload) => {
    const name = editingEndpoint?.name || payload.name;
    await run(`save:${name}`, () => editingEndpoint
      ? updateEndpointRecord(editingEndpoint.name, payload)
      : createEndpointRecord(payload));
    setFormOpen(false);
    setEditingEndpoint(undefined);
  };

  const routeMode = routingState.mode || 'auto';
  const routeTarget = routingState.endpointName || routingState.endpoint_name || '';
  const effectiveEndpoint = routingState.lastEffectiveEndpoint || routingState.last_effective_endpoint || '';
  const snapshotOutcome = resolveSnapshotOutcome(snapshot);
  const modelRoutingEnabled = modelRoutingSettings?.claude_enabled === true;
  const routeSummary = modelRoutingEnabled
    ? `模型路由 · ${modelRoutingSettings.binding_count || 0} 个模型`
    : `${ROUTE_MODE_LABELS[routeMode] || '自动调度'}${routeTarget ? ` · ${routeTarget}` : effectiveEndpoint ? ` · 最近命中 ${effectiveEndpoint}` : ''}`;

  const endpointNames = useMemo(() => endpoints.map((endpoint) => endpoint.name), [endpoints]);
  const selectedEndpoint = endpoints.find((endpoint) => endpoint.name === selectedName) || endpoints[0] || null;
  const handleListKeyDown = useListKeyboard({ ids: endpointNames, selectedId: selectedEndpoint?.name ?? null, onSelect: setSelectedName, containerRef: listRef });

  const openEdit = (endpoint) => { setEditingEndpoint(endpoint); setFormOpen(true); };
  const testOne = (name) => run(`test:${name}`, () => testEndpoint(name)).catch(() => {});
  const changeAvailability = (name, enabled) => run(`availability:${name}`, () => setAvailability(name, enabled)).catch(() => {});
  const changeAutoSchedule = (name, enabled) => run(`auto:${name}`, () => setAutoSchedule(name, enabled)).catch(() => {});
  const clearOneCooldown = (name) => run(`cooldown:${name}`, () => clearCooldown(name)).catch(() => {});
  const setRoutingSafely = (mode, name) => setRouting(mode, name).catch(() => {});

  const handleRowContextMenu = (event, endpoint) => {
    setSelectedName(endpoint.name);
    const isTarget = routeTarget === endpoint.name && routeMode !== 'auto';
    const routingLocked = Boolean(busyKey) || !endpoint.availabilityEnabled || modelRoutingEnabled;
    openContextMenu(event, [
      { label: '检测连通性', disabled: Boolean(busyKey), onSelect: () => testOne(endpoint.name) },
      { label: '设为优选', disabled: routingLocked, onSelect: () => setRoutingSafely('manual_preferred', endpoint.name) },
      { label: '固定使用', disabled: routingLocked, onSelect: () => setRoutingSafely('manual_fixed', endpoint.name) },
      isTarget && { label: '恢复自动调度', disabled: Boolean(busyKey), onSelect: () => setRoutingSafely('auto', '') },
      (endpoint.inCooldown || endpoint.cooldownPersistPending) && { label: '解除冷却', disabled: Boolean(busyKey), onSelect: () => clearOneCooldown(endpoint.name) },
      '-',
      { label: '编辑…', onSelect: () => openEdit(endpoint) },
      { label: '复制 URL', onSelect: () => copyTextToClipboard(endpoint.url, 'URL') },
      '-',
      { label: '删除…', danger: true, onSelect: () => setDeleteTarget(endpoint) }
    ]);
  };

  const actions = (
    <div className="flex items-center gap-1">
      <ToolbarButton icon={RefreshCw} title="刷新" onClick={() => run('refresh', refresh)} loading={busyKey === 'refresh'} />
      <ToolbarButton icon={Activity} title="批量测试连通性" onClick={() => run('test:all', testAllEndpoints)} loading={busyKey === 'test:all'} />
      <ToolbarButton icon={Route} title="调度快照" onClick={() => setDrawerOpen(true)} />
      <ToolbarButton icon={Plus} title="新建端点" onClick={() => { setEditingEndpoint(undefined); setFormOpen(true); }} />
    </div>
  );

  if (loading && endpoints.length === 0) return <LoadingSpinner text="加载 Claude 端点..." />;
  if (error && endpoints.length === 0) return <div className="p-5"><ErrorMessage title="Claude 端点加载失败" message={error} onRetry={refresh} /></div>;

  return (
    <div className="motion-page flex min-h-0 min-w-0 flex-1 bg-surface">
      {headerTarget ? (active && createPortal(actions, headerTarget)) : null}

      <div className="flex min-w-0 flex-1 flex-col">
        {/* 独立使用（不嵌在 Claude 管理页）时没有工具栏插槽，操作放在列表上方 */}
        {!headerTarget && !embedded && <div className="flex shrink-0 justify-end border-b border-line px-3 py-2">{actions}</div>}
        {actionError && (
          <div role="alert" className="selectable flex shrink-0 items-center gap-2 border-b border-danger-line bg-danger-soft px-4 py-1.5 text-xs text-danger">
            <AlertTriangle size={13} className="shrink-0" />{actionError}
          </div>
        )}
        {snapshot.hasSnapshot && snapshotOutcome.isAbnormal && (
          <button type="button" onClick={() => setDrawerOpen(true)} className="flex shrink-0 items-center justify-between gap-2 border-b border-warn-line bg-warn-soft px-4 py-1.5 text-left text-xs text-warn">
            <span>最近调度异常：{snapshotOutcome.label}{(snapshot.updatedAt || snapshot.capturedAt) ? ` · ${formatTimestamp(snapshot.updatedAt || snapshot.capturedAt)}` : ''}</span>
            <span className="font-medium">查看明细</span>
          </button>
        )}

        <div ref={listRef} tabIndex={0} onKeyDown={handleListKeyDown} aria-label="Claude 端点列表" className="min-h-0 flex-1 overflow-auto focus:outline-none">
          {endpoints.length === 0 ? (
            <ListEmpty icon={Database} title="尚未配置 Claude 端点" description="点击工具栏的 + 创建端点，之后即可参与自动调度或手动路由" />
          ) : (
            <EndpointsTable
              endpoints={endpoints}
              routingState={routingState}
              selectedName={selectedEndpoint?.name}
              busy={Boolean(busyKey)}
              onSelect={setSelectedName}
              onRowContextMenu={handleRowContextMenu}
              onAvailabilityChange={changeAvailability}
              onAutoScheduleChange={changeAutoSchedule}
            />
          )}
        </div>

        <StatusBar>
          <StatusItem label="端点" value={stats.total} />
          <StatusItem label="可达" value={stats.healthy} />
          <StatusItem label="不可达" value={stats.unhealthy} valueClassName={stats.unhealthy > 0 ? 'text-danger' : 'text-fg-body'} />
          <StatusItem label="未检测" value={stats.unchecked} />
          <StatusItem label="冷却中" value={stats.cooldown} valueClassName={stats.cooldown > 0 ? 'text-warn' : 'text-fg-body'} />
          <StatusSpacer />
          <button type="button" onClick={onOpenModelRouting} className="truncate hover:text-fg" title="打开模型路由页签">{routeSummary}</button>
          {lastUpdate && <StatusItem label="更新于" value={lastUpdate} />}
        </StatusBar>
      </div>

      <EndpointInspector
        endpoint={selectedEndpoint}
        routingState={routingState}
        busy={Boolean(busyKey)}
        modelRoutingEnabled={modelRoutingEnabled}
        onTest={testOne}
        onEdit={openEdit}
        onDelete={setDeleteTarget}
        onAvailabilityChange={changeAvailability}
        onAutoScheduleChange={changeAutoSchedule}
        onClearCooldown={clearOneCooldown}
        onSetRouting={setRoutingSafely}
      />

      {contextMenu}
      <EndpointScheduleDrawer open={active && drawerOpen} onClose={() => setDrawerOpen(false)} snapshot={snapshot} unsupported={snapshotUnsupported} />
      {active && formOpen && <EndpointForm endpoint={editingEndpoint} onSave={saveEndpoint} onCancel={() => { setFormOpen(false); setEditingEndpoint(undefined); }} loading={busyKey.startsWith('save:')} />}
      {active && deleteTarget && <DeleteConfirmDialog endpoint={deleteTarget} loading={busyKey === `delete:${deleteTarget.name}`} onCancel={() => setDeleteTarget(null)} onConfirm={async () => { await run(`delete:${deleteTarget.name}`, () => deleteEndpointRecord(deleteTarget.name)); setDeleteTarget(null); }} />}
    </div>
  );
};

export default EndpointsPage;
