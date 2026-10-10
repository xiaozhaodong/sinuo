import { useCallback, useEffect, useRef, useState } from 'react';
import { createPortal } from 'react-dom';
import { Activity, AlertTriangle, RefreshCw } from 'lucide-react';
import { LoadingSpinner } from '@components/ui';
import { ToolbarButton } from '@components/layout/ToolbarButton.jsx';
import ListEmpty from '@components/desktop/ListEmpty.jsx';
import { StatusBar, StatusItem, StatusSpacer } from '@components/desktop/StatusBar.jsx';
import { useTimezone } from '@contexts/TimezoneContext.jsx';
import { fetchLatestEndpointScheduleSnapshot } from '@utils/endpointScheduleApi.js';
import { resolveErrorMessage } from '@utils/errors.js';
import EndpointDecisionsTable from './EndpointDecisionsTable.jsx';
import ClaudeScheduleInspector from './ClaudeScheduleInspector.jsx';
import { countDecisions } from './scheduleSnapshotView.js';

const useEndpointSnapshot = (active) => {
  const [snapshot, setSnapshot] = useState({ hasSnapshot: false, decisions: [] });
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const mounted = useRef(false);
  const pending = useRef(false);
  const unsupported = snapshot.unsupported === true;

  const loadSnapshot = useCallback(async ({ silent = false } = {}) => {
    if (pending.current) return;
    pending.current = true;
    if (!silent) setLoading(true);
    try {
      const result = await fetchLatestEndpointScheduleSnapshot();
      if (!mounted.current) return;
      setSnapshot(result);
      setError('');
    } catch (loadError) {
      if (mounted.current) setError(resolveErrorMessage(loadError, '端点调度快照加载失败'));
    } finally {
      pending.current = false;
      if (!silent && mounted.current) setLoading(false);
    }
  }, []);

  useEffect(() => {
    mounted.current = true;
    // 首次挂载立即加载；卸载后不再写回异步结果。
    loadSnapshot();
    return () => { mounted.current = false; };
  }, [loadSnapshot]);

  useEffect(() => {
    if (!active || unsupported) return undefined;
    const timer = window.setInterval(() => loadSnapshot({ silent: true }), 5000);
    return () => window.clearInterval(timer);
  }, [active, loadSnapshot, unsupported]);

  return { snapshot, loading, error, unsupported, loadSnapshot };
};

const SnapshotContent = ({ snapshot, loading, error, unsupported }) => {
  if (unsupported) return <ListEmpty title={snapshot.message || '当前运行版本暂不支持端点调度快照。'} />;
  if (loading && !snapshot.hasSnapshot && !error) return <LoadingSpinner text="加载端点调度快照..." />;
  if (!snapshot.hasSnapshot) {
    return <ListEmpty icon={Activity} title="暂无端点调度记录" description="发起一次 Claude `/v1/messages` 请求后，这里会立即显示本次请求为何选择或跳过每个端点。" />;
  }
  if (snapshot.decisions.length === 0) return <ListEmpty title="本次请求未生成端点候选。" />;
  return <EndpointDecisionsTable decisions={snapshot.decisions} />;
};

const SnapshotStatus = ({ snapshot, unsupported }) => {
  const { formatListTimestamp } = useTimezone();
  const counts = countDecisions(snapshot.decisions);
  const updatedAt = snapshot.updatedAt || snapshot.capturedAt;
  return (
    <StatusBar>
      <StatusItem label="候选" value={counts.candidates} />
      <StatusItem label="跳过" value={counts.skipped} />
      {snapshot.selectedEndpoint && <StatusItem label="命中" value={snapshot.selectedEndpoint} />}
      <StatusSpacer />
      {!unsupported && <StatusItem label="5s 自动刷新" />}
      <StatusItem label="更新于" value={updatedAt ? formatListTimestamp(updatedAt) : '—'} />
    </StatusBar>
  );
};

const ClaudeSchedulePage = ({ active = true, headerTarget = null }) => {
  const { snapshot, loading, error, unsupported, loadSnapshot } = useEndpointSnapshot(active);
  const actions = <ToolbarButton icon={RefreshCw} title="刷新" onClick={() => loadSnapshot()} loading={loading} />;
  return (
    <div className="motion-page flex min-h-0 min-w-0 flex-1 bg-surface">
      {headerTarget && active && createPortal(actions, headerTarget)}
      <div className="flex min-w-0 flex-1 flex-col">
        {!headerTarget && active && <div className="flex shrink-0 justify-end border-b border-line px-3 py-2">{actions}</div>}
        {error && (
          <div role="alert" className="selectable flex shrink-0 items-center gap-2 border-b border-danger-line bg-danger-soft px-4 py-1.5 text-xs text-danger">
            <AlertTriangle size={13} className="shrink-0" />{error}
          </div>
        )}
        <div className="min-h-0 flex-1 overflow-auto">
          <SnapshotContent snapshot={snapshot} loading={loading} error={error} unsupported={unsupported} />
        </div>
        <SnapshotStatus snapshot={snapshot} unsupported={unsupported} />
      </div>
      <ClaudeScheduleInspector snapshot={snapshot} unsupported={unsupported} />
    </div>
  );
};

export default ClaudeSchedulePage;
