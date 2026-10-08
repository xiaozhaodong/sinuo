import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { createPortal } from 'react-dom';
import { AlertCircle, Boxes, Info, Plus, RefreshCw, X } from 'lucide-react';
import { ErrorMessage, LoadingSpinner } from '@components/ui';
import { ToolbarButton } from '@components/layout/ToolbarButton.jsx';
import ToolbarSearch from '@components/layout/ToolbarSearch.jsx';
import { Inspector, InspectorEmpty } from '@components/desktop/Inspector.jsx';
import { StatusBar, StatusItem, StatusSpacer } from '@components/desktop/StatusBar.jsx';
import useContextMenu from '@components/desktop/useContextMenu.jsx';
import useListKeyboard from '@components/desktop/useListKeyboard.js';
import ListEmpty from '@components/desktop/ListEmpty.jsx';
import Switch from '@components/desktop/Switch.jsx';
import SegmentedControl from '@components/desktop/SegmentedControl.jsx';
import useConfirm from '@hooks/useConfirm.js';
import { copyTextToClipboard } from '@pages/requests/components/clipboard.js';
import { resolveErrorMessage } from '@utils/errors.js';
import { getSettingsByCategory, isWailsEnvironment, updateSetting } from '@utils/wailsApi.js';
import ClaudeModelsTable from './ClaudeModelsTable.jsx';
import ClaudeModelInspector from './ClaudeModelInspector.jsx';
import {
  CLAUDE_MODELS_CATEGORY,
  CLAUDE_MODELS_KEY,
  MAX_CLAUDE_MODELS,
  filterClaudeModels,
  normalizeClaudeCatalogForSave,
  parseClaudeCatalog,
  removeClaudeModel,
  setClaudeModelEnabled,
  upsertClaudeModel
} from './claudeModelCatalog.js';

const NEW_SELECTION = '__new__';
const DISCARD_CONFIRM = {
  title: '放弃未保存的修改？',
  message: '当前模型有未保存的修改，继续操作后将丢失。',
  confirmText: '放弃修改',
  danger: true
};
const FILTER_OPTIONS = [
  { value: 'all', label: '全部' },
  { value: 'enabled', label: '已启用' },
  { value: 'disabled', label: '已停用' }
];
const CATALOG_INFO = '独立的展示目录：维护 Claude Code /model 中的展示项，模型 ID 不限于 Anthropic 官方模型；不检测端点是否支持模型，也不改变模型改写、端点绑定或调度。客户端需应用完整的 Gateway Discovery 补丁：补丁会在配置 Base URL 与认证后自动发现模型，并仅让 /v1/models 绕过非必要流量限制，无需再设置 discovery 环境变量。';

