import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { createPortal } from 'react-dom';
import { AlertCircle, Database, Info, MoreHorizontal, Plus, X } from 'lucide-react';
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
import {
  getCodexModelCatalog, isWailsEnvironment, mergeDefaultCodexModelCatalog,
  resetCodexModelCatalog, saveCodexModelCatalog
} from '@utils/wailsApi.js';
import {
  createModelDraft, hasValidCapabilities, MAX_CATALOG_BYTES, mergeImportedModels, parseCatalogImport
} from '../../account-pool/utils/codexModelCatalog.js';
import CodexModelsTable from './CodexModelsTable.jsx';
import CodexModelInspector from './CodexModelInspector.jsx';
import CodexImportSheet from './CodexImportSheet.jsx';
import { codexModelNeedsAttention, codexModelStatusTags, filterCodexModels, summarizeCodexModel } from './codexCatalogView.js';

const NEW_SELECTION = '__new__';
const EMPTY_CATALOG = { enabled: true, mode: 'local', models: [], effective_count: 0 };
const DISCARD_CONFIRM = {
  title: '放弃未保存的修改？', message: '当前模型有未保存的修改，继续操作后将丢失。', confirmText: '放弃修改', danger: true
};
const CATALOG_INFO = '集中维护模型及能力，供 Codex 自动发现。导入完整目录后，可编辑能力或复制模型。只要有显示在模型菜单的完整能力模型，ChatGPT 登录的 Codex 就只展示完整能力模型；能力待补全或无效的条目不会出现在 Codex 模型菜单中。';

