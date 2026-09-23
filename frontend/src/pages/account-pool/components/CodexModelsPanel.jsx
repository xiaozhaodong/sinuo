import { createPortal } from 'react-dom';
import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { Check, Copy, Database, Pencil, Plus, RefreshCw, RotateCcw, Save, Trash2, Upload, X } from 'lucide-react';
import CodexModelEditor from './CodexModelEditor.jsx';
import { createModelDraft, hasValidCapabilities, MAX_CATALOG_BYTES, mergeImportedModels, parseCatalogImport } from '../utils/codexModelCatalog.js';
import useModalLifecycle from '@hooks/useModalLifecycle.js';
import useConfirm from '@hooks/useConfirm.js';
import { resolveErrorMessage } from '@utils/errors.js';
import {
  getCodexModelCatalog,
  isWailsEnvironment,
  mergeDefaultCodexModelCatalog,
  resetCodexModelCatalog,
  saveCodexModelCatalog
} from '@utils/wailsApi.js';

const EMPTY_CATALOG = {
  enabled: true,
  mode: 'local',
  models: [],
  effective_count: 0
};

const sourceLabel = {
  official: '官方',
  custom: '自定义'
};

const compactButtonClass = 'inline-flex h-8 items-center gap-1.5 rounded-lg border px-2.5 text-xs font-medium transition-colors disabled:cursor-not-allowed disabled:opacity-50';
const secondaryButtonClass = `${compactButtonClass} border-line bg-surface text-fg-body hover:bg-surface-sub`;
const primaryButtonClass = `${compactButtonClass} border-inverted bg-inverted text-fg-inverted hover:bg-inverted/85`;
const iconButtonBaseClass = 'flex h-8 w-8 items-center justify-center rounded-md text-fg-subtle transition-colors disabled:opacity-30';
const iconButtonClass = `${iconButtonBaseClass} hover:bg-surface-mut hover:text-fg-body`;
const dangerIconButtonClass = `${iconButtonBaseClass} hover:bg-danger-soft hover:text-danger`;
const rowClass = 'grid grid-cols-[minmax(0,1fr)_60px_56px_96px] items-center gap-1 px-3 py-3';

const sortModels = (models = []) => [...models].sort((a, b) => {
  if (a.enabled !== b.enabled) return a.enabled ? -1 : 1;
  if (a.deprecated !== b.deprecated) return a.deprecated ? 1 : -1;
  if (a.source !== b.source) return a.source === 'official' ? -1 : 1;
  return String(a.id || '').localeCompare(String(b.id || ''));
});

const countEnabledModels = (models = []) => models.filter(model => model.enabled).length;

const isLocalCatalog = catalog => catalog.enabled && catalog.mode !== 'passthrough';

const capabilityStatus = model => {
  if (model.capabilities_error) return { label: '能力无效', className: 'text-danger', title: model.capabilities_error };
  if (model.capabilities_json?.trim()) return { label: '完整能力', className: 'text-success' };
  return { label: '能力待补全', className: 'text-warn' };
};

const ImportPreview = ({ preview, onApply, onCancel }) => (
  <div className="rounded-xl border border-line bg-surface-sub p-4 text-sm text-fg-body">
    <p className="font-medium">导入预览 · {preview.name}</p>
    <p className="mt-2">
      {preview.models.length} 个模型，新增 {preview.models.length - preview.conflicts.length} 个，更新 {preview.conflicts.length} 个同名模型的名称、描述和能力。
    </p>
    {preview.conflicts.length > 0 && (
      <p className="mt-2 max-h-24 overflow-y-auto break-all text-xs leading-5">
        更新：{preview.conflicts.join('、')}。现有启用状态保持不变。
      </p>
    )}
    <div className="mt-3 flex gap-2">
      <button type="button" className={primaryButtonClass} onClick={onApply}>应用导入</button>
      <button type="button" className={secondaryButtonClass} onClick={onCancel}>取消</button>
    </div>
  </div>
);

