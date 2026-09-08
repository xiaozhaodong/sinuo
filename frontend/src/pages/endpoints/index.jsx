import { useCallback, useEffect, useMemo, useState } from 'react';
import { Activity, AlertTriangle, Boxes, Database, Info, Plus, RefreshCw, Route, Split } from 'lucide-react';
import { Button, ErrorMessage, LoadingSpinner } from '@components/ui';
import useEndpointsData from '@hooks/useEndpointsData.js';
import { DeleteConfirmDialog, EndpointCard, EndpointForm, EndpointRow, EndpointScheduleDrawer, ViewModeSwitcher, resolveSnapshotOutcome } from './components';
import { fetchLatestEndpointScheduleSnapshot } from '@utils/endpointScheduleApi.js';
import { resolveErrorMessage, toError } from '@utils/errors.js';
import { useTimezone } from '@contexts/TimezoneContext.jsx';
import ClaudeModelsPanel from './components/ClaudeModelsPanel.jsx';
import ModelRoutingDrawer from './components/model-routing/ModelRoutingDrawer.jsx';
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

const VIEW_MODE_STORAGE_KEY = 'endpoints.viewMode';
const readStoredViewMode = () => {
  try {
    return window.localStorage.getItem(VIEW_MODE_STORAGE_KEY) === 'grid' ? 'grid' : 'table';
  } catch {
    return 'table';
  }
};

