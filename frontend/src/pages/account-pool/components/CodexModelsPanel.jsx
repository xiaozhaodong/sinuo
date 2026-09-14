import { createPortal } from 'react-dom';
import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { Check, Database, Plus, RefreshCw, RotateCcw, Save, Trash2, X } from 'lucide-react';
import useModalLifecycle from '@hooks/useModalLifecycle.js';
import useConfirm from '@hooks/useConfirm.js';
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

const sortModels = (models = []) => [...models].sort((a, b) => {
  if (a.enabled !== b.enabled) return a.enabled ? -1 : 1;
  if (a.deprecated !== b.deprecated) return a.deprecated ? 1 : -1;
  if (a.source !== b.source) return a.source === 'official' ? -1 : 1;
  return String(a.id || '').localeCompare(String(b.id || ''));
});

const countEnabledModels = (models = []) => models.filter(model => model.enabled).length;

const CodexModelsPanel = ({ open = false, onClose, showNotice }) => {
  const [catalog, setCatalog] = useState(EMPTY_CATALOG);
  const [savedCatalog, setSavedCatalog] = useState(EMPTY_CATALOG);
  const { confirm, confirmDialog } = useConfirm();
  const [newModelId, setNewModelId] = useState('');
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState('');

  const desktopReady = isWailsEnvironment();

  const modelRows = useMemo(() => sortModels(catalog.models), [catalog.models]);
  const enabledCount = useMemo(() => countEnabledModels(catalog.models), [catalog.models]);
  const officialCount = useMemo(() => catalog.models.filter(model => model.source === 'official').length, [catalog.models]);
  const customCount = useMemo(() => catalog.models.filter(model => model.source === 'custom').length, [catalog.models]);
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
    } catch (err) {
      const message = err.message || '加载 Codex 模型目录失败';
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
    if (saving) return;
    if (dirty && !(await confirm({ title: '放弃未保存修改', message: 'Codex 模型目录有未保存修改，确定关闭吗？', confirmText: '放弃并关闭', danger: true }))) return;
    onClose?.();
  }, [confirm, dirty, onClose, saving]);

  const closeButtonRef = useRef(null);

  useModalLifecycle({ open, onClose: handleRequestClose, initialFocusRef: closeButtonRef });

  const handleToggleCatalog = useCallback(() => {
    updateCatalog(prev => ({ ...prev, enabled: !prev.enabled }));
  }, [updateCatalog]);

  const handleToggleModel = useCallback((id) => {
    updateCatalog(prev => ({
      ...prev,
      models: prev.models.map(model => model.id === id ? { ...model, enabled: !model.enabled } : model)
    }));
  }, [updateCatalog]);

  const handleAddModel = useCallback(() => {
    const id = newModelId.trim();
    if (!id) {
      showNotice?.('info', '请输入模型 ID');
      return;
    }
    if (catalog.models.some(model => model.id === id)) {
      showNotice?.('info', `模型「${id}」已存在`);
      return;
    }

    updateCatalog(prev => ({
      ...prev,
      models: [
        ...prev.models,
        {
          id,
          object: 'model',
          owned_by: 'openai',
          source: 'custom',
          enabled: true,
          deprecated: false,
          display_name: id,
          description: ''
        }
      ]
    }));
    setNewModelId('');
  }, [catalog.models, newModelId, showNotice, updateCatalog]);

  const handleRemoveModel = useCallback((id) => {
    updateCatalog(prev => ({
      ...prev,
      models: prev.models.filter(model => model.id !== id)
    }));
  }, [updateCatalog]);

  const handleSave = useCallback(async () => {
    try {
      setSaving(true);
      const saved = await saveCodexModelCatalog(catalog);
      setCatalog(saved);
      setSavedCatalog(saved);
      showNotice?.('success', 'Codex 模型目录已保存');
    } catch (err) {
      showNotice?.('error', err.message || '保存 Codex 模型目录失败');
    } finally {
      setSaving(false);
    }
  }, [catalog, showNotice]);

  const handleMergeDefaults = useCallback(async () => {
    try {
      setSaving(true);
      const merged = await mergeDefaultCodexModelCatalog();
      setCatalog(merged);
      setSavedCatalog(merged);
      showNotice?.('success', '已合并官方模型预设');
    } catch (err) {
      showNotice?.('error', err.message || '合并官方预设失败');
    } finally {
      setSaving(false);
    }
  }, [showNotice]);

  const handleResetDefaults = useCallback(async () => {
    if (!(await confirm({ title: '恢复预设目录', message: '确定恢复官方预设模型目录吗？自定义模型会被清除。', confirmText: '恢复预设', danger: true }))) return;

    try {
      setSaving(true);
      const reset = await resetCodexModelCatalog();
      setCatalog(reset);
      setSavedCatalog(reset);
      showNotice?.('success', '已恢复官方模型预设');
    } catch (err) {
      showNotice?.('error', err.message || '恢复官方预设失败');
    } finally {
      setSaving(false);
    }
  }, [confirm, showNotice]);

  if (!open) return null;

  const nav = document.querySelector('nav.sticky');
  const topOffset = nav ? nav.getBoundingClientRect().bottom : 0;

  return createPortal(
    <div className="fixed inset-0 z-[45] flex justify-end" style={{ top: topOffset }}>
      <button
        type="button"
        aria-label="关闭 Codex 模型目录"
        className="absolute inset-0 bg-overlay backdrop-blur-[2px]"
        onClick={handleRequestClose}
      />

      <aside
        role="dialog"
        aria-modal="true"
        aria-label="Codex 模型目录"
        className="relative z-10 flex h-full w-full max-w-[820px] flex-col border-l border-line bg-surface shadow-2xl animate-in slide-in-from-right duration-300"
      >
        <div className="border-b border-line-soft bg-gradient-to-r from-surface-sub via-surface to-surface px-6 py-5">
          <div className="flex items-start justify-between gap-4">
            <div className="flex min-w-0 items-start gap-3">
              <div className="flex h-10 w-10 shrink-0 items-center justify-center rounded-xl bg-inverted text-fg-inverted shadow-sm">
                <Database size={19} />
              </div>
              <div className="min-w-0">
                <h2 className="text-lg font-semibold text-fg">Codex 模型目录</h2>
                <p className="mt-0.5 text-sm text-fg-muted">GET /v1/models 本地目录</p>
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

          <div className="mt-4 flex flex-wrap items-center gap-2">
            <span className={`rounded-full border px-2.5 py-1 text-xs font-medium ${catalog.enabled ? 'tone-emerald' : 'border-line bg-surface-mut text-fg-muted'}`}>
              {catalog.enabled ? '本地接管' : '穿透上游'}
            </span>
            <span className="rounded-full border border-line bg-surface px-2.5 py-1 text-xs font-medium text-fg-body">
              启用 {enabledCount} 个
            </span>
            <span className="rounded-full border border-line bg-surface px-2.5 py-1 text-xs font-medium text-fg-body">
              官方 {officialCount} 个
            </span>
            <span className="rounded-full border border-line bg-surface px-2.5 py-1 text-xs font-medium text-fg-body">
              自定义 {customCount} 个
            </span>
          </div>
        </div>

        {!desktopReady ? (
          <div className="flex-1 px-6 py-5">
            <div className="rounded-xl border border-line bg-surface-sub px-4 py-4 text-sm text-fg-muted">
              桌面环境中可编辑本地模型目录。
            </div>
          </div>
        ) : (
          <>
            <div className="flex-1 overflow-y-auto px-6 py-5">
              {error && (
                <div className="mb-4 rounded-lg border border-danger-line bg-danger-soft px-4 py-3 text-sm text-danger">
                  {error}
                </div>
              )}

              <div className="grid gap-4 lg:grid-cols-[260px_1fr]">
                <div className="space-y-3">
                  <label className="flex items-center justify-between rounded-lg border border-line bg-surface-sub px-3 py-2.5">
                    <span>
                      <span className="block text-sm font-medium text-fg-body">本地接管 /v1/models</span>
                      <span className="mt-0.5 block text-xs text-fg-subtle">
                        关闭后穿透 CCF Codex 上游，不进入请求追踪
                      </span>
                    </span>
                    <button
                      type="button"
                      role="switch"
                      aria-checked={catalog.enabled}
                      onClick={handleToggleCatalog}
                      disabled={saving}
                      className={`relative h-6 w-11 rounded-full transition-colors disabled:opacity-50 ${catalog.enabled ? 'bg-indigo-500' : 'bg-line-strong'}`}
                    >
                      <span className={`absolute top-1 h-4 w-4 rounded-full bg-surface shadow-sm transition-all ${catalog.enabled ? 'right-1' : 'left-1'}`} />
                    </button>
                  </label>

                  <div className="rounded-lg border border-line bg-surface-sub p-3">
                    <span className="mb-2 block text-xs font-medium text-fg-muted">添加自定义模型</span>
                    <div className="flex gap-2">
                      <input
                        value={newModelId}
                        onChange={(event) => setNewModelId(event.target.value)}
                        onKeyDown={(event) => {
                          if (event.key === 'Enter') handleAddModel();
                        }}
                        disabled={saving}
                        placeholder="model-id"
                        className="min-w-0 flex-1 rounded-md border border-line bg-surface px-2 text-sm text-fg-body focus:border-inverted focus:outline-none focus:ring-1 focus:ring-inverted"
                      />
                      <button
                        type="button"
                        onClick={handleAddModel}
                        disabled={saving}
                        className="flex h-9 w-9 items-center justify-center rounded-md bg-inverted text-fg-inverted transition-colors hover:bg-inverted/85 disabled:opacity-50"
                        title="添加模型"
                      >
                        <Plus size={16} />
                      </button>
                    </div>
                  </div>
                </div>

                <div className="overflow-hidden rounded-lg border border-line">
                  <div className="grid grid-cols-[minmax(0,1fr)_90px_88px_92px] border-b border-line-soft bg-surface-sub px-3 py-2 text-xs font-medium text-fg-muted">
                    <span>模型</span>
                    <span>来源</span>
                    <span>状态</span>
                    <span className="text-right">操作</span>
                  </div>

                  {loading ? (
                    <div className="px-3 py-8 text-center text-sm text-fg-subtle">加载中...</div>
                  ) : modelRows.length === 0 ? (
                    <div className="px-3 py-8 text-center text-sm text-fg-subtle">暂无模型</div>
                  ) : (
                    <div className="divide-y divide-line-soft">
                      {modelRows.map(model => (
                        <div key={model.id} className="grid grid-cols-[minmax(0,1fr)_90px_88px_92px] items-center px-3 py-2.5 text-sm">
                          <div className="min-w-0">
                            <div className="truncate font-mono text-[13px] font-medium text-fg">{model.id}</div>
                            {(model.display_name && model.display_name !== model.id) || model.deprecated ? (
                              <div className="mt-0.5 flex items-center gap-1.5 text-[11px] text-fg-subtle">
                                {model.display_name && model.display_name !== model.id && <span className="truncate">{model.display_name}</span>}
                                {model.deprecated && <span className="rounded bg-warn-soft px-1.5 py-0.5 text-warn">deprecated</span>}
                              </div>
                            ) : null}
                          </div>
                          <span className={`w-fit rounded-full px-2 py-0.5 text-[11px] font-medium ${model.source === 'official' ? 'bg-info-soft text-info' : 'tone-violet'}`}>
                            {sourceLabel[model.source] || model.source || '自定义'}
                          </span>
                          <button
                            type="button"
                            onClick={() => handleToggleModel(model.id)}
                            disabled={saving}
                            className={`h-7 w-fit rounded-full px-2 text-[11px] font-medium transition-colors ${model.enabled ? 'bg-success-soft text-success' : 'bg-surface-mut text-fg-muted'}`}
                          >
                            {model.enabled ? '启用' : '禁用'}
                          </button>
                          <div className="flex justify-end">
                            {model.source === 'custom' && (
                              <button
                                type="button"
                                onClick={() => handleRemoveModel(model.id)}
                                disabled={saving}
                                className="flex h-8 w-8 items-center justify-center rounded-md text-fg-subtle transition-colors hover:bg-danger-soft hover:text-danger disabled:opacity-50"
                                title="删除自定义模型"
                              >
                                <Trash2 size={15} />
                              </button>
                            )}
                          </div>
                        </div>
                      ))}
                    </div>
                  )}
                </div>
              </div>
            </div>

            <div className="flex flex-col gap-3 border-t border-line-soft bg-surface px-6 py-4 sm:flex-row sm:items-center sm:justify-between">
              <div className="text-xs text-fg-subtle">
                {dirty ? '存在未保存修改' : '当前配置已保存'}
              </div>
              <div className="flex flex-wrap items-center gap-2">
                <button
                  type="button"
                  onClick={() => loadCatalog()}
                  disabled={loading || saving}
                  className={`${compactButtonClass} border-line bg-surface text-fg-body hover:bg-surface-sub`}
                >
                  <RefreshCw size={14} />
                  刷新
                </button>
                <button
                  type="button"
                  onClick={handleMergeDefaults}
                  disabled={loading || saving}
                  className={`${compactButtonClass} border-line bg-surface text-fg-body hover:bg-surface-sub`}
                >
                  <Check size={14} />
                  合并官方
                </button>
                <button
                  type="button"
                  onClick={handleResetDefaults}
                  disabled={loading || saving}
                  className={`${compactButtonClass} border-danger-line bg-danger-soft text-danger hover:bg-danger-soft`}
                >
                  <RotateCcw size={14} />
                  恢复预设
                </button>
                <button
                  type="button"
                  onClick={handleSave}
                  disabled={loading || saving || !dirty}
                  className={`${compactButtonClass} border-inverted bg-inverted text-fg-inverted hover:bg-inverted/85`}
                >
                  <Save size={14} />
                  保存
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