const ModelRow = ({ model, toggleDisabled, actionsLocked, onToggle, onEdit, onCopy, onRemove }) => {
  const status = capabilityStatus(model);
  return (
    <div className={`${rowClass} text-sm`}>
      <div className="min-w-0">
        <div className="truncate font-mono text-[13px] font-medium text-fg" title={model.id}>{model.id}</div>
        {model.display_name && model.display_name !== model.id && (
          <div className="mt-0.5 truncate text-xs text-fg-subtle">{model.display_name}</div>
        )}
        <div className={`mt-1 text-[11px] ${status.className}`} title={status.title}>
          {status.label}{model.deprecated ? ' · 已弃用' : ''}
        </div>
      </div>
      <span className="text-xs text-fg-muted">{sourceLabel[model.source] || model.source || '自定义'}</span>
      <button
        type="button"
        onClick={onToggle}
        disabled={toggleDisabled}
        className={`w-fit rounded-full px-2 py-1 text-[11px] transition-colors disabled:opacity-50 ${model.enabled ? 'bg-success-soft text-success' : 'bg-surface-mut text-fg-muted'}`}
      >
        {model.enabled ? '启用' : '禁用'}
      </button>
      <div className="flex justify-end">
        <button type="button" title="编辑模型" aria-label={`编辑 ${model.id}`} disabled={actionsLocked} className={iconButtonClass} onClick={onEdit}>
          <Pencil size={14} />
        </button>
        <button type="button" title="复制模型" aria-label={`复制 ${model.id}`} disabled={actionsLocked || !hasValidCapabilities(model)} className={iconButtonClass} onClick={onCopy}>
          <Copy size={14} />
        </button>
        {model.source === 'custom' ? (
          <button
            type="button"
            title="删除自定义模型"
            aria-label={`删除 ${model.id}`}
            disabled={actionsLocked}
            className={dangerIconButtonClass}
            onClick={onRemove}
          >
            <Trash2 size={14} />
          </button>
        ) : <span className="w-8" />}
      </div>
    </div>
  );
};