const EndpointsPage = () => {
  const { formatTimestamp } = useTimezone();
  const { endpoints, loading, error, stats, lastUpdate, refresh, testEndpoint, testAllEndpoints, setAvailability, setAutoSchedule, clearCooldown } = useEndpointsData();
  const [routingState, setRoutingState] = useState(initialRoutingState);
  const [modelRoutingSettings, setModelRoutingSettings] = useState({ claude_enabled: false, binding_count: 0 });
  const [snapshot, setSnapshot] = useState({ hasSnapshot: false, decisions: [] });
  const [snapshotUnsupported, setSnapshotUnsupported] = useState(false);
  const [drawerOpen, setDrawerOpen] = useState(false);
  const [modelsOpen, setModelsOpen] = useState(false);
  const [routingOpen, setRoutingOpen] = useState(false);
  const [editingEndpoint, setEditingEndpoint] = useState(undefined);
  const [formOpen, setFormOpen] = useState(false);
  const [deleteTarget, setDeleteTarget] = useState(null);
  const [busyKey, setBusyKey] = useState('');
  const [actionError, setActionError] = useState('');
  const [viewMode, setViewMode] = useState(readStoredViewMode);

  const changeViewMode = (mode) => {
    setViewMode(mode);
    try {
      window.localStorage.setItem(VIEW_MODE_STORAGE_KEY, mode);
    } catch { /* 隐私模式等场景下写不进去就只在内存里生效 */ }
  };

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
    if (snapshotUnsupported) return undefined;
    const timer = window.setInterval(() => loadSnapshot().catch(() => {}), 5000);
    return () => window.clearInterval(timer);
  }, [loadSnapshot, snapshotUnsupported]);

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
  const statsCards = useMemo(() => [
    ['端点总数', stats.total, 'text-fg'],
    ['最近可达', stats.healthy, 'text-success'],
    ['最近不可达', stats.unhealthy, 'text-danger'],
    ['未检测', stats.unchecked, 'text-fg-subtle'],
    ['冷却中', stats.cooldown, 'text-warn']
  ], [stats]);

  if (loading && endpoints.length === 0) return <LoadingSpinner text="加载 Claude 端点..." />;
  if (error && endpoints.length === 0) return <ErrorMessage title="Claude 端点加载失败" message={error} onRetry={refresh} />;

  return (
    <div className="animate-fade-in space-y-6">
      <div className="flex flex-col gap-4 xl:flex-row xl:items-end xl:justify-between">
        <div>
          <div className="flex items-center gap-2 text-xs font-semibold uppercase tracking-[0.16em] text-accent"><Database size={14} />SQLite 权威源</div>
          <h1 className="mt-1 text-2xl font-bold text-fg">Claude 端点</h1>
          <p className="mt-1 text-sm text-fg-muted">扁平管理固定认证、调度资格、健康状态与模型兼容规则。{lastUpdate && <span className="text-fg-subtle"> · 更新于 {lastUpdate}</span>}</p>
        </div>
        <div className="flex flex-wrap items-center gap-2">
          <Button variant="ghost" size="sm" icon={RefreshCw} onClick={() => run('refresh', refresh)} loading={busyKey === 'refresh'}>刷新</Button>
          <Button variant="secondary" icon={Activity} onClick={() => run('test:all', testAllEndpoints)} loading={busyKey === 'test:all'}>批量测试</Button>
          <Button variant="secondary" icon={Route} onClick={() => setDrawerOpen(true)}>调度快照</Button>
          <Button variant="secondary" icon={Boxes} onClick={() => setModelsOpen(true)}>模型目录</Button>
          <Button variant="secondary" icon={Split} onClick={() => setRoutingOpen(true)}>模型路由</Button>
          <Button icon={Plus} onClick={() => { setEditingEndpoint(undefined); setFormOpen(true); }}>新建端点</Button>
        </div>
      </div>

      <div className="grid gap-3 sm:grid-cols-2 xl:grid-cols-6">
        <button
          type="button"
          onClick={() => setRoutingOpen(true)}
          title="打开模型路由"
          className="rounded-xl border border-accent-line bg-accent-soft/60 p-4 text-left transition-colors hover:bg-accent-soft sm:col-span-2 xl:col-span-1"
        >
          <div className="text-xs text-accent">{modelRoutingSettings?.claude_enabled ? '模型路由' : 'Claude 路由'}</div>
          <div className="mt-1 truncate font-semibold text-accent-fg">{modelRoutingSettings?.claude_enabled ? `${modelRoutingSettings.binding_count || 0} 个模型` : routeMode === 'auto' ? '自动调度' : routeMode === 'manual_fixed' ? '手动固定' : '手动优选'}</div>
          <div className="mt-1 truncate text-xs text-accent" title={routeTarget || effectiveEndpoint}>{modelRoutingSettings?.claude_enabled ? '按请求模型选择端点' : routeTarget || effectiveEndpoint || '等待请求命中'}</div>
        </button>
        {statsCards.map(([label, value, color]) => (
          <div key={label} className="rounded-xl border border-line bg-surface p-4 shadow-sm"><div className={`text-2xl font-bold ${color}`}>{value}</div><div className="mt-1 text-xs text-fg-muted">{label}</div></div>
        ))}
      </div>

      <div className="flex items-start gap-2 rounded-xl border border-line bg-surface-sub px-3 py-2.5 text-xs leading-5 text-fg-muted"><Info size={15} className="mt-0.5 shrink-0" /><span>“可达 / 不可达”只表示最近一次主动检测；真实请求仍根据硬启用、自动调度资格、请求级失败与冷却状态选择候选。</span></div>
      {actionError && <div className="tone-rose flex items-center gap-2 rounded-xl border px-3 py-2 text-sm"><AlertTriangle size={16} />{actionError}</div>}
      {snapshot.hasSnapshot && snapshotOutcome.isAbnormal && <button type="button" onClick={() => setDrawerOpen(true)} className="tone-amber flex w-full items-center justify-between rounded-xl border px-3 py-2 text-left text-xs"><span>最近调度异常：{snapshotOutcome.label}{(snapshot.updatedAt || snapshot.capturedAt) ? ` · ${formatTimestamp(snapshot.updatedAt || snapshot.capturedAt)}` : ''}</span><span className="font-medium">查看明细</span></button>}

      <div className="overflow-hidden rounded-2xl border border-line bg-surface shadow-sm">
        {endpoints.length === 0 ? (
          <div className="px-6 py-16 text-center"><Database size={38} className="mx-auto text-fg-subtle/60" /><div className="mt-3 font-medium text-fg-body">尚未配置 Claude 端点</div><div className="mt-1 text-sm text-fg-subtle">创建端点后即可参与自动调度或手动路由。</div></div>
        ) : (
          <>
            <div className="flex items-center justify-between gap-2 border-b border-line-soft px-3 py-2">
              <span className="text-xs text-fg-subtle">共 {endpoints.length} 个端点</span>
              <ViewModeSwitcher value={viewMode} onChange={changeViewMode} />
            </div>
            {viewMode === 'table' ? (
              <div className="overflow-x-auto">
                <table className="w-full min-w-[1120px] text-left">
                  <thead className="border-b border-line bg-surface-sub text-[11px] font-semibold uppercase tracking-wider text-fg-subtle"><tr>{['名称 / URL', '认证', '优先级', '硬启用', '自动调度', '连通性', '冷却', '模型改写', '操作'].map((label) => <th key={label} className="px-3 py-3">{label}</th>)}</tr></thead>
                  <tbody className="divide-y divide-line-soft">
                    {endpoints.map((endpoint) => (
                      <EndpointRow
                        key={endpoint.id || endpoint.name}
                        endpoint={endpoint}
                        routingState={routingState}
                        busy={Boolean(busyKey)}
                        onEdit={(value) => { setEditingEndpoint(value); setFormOpen(true); }}
                        onDelete={setDeleteTarget}
                        onTest={(name) => run(`test:${name}`, () => testEndpoint(name))}
                        onAvailabilityChange={(name, enabled) => run(`availability:${name}`, () => setAvailability(name, enabled))}
                        onAutoScheduleChange={(name, enabled) => run(`auto:${name}`, () => setAutoSchedule(name, enabled))}
                        onClearCooldown={(name) => run(`cooldown:${name}`, () => clearCooldown(name))}
                        onSetRouting={setRouting}
                        modelRoutingEnabled={modelRoutingSettings?.claude_enabled === true}
                      />
                    ))}
                  </tbody>
                </table>
              </div>
            ) : (
              <div className="grid gap-4 p-4 sm:grid-cols-2 xl:grid-cols-3">
                {endpoints.map((endpoint) => (
                  <EndpointCard
                    key={endpoint.id || endpoint.name}
                    endpoint={endpoint}
                    routingState={routingState}
                    busy={Boolean(busyKey)}
                    onEdit={(value) => { setEditingEndpoint(value); setFormOpen(true); }}
                    onDelete={setDeleteTarget}
                    onTest={(name) => run(`test:${name}`, () => testEndpoint(name))}
                    onAvailabilityChange={(name, enabled) => run(`availability:${name}`, () => setAvailability(name, enabled))}
                    onAutoScheduleChange={(name, enabled) => run(`auto:${name}`, () => setAutoSchedule(name, enabled))}
                    onClearCooldown={(name) => run(`cooldown:${name}`, () => clearCooldown(name))}
                    onSetRouting={setRouting}
                    modelRoutingEnabled={modelRoutingSettings?.claude_enabled === true}
                  />
                ))}
              </div>
            )}
          </>
        )}
      </div>

      <EndpointScheduleDrawer open={drawerOpen} onClose={() => setDrawerOpen(false)} snapshot={snapshot} unsupported={snapshotUnsupported} />
      <ClaudeModelsPanel open={modelsOpen} onClose={() => setModelsOpen(false)} />
      <ModelRoutingDrawer open={routingOpen} onClose={() => { setRoutingOpen(false); loadRouting().catch(() => {}); }} endpoints={endpoints} />
      {formOpen && <EndpointForm endpoint={editingEndpoint} onSave={saveEndpoint} onCancel={() => { setFormOpen(false); setEditingEndpoint(undefined); }} loading={busyKey.startsWith('save:')} />}
      {deleteTarget && <DeleteConfirmDialog endpoint={deleteTarget} loading={busyKey === `delete:${deleteTarget.name}`} onCancel={() => setDeleteTarget(null)} onConfirm={async () => { await run(`delete:${deleteTarget.name}`, () => deleteEndpointRecord(deleteTarget.name)); setDeleteTarget(null); }} />}
    </div>
  );
};

export default EndpointsPage;