const CodexModelsPage = ({ active = true, headerTarget = null }) => {
  const environmentReady = isWailsEnvironment();
  const [catalog, setCatalog] = useState(EMPTY_CATALOG);
  const [loading, setLoading] = useState(environmentReady);
  const [saving, setSaving] = useState(false);
  const [importing, setImporting] = useState(false);
  const [error, setError] = useState(null);
  const [actionError, setActionError] = useState(null);
  const [revision, setRevision] = useState(0);
  const [query, setQuery] = useState('');
  const [filter, setFilter] = useState('all');
  const [selectedId, setSelectedId] = useState(null);
  const [newDraft, setNewDraft] = useState(() => ({ model: createModelDraft(), revision: 0 }));
  const [importPreview, setImportPreview] = useState(null);
  const dirtyRef = useRef(false);
  const listRef = useRef(null);
  const fileInputRef = useRef(null);
  const { confirm, confirmDialog } = useConfirm();
  const { contextMenu, openContextMenu, openMenuAt } = useContextMenu();

  const loadCatalog = useCallback(async () => {
    if (!environmentReady) return;
    setLoading(true);
    setError(null);
    try {
      setCatalog(await getCodexModelCatalog());
      setRevision((value) => value + 1);
      setSelectedId((current) => current === NEW_SELECTION ? null : current);
    } catch (loadError) {
      setError(resolveErrorMessage(loadError, '加载 Codex 模型目录失败'));
    } finally {
      setLoading(false);
    }
  }, [environmentReady]);

  useEffect(() => {
    loadCatalog();
  }, [loadCatalog]);

  const rows = useMemo(() => filterCodexModels(catalog.models, { query, filter }), [catalog.models, filter, query]);
  const rowIds = useMemo(() => rows.map((model) => model.id), [rows]);
  const summaries = useMemo(() => new Map(catalog.models.map((model) => {
    const summary = summarizeCodexModel(model);
    return [model.id, { ...summary, tags: codexModelStatusTags(model, summary) }];
  })), [catalog.models]);
  const attentionCount = catalog.models.filter(codexModelNeedsAttention).length;
  const isLocalCatalog = catalog.enabled && catalog.mode !== 'passthrough';
  const isNew = selectedId === NEW_SELECTION;
  const effectiveId = isNew || catalog.models.some((model) => model.id === selectedId) ? selectedId : rows[0]?.id || null;
  const selected = isNew ? null : catalog.models.find((model) => model.id === effectiveId) || null;
  const actionsLocked = saving || importing || !environmentReady;
  const filterOptions = [
    { value: 'all', label: '全部' }, { value: 'enabled', label: '已启用' },
    { value: 'attention', label: attentionCount ? `需处理 ${attentionCount}` : '需处理' }
  ];

  const handleDirtyChange = useCallback((value) => { dirtyRef.current = value; }, []);
  const confirmDiscard = useCallback(async () => {
    if (!dirtyRef.current) return true;
    const discarded = await confirm(DISCARD_CONFIRM);
    if (discarded) {
      dirtyRef.current = false;
      setRevision((value) => value + 1);
      setSelectedId((current) => current === NEW_SELECTION ? null : current);
    }
    return discarded;
  }, [confirm]);
  const requestSelect = useCallback(async (nextId) => {
    if (nextId === effectiveId || !(await confirmDiscard())) return;
    setSelectedId(nextId);
  }, [confirmDiscard, effectiveId]);
  const handleListKeyDown = useListKeyboard({ ids: rowIds, selectedId: effectiveId, onSelect: requestSelect, containerRef: listRef });

  // 保存、合并与恢复预设共用成功替换与 revision 更新，目录始终是后端返回的已落库值。
  const persistCatalog = useCallback(async (buildNext, writeCatalog = saveCodexModelCatalog) => {
    setSaving(true);
    try {
      const next = buildNext(catalog);
      const saved = await writeCatalog({ enabled: next.enabled, mode: next.mode, models: next.models });
      setCatalog(saved);
      setRevision((value) => value + 1);
      dirtyRef.current = false;
      return saved;
    } finally {
      setSaving(false);
    }
  }, [catalog]);
  const runCatalogAction = useCallback(async (buildNext, writeCatalog) => {
    if (!(await confirmDiscard())) return null;
    setActionError(null);
    try {
      return await persistCatalog(buildNext, writeCatalog);
    } catch (failure) {
      setActionError(resolveErrorMessage(failure, '保存模型目录失败'));
      return null;
    }
  }, [confirmDiscard, persistCatalog]);

  const toggleEnabled = (id, enabled) => runCatalogAction((current) => ({
    ...current, models: current.models.map((model) => model.id === id ? { ...model, enabled } : model)
  }));
  const toggleEnabledDirectory = (enabled) => runCatalogAction((current) => ({ ...current, enabled, mode: 'local' }));
  const requestRefresh = async () => {
    if (!(await confirmDiscard())) return;
    setActionError(null);
    await loadCatalog();
  };
  const handleSaveInspector = async (draft, originalId) => {
    const result = await persistCatalog((current) => {
      if (!originalId && current.models.some((entry) => entry.id === draft.id)) throw new Error(`模型「${draft.id}」已存在`);
      const existing = originalId && current.models.find((entry) => entry.id === originalId);
      const model = existing ? { ...draft, enabled: existing.enabled, source: existing.source, deprecated: existing.deprecated } : { ...draft, enabled: true };
      return { ...current, models: originalId ? current.models.map((entry) => entry.id === originalId ? model : entry) : [...current.models, model] };
    });
    setSelectedId(draft.id);
    return result;
  };
  const startNew = async (template) => {
    if (actionsLocked || (template && !hasValidCapabilities(template)) || !(await confirmDiscard())) return;
    setActionError(null);
    setNewDraft((previous) => ({ model: createModelDraft(template), revision: previous.revision + 1 }));
    setSelectedId(NEW_SELECTION);
  };
  const handleDelete = async (model) => {
    if (model.source !== 'custom' || !(await confirmDiscard())) return;
    if (!(await confirm({ title: '删除模型', message: `确定从目录中删除「${model.id}」吗？`, confirmText: '删除', danger: true }))) return;
    const index = rows.findIndex((row) => row.id === model.id);
    const nextId = rows[index + 1]?.id || rows[index - 1]?.id || null;
    const result = await runCatalogAction((current) => ({ ...current, models: current.models.filter((entry) => entry.id !== model.id) }));
    if (result) setSelectedId(nextId);
  };
  const handleRowContextMenu = (event, model) => {
    if (!dirtyRef.current) setSelectedId(model.id);
    openContextMenu(event, [
      { label: model.enabled ? '停用' : '启用', disabled: actionsLocked, onSelect: () => toggleEnabled(model.id, !model.enabled) },
      { label: '复制为新模型', disabled: actionsLocked || !hasValidCapabilities(model), onSelect: () => startNew(model) },
      { label: '复制模型 ID', onSelect: () => copyTextToClipboard(model.id, '模型 ID') },
      model.source === 'custom' && '-',
      model.source === 'custom' && { label: '删除…', danger: true, disabled: actionsLocked, onSelect: () => handleDelete(model) }
    ]);
  };
  const requestImport = async () => {
    if (!(await confirmDiscard())) return;
    fileInputRef.current?.click();
  };
  const handleImport = async (event) => {
    const file = event.target.files?.[0];
    event.target.value = '';
    if (!file) return;
    setImporting(true);
    setActionError(null);
    try {
      if (file.size > MAX_CATALOG_BYTES) throw new Error('目录文件不能超过 16 MiB');
      const text = await file.text();
      setImportPreview({ ...parseCatalogImport(text, catalog.models), name: file.name });
    } catch (failure) {
      setActionError(`导入失败：${resolveErrorMessage(failure, '无法读取目录文件')}`);
    } finally {
      setImporting(false);
    }
  };
  const applyImport = async () => {
    await persistCatalog((current) => ({ ...current, models: mergeImportedModels(current.models, importPreview.models) }));
    setImportPreview(null);
  };
  const resetDefaults = async () => {
    if (!(await confirmDiscard())) return;
    if (!(await confirm({ title: '恢复预设目录', message: '恢复后将清除自定义模型和全部已维护的能力，仅保留基础预设 ID。需要重新导入完整目录，确定继续吗？', confirmText: '恢复预设', danger: true }))) return;
    await runCatalogAction((current) => current, resetCodexModelCatalog);
  };
  const openMoreMenu = (event) => openMenuAt(event.currentTarget, [
    { label: '导入完整目录…', disabled: actionsLocked, onSelect: requestImport },
    { label: '合并官方预设', disabled: actionsLocked, onSelect: () => runCatalogAction((current) => current, mergeDefaultCodexModelCatalog) },
    '-', { label: '恢复预设…', danger: true, disabled: actionsLocked, onSelect: resetDefaults },
    '-', { label: '重新载入', disabled: actionsLocked || loading, onSelect: requestRefresh }
  ]);
  const actions = (
    <div className="flex items-center gap-1">
      <ToolbarSearch value={query} onChange={setQuery} placeholder="筛选模型" width={180} />
      <ToolbarButton icon={MoreHorizontal} title="更多操作" onClick={openMoreMenu} disabled={actionsLocked} />
      <ToolbarButton icon={Plus} title="新建模型" onClick={() => startNew()} disabled={actionsLocked} />
    </div>
  );

  if (error && catalog.models.length === 0) {
    return (
      <div className="motion-page flex min-h-0 min-w-0 flex-1 flex-col bg-surface">
        {headerTarget && active && createPortal(actions, headerTarget)}
        {!headerTarget && <div className="flex shrink-0 justify-end border-b border-line px-3 py-2">{actions}</div>}
        <div className="p-5">
          <ErrorMessage title="Codex 模型目录加载失败" message={error} onRetry={loadCatalog} />
        </div>
      </div>
    );
  }
  if (loading && catalog.models.length === 0) return <LoadingSpinner text="加载 Codex 模型目录..." />;

  return (
    <div className="motion-page flex min-h-0 min-w-0 flex-1 bg-surface">
      {headerTarget && active && createPortal(actions, headerTarget)}
      <input
        ref={fileInputRef}
        type="file"
        accept=".json,application/json"
        aria-label="导入完整 Codex 目录"
        className="hidden"
        onChange={handleImport}
      />
      <div className="flex min-w-0 flex-1 flex-col">
        {!headerTarget && <div className="flex shrink-0 justify-end border-b border-line px-3 py-2">{actions}</div>}
        <div className="flex shrink-0 flex-wrap items-center gap-x-3 gap-y-1.5 border-b border-line bg-inspector px-3 py-1.5">
          <Switch checked={isLocalCatalog} label="本地接管 /v1/models" disabled={actionsLocked} onChange={toggleEnabledDirectory} />
          <span className="text-[12.5px] text-fg">本地接管 /v1/models</span>
          <span className="text-[11.5px] text-fg-muted">关闭后穿透账号池上游</span>
          <div className="ml-auto flex items-center gap-2">
            <SegmentedControl options={filterOptions} value={filter} onChange={setFilter} ariaLabel="模型筛选" disabled={saving} />
            <button type="button" aria-label="目录说明" title={CATALOG_INFO} className="inline-flex h-6 w-6 items-center justify-center rounded text-fg-muted hover:bg-surface-mut hover:text-fg">
              <Info size={14} />
            </button>
          </div>
        </div>
        {(actionError || error) && (
          <div role="alert" className="selectable flex shrink-0 items-center gap-2 border-b border-danger-line bg-danger-soft px-4 py-1.5 text-xs text-danger">
            <AlertCircle size={13} className="shrink-0" />
            <span className="min-w-0 flex-1">{actionError || error}</span>
            <button type="button" onClick={() => { setActionError(null); setError(null); }} aria-label="关闭提示" className="shrink-0 rounded p-0.5 hover:bg-danger-soft">
              <X size={13} />
            </button>
          </div>
        )}
        <div ref={listRef} tabIndex={0} onKeyDown={handleListKeyDown} aria-label="Codex 模型列表" className="min-h-0 flex-1 overflow-auto focus:outline-none">
          {!environmentReady ? (
            <ListEmpty title="桌面环境中可编辑本地模型目录" />
          ) : rows.length > 0 ? (
            <CodexModelsTable
              models={rows}
              summaries={summaries}
              selectedId={effectiveId}
              saving={actionsLocked}
              onSelect={requestSelect}
              onToggleEnabled={toggleEnabled}
              onRowContextMenu={handleRowContextMenu}
            />
          ) : catalog.models.length > 0 ? (
            <ListEmpty title="没有匹配的模型" />
          ) : (
            <ListEmpty icon={Database} title="暂无模型" description="从更多菜单导入完整目录，或点击 + 新建模型" />
          )}
        </div>
        <StatusBar>
          <StatusItem label="模型" value={catalog.models.length} />
          <StatusItem label="启用" value={catalog.models.filter((model) => model.enabled).length} />
          <StatusItem label="完整能力" value={catalog.models.filter(hasValidCapabilities).length} />
          <StatusItem label="需处理" value={attentionCount} valueClassName={attentionCount > 0 ? 'text-warn' : undefined} />
          <StatusItem label="官方" value={catalog.models.filter((model) => model.source === 'official').length} />
          <StatusItem label="自定义" value={catalog.models.filter((model) => model.source === 'custom').length} />
          {(query || filter !== 'all') && <StatusItem label="筛选后" value={rows.length} />}
          {!isLocalCatalog && <StatusItem label="目录" value="穿透上游" valueClassName="text-warn" />}
          <StatusSpacer />
          {saving && <StatusItem label="" value="保存中…" />}
        </StatusBar>
      </div>
      {isNew || selected ? (
        <CodexModelInspector
          key={isNew ? `${NEW_SELECTION}:${newDraft.revision}` : `${selected.id}:${revision}`}
          model={isNew ? newDraft.model : selected}
          models={catalog.models}
          isNew={isNew}
          saving={saving}
          onSave={handleSaveInspector}
          onCancelNew={() => { dirtyRef.current = false; setSelectedId(null); }}
          onDelete={handleDelete}
          onDirtyChange={handleDirtyChange}
        />
      ) : (
        <Inspector label="Codex 模型详情" width={360}>
          <InspectorEmpty icon={Database}>选择一个模型查看和编辑</InspectorEmpty>
        </Inspector>
      )}
      {importPreview && (
        <CodexImportSheet preview={importPreview} saving={saving} onImport={applyImport} onClose={() => setImportPreview(null)} />
      )}
      {contextMenu}
      {confirmDialog}
    </div>
  );
};

export default CodexModelsPage;