const ClaudeModelsPage = ({ active = true, headerTarget = null }) => {
  const environmentReady = isWailsEnvironment();
  const [catalog, setCatalog] = useState(() => parseClaudeCatalog(null));
  const [loading, setLoading] = useState(environmentReady);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState(null);
  const [actionError, setActionError] = useState(null);
  const [revision, setRevision] = useState(0);
  const [query, setQuery] = useState('');
  const [filter, setFilter] = useState('all');
  const [selectedId, setSelectedId] = useState(null);
  const dirtyRef = useRef(false);
  const listRef = useRef(null);
  const { confirm, confirmDialog } = useConfirm();
  const { contextMenu, openContextMenu } = useContextMenu();

  const loadCatalog = useCallback(async () => {
    if (!environmentReady) return;
    setLoading(true);
    setError(null);
    try {
      const settings = await getSettingsByCategory(CLAUDE_MODELS_CATEGORY);
      const record = settings.find((setting) => setting.key === CLAUDE_MODELS_KEY);
      const next = parseClaudeCatalog(record?.value);
      setCatalog(next);
      setRevision((value) => value + 1);
    } catch (loadError) {
      setError(resolveErrorMessage(loadError, '加载 Claude 模型目录失败'));
    } finally {
      setLoading(false);
    }
  }, [environmentReady]);

  useEffect(() => {
    loadCatalog();
  }, [loadCatalog]);

  const rows = useMemo(() => filterClaudeModels(catalog.models, { query, filter }), [catalog.models, filter, query]);
  const rowIds = useMemo(() => rows.map((model) => model.id), [rows]);
  const isNew = selectedId === NEW_SELECTION;
  const effectiveId = isNew || catalog.models.some((model) => model.id === selectedId)
    ? selectedId
    : rows[0]?.id || null;
  const selected = isNew ? null : catalog.models.find((model) => model.id === effectiveId) || null;
  const enabledCount = catalog.models.filter((model) => model.enabled).length;

  const handleDirtyChange = useCallback((value) => {
    dirtyRef.current = value;
  }, []);

  const confirmDiscard = useCallback(async () => {
    if (!dirtyRef.current) return true;
    const discarded = await confirm(DISCARD_CONFIRM);
    if (discarded) dirtyRef.current = false;
    return discarded;
  }, [confirm]);

  const requestSelect = useCallback(async (nextId) => {
    if (nextId === effectiveId || !(await confirmDiscard())) return;
    setSelectedId(nextId);
  }, [confirmDiscard, effectiveId]);

  const handleListKeyDown = useListKeyboard({ ids: rowIds, selectedId: effectiveId, onSelect: requestSelect, containerRef: listRef });

  const persistCatalog = useCallback(async (buildNext) => {
    setSaving(true);
    try {
      const normalized = normalizeClaudeCatalogForSave(buildNext(catalog));
      await updateSetting(CLAUDE_MODELS_CATEGORY, CLAUDE_MODELS_KEY, JSON.stringify(normalized));
      setCatalog(parseClaudeCatalog(normalized));
      setRevision((value) => value + 1);
      dirtyRef.current = false;
      return normalized;
    } finally {
      setSaving(false);
    }
  }, [catalog]);

  const runCatalogAction = useCallback(async (buildNext) => {
    if (!(await confirmDiscard())) return null;
    setActionError(null);
    try {
      return await persistCatalog(buildNext);
    } catch (actionFailure) {
      setActionError(resolveErrorMessage(actionFailure, '保存模型目录失败'));
      return null;
    }
  }, [confirmDiscard, persistCatalog]);

  const requestRefresh = async () => {
    if (!(await confirmDiscard())) return;
    setActionError(null);
    await loadCatalog();
  };

  const toggleEnabled = (id, enabled) => runCatalogAction((current) => ({
    ...current,
    models: setClaudeModelEnabled(current.models, id, enabled)
  }));

  const toggleEnabledDirectory = (enabled) => runCatalogAction((current) => ({ ...current, enabled }));

  const handleSaveInspector = async (draft, originalId) => {
    const result = await persistCatalog((current) => ({
      ...current,
      models: upsertClaudeModel(current.models, {
        id: draft.id.trim(),
        display_name: draft.display_name.trim(),
        enabled: originalId ? current.models.find((model) => model.id === originalId)?.enabled !== false : true
      }, originalId)
    }));
    setSelectedId(draft.id.trim());
    return result;
  };

  const handleAdd = async () => {
    if (!environmentReady || catalog.models.length >= MAX_CLAUDE_MODELS || !(await confirmDiscard())) return;
    setActionError(null);
    setSelectedId(NEW_SELECTION);
  };

  const handleDelete = async (model) => {
    if (!(await confirmDiscard())) return;
    const confirmed = await confirm({
      title: '删除模型',
      message: `确定从目录中删除「${model.id}」吗？`,
      confirmText: '删除',
      danger: true
    });
    if (!confirmed) return;
    const index = rows.findIndex((row) => row.id === model.id);
    const nextId = rows[index + 1]?.id || rows[index - 1]?.id || null;
    const result = await runCatalogAction((current) => ({ ...current, models: removeClaudeModel(current.models, model.id) }));
    if (result) setSelectedId(nextId);
  };

  const handleRowContextMenu = (event, model) => {
    if (!dirtyRef.current) setSelectedId(model.id);
    openContextMenu(event, [
      { label: model.enabled ? '停用' : '启用', disabled: saving, onSelect: () => toggleEnabled(model.id, !model.enabled) },
      { label: '复制模型 ID', disabled: saving, onSelect: () => copyTextToClipboard(model.id, '模型 ID') },
      '-',
      { label: '删除…', danger: true, disabled: saving, onSelect: () => handleDelete(model) }
    ]);
  };

  const actions = (
    <div className="flex items-center gap-1">
      <ToolbarSearch value={query} onChange={setQuery} placeholder="筛选模型" width={180} />
      <ToolbarButton icon={RefreshCw} title="重新载入" onClick={requestRefresh} loading={loading} disabled={saving || !environmentReady} />
      <ToolbarButton
        icon={Plus}
        title={catalog.models.length >= MAX_CLAUDE_MODELS ? '已达到模型条目上限（1000）' : '添加模型'}
        onClick={handleAdd}
        disabled={saving || !environmentReady || catalog.models.length >= MAX_CLAUDE_MODELS}
      />
    </div>
  );

  if (error && catalog.models.length === 0) {
    return (
      <div className="motion-page flex min-h-0 min-w-0 flex-1 flex-col bg-surface">
        {headerTarget && active && createPortal(actions, headerTarget)}
        {!headerTarget && <div className="flex shrink-0 justify-end border-b border-line px-3 py-2">{actions}</div>}
        <div className="p-5"><ErrorMessage title="Claude 模型目录加载失败" message={error} onRetry={loadCatalog} /></div>
      </div>
    );
  }
  if (loading && catalog.models.length === 0) return <LoadingSpinner text="加载 Claude 模型目录..." />;

  return (
    <div className="motion-page flex min-h-0 min-w-0 flex-1 bg-surface">
      {headerTarget && active && createPortal(actions, headerTarget)}
      <div className="flex min-w-0 flex-1 flex-col">
        {!headerTarget && <div className="flex shrink-0 justify-end border-b border-line px-3 py-2">{actions}</div>}
        <div className="flex shrink-0 flex-wrap items-center gap-x-3 gap-y-1.5 border-b border-line bg-inspector px-3 py-1.5">
          <Switch
            checked={catalog.enabled}
            label="启用本地模型目录"
            disabled={saving || !environmentReady}
            onChange={(enabled) => toggleEnabledDirectory(enabled)}
          />
          <span className="text-[12.5px] text-fg">启用本地模型目录</span>
          <span className="text-[11.5px] text-fg-muted">关闭时 Claude /v1/models 返回空列表</span>
          <div className="ml-auto flex items-center gap-2">
            <SegmentedControl options={FILTER_OPTIONS} value={filter} onChange={setFilter} ariaLabel="模型筛选" disabled={saving} />
            <button type="button" aria-label="目录说明" title={CATALOG_INFO} className="inline-flex h-6 w-6 items-center justify-center rounded text-fg-muted hover:bg-surface-mut hover:text-fg">
              <Info size={14} />
            </button>
          </div>
        </div>
        {(actionError || error) && (
          <div role="alert" className="selectable flex shrink-0 items-center gap-2 border-b border-danger-line bg-danger-soft px-4 py-1.5 text-xs text-danger">
            <AlertCircle size={13} className="shrink-0" />
            <span className="min-w-0 flex-1">{actionError || error}</span>
            <button type="button" onClick={() => { setActionError(null); setError(null); }} aria-label="关闭提示" className="shrink-0 rounded p-0.5 hover:bg-danger-soft"><X size={13} /></button>
          </div>
        )}
        <div ref={listRef} tabIndex={0} onKeyDown={handleListKeyDown} aria-label="Claude 模型列表" className="min-h-0 flex-1 overflow-auto focus:outline-none">
          {!environmentReady ? (
            <ListEmpty title="桌面环境中可编辑本地模型目录" />
          ) : rows.length > 0 ? (
            <ClaudeModelsTable models={rows} selectedId={effectiveId} saving={saving} onSelect={requestSelect} onToggleEnabled={toggleEnabled} onRowContextMenu={handleRowContextMenu} />
          ) : catalog.models.length > 0 ? (
            <ListEmpty title="没有匹配的模型" />
          ) : (
            <ListEmpty icon={Boxes} title="目录还是空的" description="点击工具栏的 + 添加 Gateway 返回给 Claude Code 的模型 ID" />
          )}
        </div>
        <StatusBar>
          <StatusItem label="模型" value={catalog.models.length} />
          <StatusItem label="启用" value={enabledCount} />
          {(query || filter !== 'all') && <StatusItem label="筛选后" value={rows.length} />}
          {!catalog.enabled && <StatusItem label="目录" value="已关闭" valueClassName="text-warn" />}
          <StatusSpacer />
          {saving && <StatusItem label="" value="保存中…" />}
        </StatusBar>
      </div>
      {isNew || selected ? (
        <ClaudeModelInspector
          key={isNew ? NEW_SELECTION : `${selected.id}:${revision}`}
          model={selected}
          isNew={isNew}
          saving={saving}
          models={catalog.models}
          onSave={handleSaveInspector}
          onCancelNew={() => { dirtyRef.current = false; setSelectedId(null); }}
          onDelete={handleDelete}
          onDirtyChange={handleDirtyChange}
        />
      ) : (
        <Inspector label="模型详情"><InspectorEmpty icon={Boxes}>选择一个模型查看和编辑</InspectorEmpty></Inspector>
      )}
      {contextMenu}
      {confirmDialog}
    </div>
  );
};

export default ClaudeModelsPage;