const CodexModelsPanel = ({ open = false, onClose, showNotice }) => {
  const [catalog, setCatalog] = useState(EMPTY_CATALOG);
  const [savedCatalog, setSavedCatalog] = useState(EMPTY_CATALOG);
  const { confirm, confirmDialog } = useConfirm();
  // editing = { model, isNew, dirty }；dirty 由编辑器上报，只在有未应用修改时才拦截关闭/返回。
  const [editing, setEditing] = useState(null);
  const [importPreview, setImportPreview] = useState(null);
  const [importing, setImporting] = useState(false);
  const fileInputRef = useRef(null);
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState('');

  const desktopReady = isWailsEnvironment();

  const modelRows = useMemo(() => sortModels(catalog.models), [catalog.models]);
  const enabledCount = useMemo(() => countEnabledModels(catalog.models), [catalog.models]);
  const officialCount = useMemo(() => catalog.models.filter(model => model.source === 'official').length, [catalog.models]);
  const customCount = useMemo(() => catalog.models.filter(model => model.source === 'custom').length, [catalog.models]);
  const templates = useMemo(() => catalog.models.filter(hasValidCapabilities), [catalog.models]);
  const dirty = useMemo(() => JSON.stringify(catalog) !== JSON.stringify(savedCatalog), [catalog, savedCatalog]);

  const loadCatalog = useCallback(async ({ silent = false } = {}) => {
    if (!desktopReady) {
      setLoading(false);
      return;
    }

    try {
      if (!silent) setLoading(true);
      setError('');
      const data = await getCodexModelCatalog();
      setCatalog(data || EMPTY_CATALOG);
      setSavedCatalog(data || EMPTY_CATALOG);
      setEditing(null);
      setImportPreview(null);
    } catch (err) {
      const message = resolveErrorMessage(err, '加载 Codex 模型目录失败');
      setError(message);
      showNotice?.('error', message);
    } finally {
      if (!silent) setLoading(false);
    }
  }, [desktopReady, showNotice]);

  useEffect(() => {
    if (open) {
      loadCatalog();
    }
  }, [loadCatalog, open]);

  const updateCatalog = useCallback((updater) => {
    setCatalog(prev => {
      const next = typeof updater === 'function' ? updater(prev) : updater;
      return {
        ...EMPTY_CATALOG,
        ...next,
        models: Array.isArray(next?.models) ? next.models : []
      };
    });
  }, []);

  const handleRequestClose = useCallback(async () => {
    if (saving || importing) return;
    if ((dirty || editing?.dirty) && !(await confirm({ title: '放弃未保存修改', message: 'Codex 模型目录有未保存修改，确定关闭吗？', confirmText: '放弃并关闭', danger: true }))) return;
    onClose?.();
  }, [confirm, dirty, editing, importing, onClose, saving]);

  const closeButtonRef = useRef(null);

  useModalLifecycle({ open, onClose: handleRequestClose, initialFocusRef: closeButtonRef });

  const handleToggleCatalog = useCallback(() => {
    updateCatalog(prev => ({ ...prev, enabled: !isLocalCatalog(prev), mode: 'local' }));
  }, [updateCatalog]);

  const handleToggleModel = useCallback((id) => {
    updateCatalog(prev => ({
      ...prev,
      models: prev.models.map(model => (model.id === id ? { ...model, enabled: !model.enabled } : model))
    }));
  }, [updateCatalog]);

  const handleEditorDirtyChange = useCallback((editorDirty) => {
    setEditing(previous => (previous && previous.dirty !== editorDirty ? { ...previous, dirty: editorDirty } : previous));
  }, []);

  const handleCancelEdit = useCallback(async (changed) => {
    if (changed && !(await confirm({ title: '返回模型目录', message: '返回将放弃尚未应用的编辑。', confirmText: '放弃并返回', danger: true }))) return;
    setEditing(null);
  }, [confirm]);

  // 由编辑器的保存按钮调用，抛出的错误会显示在编辑器内。
  const handleApplyModel = (model) => {
    if (editing.isNew && catalog.models.some(entry => entry.id === model.id)) throw new Error(`模型「${model.id}」已存在`);
    updateCatalog(previous => ({
      ...previous,
      models: editing.isNew ? [...previous.models, model] : previous.models.map(entry => (entry.id === model.id ? model : entry))
    }));
    setEditing(null);
  };

  const handleImport = async (event) => {
    const file = event.target.files?.[0];
    event.target.value = '';
    if (!file) return;
    setImporting(true);
    setError('');
    try {
      if (file.size > MAX_CATALOG_BYTES) throw new Error('目录文件不能超过 16 MiB');
      const text = await file.text();
      setImportPreview({ ...parseCatalogImport(text, catalog.models), text, name: file.name });
    } catch (err) {
      setError(`导入失败：${resolveErrorMessage(err, '无法读取目录文件')}`);
    } finally {
      setImporting(false);
    }
  };

  const applyImport = () => {
    try {
      const parsed = parseCatalogImport(importPreview.text, catalog.models);
      updateCatalog(previous => ({ ...previous, models: mergeImportedModels(previous.models, parsed.models) }));
      setImportPreview(null);
      showNotice?.('success', '已应用导入，请保存目录');
    } catch (err) {
      setError(resolveErrorMessage(err, '应用导入失败'));
    }
  };

  const handleRemoveModel = useCallback((id) => {
    updateCatalog(prev => ({
      ...prev,
      models: prev.models.filter(model => model.id !== id)
    }));
  }, [updateCatalog]);

  const handleRefresh = useCallback(async () => {
    if (dirty && !(await confirm({ title: '重新读取目录', message: '刷新将放弃未保存的修改。', confirmText: '放弃并刷新', danger: true }))) return;
    loadCatalog();
  }, [confirm, dirty, loadCatalog]);

  const handleSave = useCallback(async () => {
    try {
      setSaving(true);
      const saved = await saveCodexModelCatalog(catalog);
      setCatalog(saved);
      setSavedCatalog(saved);
      showNotice?.('success', 'Codex 模型目录已保存');
    } catch (err) {
      showNotice?.('error', resolveErrorMessage(err, '保存 Codex 模型目录失败'));
    } finally {
      setSaving(false);
    }
  }, [catalog, showNotice]);

  const handleMergeDefaults = useCallback(async () => {
    if (dirty && !(await confirm({ title: '合并预设目录', message: '此操作会重新读取已保存目录，当前未保存修改将丢失。继续吗？', confirmText: '继续合并', danger: true }))) return;
    try {
      setSaving(true);
      const merged = await mergeDefaultCodexModelCatalog();
      setCatalog(merged);
      setSavedCatalog(merged);
      showNotice?.('success', '已合并官方模型预设');
    } catch (err) {
      showNotice?.('error', resolveErrorMessage(err, '合并官方预设失败'));
    } finally {
      setSaving(false);
    }
  }, [confirm, dirty, showNotice]);

  const handleResetDefaults = useCallback(async () => {
    if (!(await confirm({ title: '恢复预设目录', message: '恢复后将清除自定义模型和全部已维护的能力，仅保留基础预设 ID。需要重新导入完整目录，确定继续吗？', confirmText: '恢复预设', danger: true }))) return;

    try {
      setSaving(true);
      const reset = await resetCodexModelCatalog();
      setCatalog(reset);
      setSavedCatalog(reset);
      showNotice?.('success', '已恢复官方模型预设');
    } catch (err) {
      showNotice?.('error', resolveErrorMessage(err, '恢复官方预设失败'));
    } finally {
      setSaving(false);
    }
  }, [confirm, showNotice]);

  if (!open) return null;

  const nav = document.querySelector('nav.sticky');
  const topOffset = nav ? nav.getBoundingClientRect().bottom : 0;
  const busy = loading || saving || importing;
  const actionsLocked = busy || !!editing || !!importPreview;
  const localCatalog = isLocalCatalog(catalog);
  const summaryLabels = [`启用 ${enabledCount} 个`, `完整能力 ${templates.length} 个`, `官方 ${officialCount} 个`, `自定义 ${customCount} 个`];

  return createPortal(
    <div className="fixed inset-0 z-[45] flex justify-end" style={{ top: topOffset }}>
      <button type="button" aria-label="关闭 Codex 模型目录" className="absolute inset-0 bg-overlay backdrop-blur-[2px]" onClick={handleRequestClose} />

      <aside role="dialog" aria-modal="true" aria-label="Codex 模型目录" className="relative z-10 flex h-full w-full max-w-[820px] flex-col border-l border-line bg-surface shadow-2xl">
        <div className="border-b border-line-soft bg-surface-sub px-6 py-5">
          <div className="flex items-start justify-between gap-4">
            <div className="flex min-w-0 items-start gap-3">
              <div className="flex h-10 w-10 shrink-0 items-center justify-center rounded-xl bg-inverted text-fg-inverted">
                <Database size={19} />
              </div>
              <div className="min-w-0">
                <h2 className="text-lg font-semibold text-fg">Codex 模型目录</h2>
                <p className="mt-1 text-sm text-fg-muted">集中维护模型及能力，供 Codex 自动发现</p>
              </div>
            </div>
            <button
              type="button"
              ref={closeButtonRef}
              aria-label="关闭抽屉"
              onClick={handleRequestClose}
              className="rounded-lg border border-line bg-surface p-2 text-fg-subtle transition-colors hover:border-line-strong hover:text-fg-body"
            >
              <X size={16} />
            </button>
          </div>

          <div className="mt-4 flex flex-wrap gap-2 text-xs text-fg-body">
            <span className={`rounded-full border px-2.5 py-1 ${localCatalog ? 'tone-emerald' : 'border-line'}`}>
              {localCatalog ? '本地接管' : '穿透上游'}
            </span>
            {summaryLabels.map(label => (
              <span key={label} className="rounded-full border border-line bg-surface px-2.5 py-1">{label}</span>
            ))}
          </div>
        </div>

        {!desktopReady ? (
          <div className="flex-1 px-6 py-5 text-sm text-fg-muted">桌面环境中可编辑本地模型目录。</div>
        ) : (
          <>
            <div className="flex-1 overflow-y-auto px-6 py-5">
              {error && <p role="alert" className="mb-4 rounded-lg border border-danger-line bg-danger-soft px-4 py-3 text-sm text-danger">{error}</p>}

              {editing ? (
                <CodexModelEditor
                  key={`${editing.isNew ? 'new' : 'edit'}-${editing.model.id}`}
                  model={editing.model}
                  isNew={editing.isNew}
                  templates={templates}
                  onApply={handleApplyModel}
                  onCancel={handleCancelEdit}
                  onDirtyChange={handleEditorDirtyChange}
                />
              ) : (
                <div className="space-y-4">
                  <input ref={fileInputRef} type="file" accept=".json,application/json" className="hidden" aria-label="导入完整 Codex 目录" onChange={handleImport} />

                  {importPreview && <ImportPreview preview={importPreview} onApply={applyImport} onCancel={() => setImportPreview(null)} />}

                  <div className="flex flex-wrap items-center justify-between gap-3 rounded-xl border border-line bg-surface-sub p-3">
                    <div>
                      <span className="block text-sm font-medium text-fg-body">本地接管 /v1/models</span>
                      <span className="mt-1 block text-xs text-fg-subtle">关闭后穿透账号池上游</span>
                    </div>
                    <button
                      type="button"
                      role="switch"
                      aria-label="本地接管 /v1/models"
                      aria-checked={localCatalog}
                      onClick={handleToggleCatalog}
                      disabled={busy}
                      className={`relative h-6 w-11 rounded-full transition-colors disabled:opacity-50 ${localCatalog ? 'bg-inverted' : 'bg-line-strong'}`}
                    >
                      <span className={`absolute left-1 top-1 h-4 w-4 rounded-full bg-surface shadow-sm transition-transform ${localCatalog ? 'translate-x-5' : 'translate-x-0'}`} />
                    </button>
                  </div>

                  <div className="flex flex-wrap items-center justify-between gap-3">
                    <p className="max-w-md text-xs leading-5 text-fg-subtle">
                      导入完整目录后，可编辑能力或复制模型。只要有显示在模型菜单的完整能力模型，ChatGPT 登录的 Codex 就只展示完整能力模型；能力待补全或无效的条目不会出现在 Codex 模型菜单中。
                    </p>
                    <div className="flex gap-2">
                      <button type="button" disabled={busy} className={secondaryButtonClass} onClick={() => fileInputRef.current?.click()}>
                        <Upload size={14} />
                        {importing ? '读取中…' : '导入完整目录'}
                      </button>
                      <button type="button" disabled={actionsLocked} className={primaryButtonClass} onClick={() => setEditing({ model: createModelDraft(), isNew: true, dirty: false })}>
                        <Plus size={14} />
                        新建模型
                      </button>
                    </div>
                  </div>

                  <div className="overflow-hidden rounded-xl border border-line">
                    <div className={`${rowClass} border-b border-line-soft bg-surface-sub text-xs font-medium text-fg-muted`}>
                      <span>模型 / 能力</span>
                      <span>来源</span>
                      <span>状态</span>
                      <span className="text-right">操作</span>
                    </div>
                    {loading ? (
                      <p className="px-3 py-8 text-center text-sm text-fg-subtle">加载中...</p>
                    ) : modelRows.length === 0 ? (
                      <p className="px-3 py-8 text-center text-sm text-fg-subtle">暂无模型，请导入完整目录</p>
                    ) : (
                      <div className="divide-y divide-line-soft">
                        {modelRows.map(model => (
                          <ModelRow
                            key={model.id}
                            model={model}
                            toggleDisabled={busy || !!importPreview}
                            actionsLocked={actionsLocked}
                            onToggle={() => handleToggleModel(model.id)}
                            onEdit={() => setEditing({ model, isNew: false, dirty: false })}
                            onCopy={() => setEditing({ model: createModelDraft(model), isNew: true, dirty: false })}
                            onRemove={() => handleRemoveModel(model.id)}
                          />
                        ))}
                      </div>
                    )}
                  </div>
                </div>
              )}
            </div>

            <div className="flex flex-col gap-3 border-t border-line-soft px-6 py-4 sm:flex-row sm:items-center sm:justify-between">
              <p className="text-xs text-fg-subtle">
                {editing ? '请先应用编辑，再保存目录' : dirty ? '存在未保存修改' : '当前配置已保存'}
              </p>
              <div className="flex flex-wrap gap-2">
                <button type="button" disabled={actionsLocked} className={secondaryButtonClass} onClick={handleRefresh}>
                  <RefreshCw size={14} />
                  刷新
                </button>
                <button type="button" disabled={actionsLocked} className={secondaryButtonClass} onClick={handleMergeDefaults}>
                  <Check size={14} />
                  合并官方
                </button>
                <button type="button" disabled={actionsLocked} className={`${compactButtonClass} border-danger-line bg-danger-soft text-danger`} onClick={handleResetDefaults}>
                  <RotateCcw size={14} />
                  恢复预设
                </button>
                <button type="button" disabled={actionsLocked || !dirty} className={primaryButtonClass} onClick={handleSave}>
                  <Save size={14} />
                  {saving ? '保存中…' : '保存目录'}
                </button>
              </div>
            </div>
          </>
        )}
      </aside>
      {confirmDialog}
    </div>,
    document.body
  );
};

export default CodexModelsPanel;
