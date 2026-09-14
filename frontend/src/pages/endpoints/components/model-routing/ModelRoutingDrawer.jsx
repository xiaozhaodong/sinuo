// ============================================
// 模型路由抽屉：模型 → 端点集合的编排入口
// 2026-09-03
// ============================================

import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { createPortal } from 'react-dom';
import { AlertTriangle, Plus, RefreshCw, Route, X } from 'lucide-react';
import { Button, LoadingSpinner } from '@components/ui';
import {
  createModelRouteBinding,
  deleteModelRouteBinding,
  fetchModelRouteBindings,
  fetchModelRoutingSettings,
  testModelRouting,
  updateModelRouteBinding,
  updateModelRoutingSettings
} from '@utils/api.js';
import { subscribeToEvent } from '@utils/wailsApi.js';
import useModalLifecycle from '@hooks/useModalLifecycle.js';
import useConfirm from '@hooks/useConfirm.js';
import { patchBindingPayload } from '../../utils/modelRouting.js';
import ModelBindingDialog from './ModelBindingDialog.jsx';
import ModelBindingRow from './ModelBindingRow.jsx';
import RouteTestBox from './RouteTestBox.jsx';
import ToggleSwitch from '../ToggleSwitch.jsx';

const ModelRoutingDrawer = ({ open, onClose, endpoints = [] }) => {
  const [settings, setSettings] = useState(null);
  const [bindings, setBindings] = useState([]);
  const [loading, setLoading] = useState(true);
  const { confirm, confirmDialog } = useConfirm();
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  const [dialogTarget, setDialogTarget] = useState(null);
  const closeButtonRef = useRef(null);

  useModalLifecycle({ open, onClose, initialFocusRef: closeButtonRef });

  const load = useCallback(async () => {
    setError('');
    try {
      const [nextSettings, nextBindings] = await Promise.all([fetchModelRoutingSettings(), fetchModelRouteBindings()]);
      setSettings(nextSettings);
      setBindings(nextBindings);
    } catch (loadError) {
      setError(loadError?.message || '模型路由加载失败');
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    if (!open) return;
    setLoading(true);
    load();
  }, [open, load]);

  // 别处（例如另一个窗口或后端事件）改动配置时同步刷新列表。
  useEffect(() => {
    if (!open) return undefined;
    return subscribeToEvent('model-routing:update', () => load().catch(() => {}));
  }, [open, load]);

  const run = async (operation) => {
    setBusy(true);
    setError('');
    try {
      await operation();
      await load();
      return true;
    } catch (runError) {
      setError(runError?.message || '操作失败');
      return false;
    } finally {
      setBusy(false);
    }
  };

  const saveBinding = async (assignmentId, payload) => {
    const ok = await run(() => (assignmentId ? updateModelRouteBinding(assignmentId, payload) : createModelRouteBinding(payload)));
    if (ok) setDialogTarget(null);
  };

  const patchBinding = (binding, patch) =>
    run(() => updateModelRouteBinding(binding.assignment_id, patchBindingPayload(binding, patch)));

  const removeBinding = async (binding) => {
    const ok = await confirm({
      title: '删除模型映射',
      message: `确定删除模型「${binding.model}」的映射吗？删除后该模型的请求会返回未分配错误。`,
      confirmText: '确认删除',
      danger: true
    });
    if (!ok) return;
    run(() => deleteModelRouteBinding(binding.assignment_id));
  };

  const toggleRouting = async (nextEnabled) => {
    // 后端只把启用中的映射放进匹配表，全部停用等价于没有映射，同样要提醒。
    const enabledCount = bindings.filter((binding) => binding.enabled).length;
    if (nextEnabled && enabledCount === 0) {
      const hint = bindings.length > 0
        ? `当前 ${bindings.length} 条映射全部处于停用状态，开启后所有 Claude 请求都会返回未分配错误。仍要开启吗？`
        : '还没有任何模型映射，开启后所有 Claude 请求都会返回未分配错误。仍要开启吗？';
      if (!(await confirm({ title: '开启模型路由', message: hint, confirmText: '仍要开启' }))) return;
    }
    run(() => updateModelRoutingSettings({ claude_enabled: nextEnabled }));
  };

  const enabled = settings?.claude_enabled === true;
  const loadFailed = settings?.status === 'load_failed';
  const nav = typeof document === 'undefined' ? null : document.querySelector('nav.sticky');
  const topOffset = nav ? nav.getBoundingClientRect().bottom : 0;
  const summary = useMemo(() => {
    const active = bindings.filter((binding) => binding.enabled).length;
    return `${bindings.length} 个模型${bindings.length > active ? ` · ${bindings.length - active} 个已停用` : ''}`;
  }, [bindings]);

  if (!open) return null;

  return createPortal(
    <div className="fixed inset-0 z-[46] flex justify-end" style={{ top: topOffset }}>
      <button type="button" aria-label="关闭模型路由" className="absolute inset-0 bg-overlay backdrop-blur-[2px]" onClick={onClose} />
      <aside
        role="dialog"
        aria-modal="true"
        aria-label="Claude 模型路由"
        className="relative z-10 flex h-full w-full max-w-[880px] flex-col border-l border-line bg-surface shadow-2xl"
      >
        <div className="border-b border-line-soft bg-gradient-to-r from-accent-soft/60 via-surface to-surface px-6 py-5">
          <div className="flex items-start justify-between gap-4">
            <div className="flex items-start gap-3">
              <div className="tone-indigo flex h-10 w-10 items-center justify-center rounded-xl shadow-sm"><Route size={20} /></div>
              <div>
                <h2 className="text-lg font-semibold text-fg">Claude 模型路由</h2>
                <p className="mt-0.5 text-sm text-fg-muted">为每个模型单独指定可用的上游端点，未配置的模型不会回退到全局调度。</p>
              </div>
            </div>
            <button
              type="button"
              ref={closeButtonRef}
              aria-label="关闭抽屉"
              onClick={onClose}
              className="rounded-lg border border-line bg-surface p-2 text-fg-subtle transition-colors hover:border-line-strong hover:text-fg-body"
            >
              <X size={16} />
            </button>
          </div>

          <div className="mt-4 flex items-center justify-between rounded-xl border border-line bg-surface px-4 py-3">
            <div>
              <div className="text-sm font-medium text-fg">启用模型路由</div>
              <div className="mt-0.5 text-xs text-fg-muted">
                {enabled ? '已生效：Claude 请求按模型选择端点，端点页的全局手动路由暂不参与。' : '关闭中：Claude 请求按端点页的全局调度策略执行。'}
              </div>
            </div>
            <ToggleSwitch enabled={enabled} disabled={busy || loading} size="md" title="启用 Claude 模型路由" onChange={() => toggleRouting(!enabled)} />
          </div>
        </div>

        <div className="flex-1 space-y-4 overflow-y-auto px-6 py-5">
          {loadFailed && (
            <div className="tone-rose flex items-start gap-2 rounded-xl border px-3 py-2 text-xs">
              <AlertTriangle size={14} className="mt-0.5 shrink-0" />
              <div>
                <div className="font-semibold">配置加载失败</div>
                {enabled
                  ? '模型路由仍处于启用状态，为避免绕过模型隔离，所有 Claude 请求都会返回 422。请修复配置后重新加载。'
                  : '模型路由当前关闭，请求按全局端点策略调度。修复数据库配置后重新加载。'}
              </div>
            </div>
          )}

          {error && (
            <div className="tone-rose flex items-start gap-2 rounded-xl border px-3 py-2 text-xs">
              <AlertTriangle size={14} className="mt-0.5 shrink-0" />
              <span className="flex-1">{error}</span>
              <button type="button" onClick={load} className="shrink-0 font-medium underline">重试</button>
            </div>
          )}

          <div className="flex items-center justify-between gap-3">
            <div>
              <h3 className="text-sm font-semibold text-fg">模型映射</h3>
              <p className="mt-0.5 text-xs text-fg-subtle">{loading ? '加载中...' : summary}</p>
            </div>
            <div className="flex items-center gap-2">
              <Button variant="ghost" size="sm" icon={RefreshCw} onClick={load} loading={loading}>刷新</Button>
              <Button size="sm" icon={Plus} disabled={busy} onClick={() => setDialogTarget({})}>添加模型</Button>
            </div>
          </div>

          {loading && bindings.length === 0 ? (
            <div className="rounded-xl border border-line py-10"><LoadingSpinner text="加载模型映射..." /></div>
          ) : bindings.length === 0 ? (
            <button
              type="button"
              onClick={() => setDialogTarget({})}
              className="w-full rounded-xl border border-dashed border-line bg-surface-sub px-4 py-10 text-center transition-colors hover:border-accent-line hover:bg-accent-soft/40"
            >
              <Route size={28} className="mx-auto text-fg-subtle/60" />
              <div className="mt-2 text-sm font-medium text-fg-body">还没有模型映射</div>
              <div className="mt-1 text-xs text-fg-subtle">添加一个模型，并勾选它可以使用的上游端点。</div>
            </button>
          ) : (
            <div className="divide-y divide-line-soft overflow-hidden rounded-xl border border-line bg-surface shadow-sm">
              {bindings.map((binding) => (
                <ModelBindingRow
                  key={binding.assignment_id}
                  binding={binding}
                  busy={busy}
                  onEdit={() => setDialogTarget(binding)}
                  onDelete={() => removeBinding(binding)}
                  onPatch={(patch) => patchBinding(binding, patch)}
                />
              ))}
            </div>
          )}

          <RouteTestBox onTest={testModelRouting} />
        </div>
      </aside>

      {dialogTarget && (
        <ModelBindingDialog
          // key 让编辑不同模型时重新挂载，草稿状态天然回到初始值，不需要 effect 同步。
          key={dialogTarget.assignment_id ?? 'new'}
          binding={dialogTarget.assignment_id ? dialogTarget : null}
          bindings={bindings}
          endpoints={endpoints}
          busy={busy}
          onSave={saveBinding}
          onClose={() => setDialogTarget(null)}
        />
      )}
      {confirmDialog}
    </div>,
    document.body
  );
};

export default ModelRoutingDrawer;
