import { useMemo, useRef } from 'react';
import { createPortal } from 'react-dom';
import { AlertTriangle, Plus, RefreshCw, Route } from 'lucide-react';
import { LoadingSpinner } from '@components/ui';
import { ToolbarButton } from '@components/layout/ToolbarButton.jsx';
import ToolbarSearch from '@components/layout/ToolbarSearch.jsx';
import { Inspector, InspectorBody, InspectorHeader } from '@components/desktop/Inspector.jsx';
import { StatusBar, StatusItem, StatusSpacer } from '@components/desktop/StatusBar.jsx';
import ListEmpty from '@components/desktop/ListEmpty.jsx';
import Switch from '@components/desktop/Switch.jsx';
import SegmentedControl from '@components/desktop/SegmentedControl.jsx';
import useListKeyboard from '@components/desktop/useListKeyboard.js';
import useConfirm from '@hooks/useConfirm.js';
import { resolveErrorMessage, toError } from '@utils/errors.js';
import { isWailsEnvironment, subscribeToEvent } from '@utils/wailsApi.js';
import { ROUTING_FILTERS, routingCounts } from './routingView.js';
import { useRoutingData } from './useRoutingData.js';
import { useBindingActions } from './useBindingActions.js';
import { useRoutingWorkspace } from './useRoutingWorkspace.js';

const RoutingToolbar = ({ query, setQuery, data, actions, disabled }) => (
  <div className="flex items-center gap-1">
    <ToolbarSearch value={query} onChange={setQuery} placeholder="筛选模型" width={180} />
    <ToolbarButton icon={RefreshCw} title="刷新模型路由" onClick={data.load} loading={data.loading} disabled={actions.busy || !data.ready} />
    <ToolbarButton icon={Plus} title="添加映射" onClick={() => actions.setEditor({})} disabled={disabled} />
  </div>
);

const RoutingSwitchBar = ({ data, filter, setFilter, disabled, onToggle, config }) => (
  <div className="flex shrink-0 flex-wrap items-center gap-x-3 gap-y-1.5 border-b border-line bg-inspector px-3 py-1.5">
    <Switch checked={data.settings?.[config.settingsField] === true} label={config.switchLabel} disabled={disabled} onChange={onToggle} />
    <span className="text-[12.5px] text-fg">启用模型路由</span>
    <span className="text-[11.5px] text-fg-muted">
      {!data.settings ? '路由状态尚未加载' : data.settings[config.settingsField] ? config.enabledMessage : config.disabledMessage}
    </span>
    <div className="ml-auto"><SegmentedControl options={ROUTING_FILTERS} value={filter} onChange={setFilter} ariaLabel="映射筛选" /></div>
  </div>
);

const RoutingNotices = ({ data, config }) => {
  const ExtraNotices = config.ExtraNotices;
  return (
    <>
      <ExtraNotices data={data} />
      {data.error && (
        <div role="alert" className="selectable flex shrink-0 items-start gap-2 border-b border-danger-line bg-danger-soft px-4 py-2 text-xs text-danger">
          <AlertTriangle size={14} className="shrink-0" /><span className="min-w-0 flex-1">{data.error}</span>
          <button type="button" onClick={data.load} className="shrink-0 underline">重试</button>
        </div>
      )}
    </>
  );
};

const RoutingList = ({ data, rows, selectedId, memberProps, onSelect, onPatch, onRowContextMenu, disabled, config }) => {
  const listRef = useRef(null);
  const ids = useMemo(() => rows.map((binding) => binding.assignment_id), [rows]);
  const handleListKeyDown = useListKeyboard({ ids, selectedId, onSelect, containerRef: listRef });
  const Table = config.Table;
  return (
    <div ref={listRef} tabIndex={0} onKeyDown={handleListKeyDown} aria-label={config.listLabel} className="@container min-h-0 flex-1 overflow-auto focus:outline-none">
      {!data.ready ? <ListEmpty title="桌面环境中可编辑模型路由" />
        : data.loading && !data.settings ? <LoadingSpinner text="加载模型路由..." />
          : rows.length > 0 ? <Table {...memberProps} bindings={rows} selectedId={selectedId} busy={disabled} onSelect={onSelect} onPatch={onPatch} onRowContextMenu={onRowContextMenu} />
            : <ListEmpty icon={Route} title={data.readFailed ? '模型路由加载失败' : data.bindings.length ? '没有匹配的映射' : '还没有模型映射'} description={data.readFailed ? '点击刷新重试。' : config.emptyDescription} />}
    </div>
  );
};

const RoutingStatus = ({ data, rows, query, filter, busy }) => {
  const counts = routingCounts(data.bindings);
  return (
    <StatusBar>
      <StatusItem label="映射" value={counts.total} /><StatusItem label="已启用" value={counts.enabled} /><StatusItem label="已停用" value={counts.disabled} />
      {(query.trim() || filter !== 'all') && <StatusItem label="筛选后" value={rows.length} />}
      <StatusSpacer />{busy && <StatusItem value="保存中…" />}
    </StatusBar>
  );
};

const testRouting = async (api, model) => {
  try { return await api.testRouting(model); }
  catch (error) { throw toError(error, resolveErrorMessage(error, '路由试算失败')); }
};

const RoutingInspector = ({ selected, data, memberProps, disabled, actions, onDelete, onClearSelection, config }) => {
  const Details = config.Inspector;
  const TestBox = config.TestBox;
  return selected ? (
    <Details {...memberProps} binding={selected} busy={disabled} onEdit={actions.setEditor} onDelete={onDelete} onClearSelection={onClearSelection} />
  ) : (
    <Inspector label={config.testLabel}>
      <InspectorHeader title="模型路由" subtitle="选择映射可查看成员与最近命中" />
      <InspectorBody>{data.ready ? <TestBox presentation="inspector" onTest={(model) => testRouting(config.api, model)} /> : <ListEmpty title="路由试算需要桌面环境" />}</InspectorBody>
    </Inspector>
  );
};

const RoutingEditor = ({ data, actions, config }) => {
  const Editor = config.Editor;
  const memberProps = { [config.editorMembersProp]: data.members };
  return (
    <Editor key={actions.editor.assignment_id ?? 'new'} {...memberProps}
      binding={actions.editor.assignment_id ? actions.editor : null} bindings={data.bindings}
      busy={actions.busy} onSave={actions.save} onClose={() => actions.setEditor(null)} />
  );
};

const ModelRoutingWorkspace = ({ active = true, headerTarget = null, config }) => {
  const data = useRoutingData({ active, ready: isWailsEnvironment(), fetchData: config.api.fetchData, subscribe: subscribeToEvent });
  const actions = useBindingActions({ data, createBinding: config.api.createBinding, updateBinding: config.api.updateBinding, buildPatch: config.buildPatch });
  const { confirm, confirmDialog } = useConfirm();
  const view = useRoutingWorkspace({ data, actions, confirm, config });
  const memberProps = useMemo(() => config.memberProps(data.members), [config, data.members]);
  const toolbar = <RoutingToolbar {...view} data={data} actions={actions} />;
  return (
    <div className="motion-page flex min-h-0 min-w-0 flex-1 bg-surface">
      {headerTarget && active && createPortal(toolbar, headerTarget)}
      <div className="flex min-w-0 flex-1 flex-col">
        {!headerTarget && <div className="flex shrink-0 justify-end border-b border-line px-3 py-2">{toolbar}</div>}
        <RoutingSwitchBar {...view} data={data} config={config} />
        <RoutingNotices data={data} config={config} />
        <RoutingList {...view} data={data} config={config} memberProps={memberProps} selectedId={view.selected?.assignment_id ?? null} onSelect={view.setSelectedId} onPatch={actions.patch} />
        <RoutingStatus {...view} data={data} busy={actions.busy} />
      </div>
      <RoutingInspector {...view} data={data} config={config} memberProps={memberProps} actions={actions} onClearSelection={() => view.setSelectedId(null)} />
      {active && view.contextMenu}
      {active && confirmDialog}
      {active && actions.editor && <RoutingEditor data={data} actions={actions} config={config} />}
    </div>
  );
};

export default ModelRoutingWorkspace;
