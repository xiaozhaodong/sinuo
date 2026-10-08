// ============================================
// 模型路由抽屉：模型 → 端点集合的编排入口
// 2026-09-03
// ============================================

import { useCallback, useEffect, useMemo, useState } from 'react';
import { AlertTriangle, Plus, RefreshCw, Route } from 'lucide-react';
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
import useConfirm from '@hooks/useConfirm.js';
import { isWebToolsBinding, patchBindingPayload } from '../../utils/modelRouting.js';
import ModelBindingDialog from './ModelBindingDialog.jsx';
import ModelBindingRow from './ModelBindingRow.jsx';
import RouteTestBox from './RouteTestBox.jsx';
import { FormBlock, FormRow, FormSection } from '@components/desktop/FormSection.jsx';
import ListEmpty from '@components/desktop/ListEmpty.jsx';
import SidePanel from '@components/desktop/SidePanel.jsx';
import Switch from '@components/desktop/Switch.jsx';

const ModelRoutingDrawer = ({ open, onClose, endpoints = [] }) => {
  const [settings, setSettings] = useState(null);
  const [bindings, setBindings] = useState([]);
  const [loading, setLoading] = useState(true);
  const { confirm, confirmDialog } = useConfirm();
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  const [dialogTarget, setDialogTarget] = useState(null);

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
      title: isWebToolsBinding(binding) ? '删除联网工具映射' : '删除模型映射',
      message: isWebToolsBinding(binding)
        ? '确定删除联网工具映射吗？删除后带 web_search / web_fetch 的请求会改按模型映射调度。'
        : `确定删除模型「${binding.model}」的映射吗？删除后该模型的请求会返回未分配错误。`,
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
  const summary = useMemo(() => {
    const active = bindings.filter((binding) => binding.enabled).length;
    const models = bindings.filter((binding) => !isWebToolsBinding(binding)).length;
    const webTools = bindings.length - models;
    return `${models} 个模型${webTools > 0 ? ' · 联网工具' : ''}${bindings.length > active ? ` · ${bindings.length - active} 个已停用` : ''}`;
  }, [bindings]);

  if (!open) return null;

  return (
    <>
      <SidePanel
        open={open}
        onClose={onClose}
        label="Claude 模型路由"
        title="Claude 模型路由"
        subtitle="为每个模型单独指定可用的上游端点，未配置的模型不会回退到全局调度。"
        width={880}
        layer={46}
        closeOnOverlay={true}
        overlayLabel="关闭模型路由"
      >
        <FormSection title="路由开关">
          <FormRow label="启用模型路由" description={enabled
            ? '已生效：Claude 请求按模型选择端点，端点页的全局手动路由暂不参与。'
            : '关闭中：Claude 请求按端点页的全局调度策略执行。'}
          >
            <Switch checked={enabled} disabled={busy || loading} title="启用 Claude 模型路由" onChange={toggleRouting} />
          </FormRow>
        </FormSection>

        {loadFailed && (
          <div className="mt-5 flex items-start gap-2 rounded-lg border border-danger-line bg-danger-soft px-3 py-2 text-xs text-danger">
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
          <div className="mt-5 flex items-start gap-2 rounded-lg border border-danger-line bg-danger-soft px-3 py-2 text-xs text-danger">
            <AlertTriangle size={14} className="mt-0.5 shrink-0" />
            <span className="flex-1">{error}</span>
            <button type="button" onClick={load} className="shrink-0 font-medium underline">重试</button>
          </div>
        )}

        <FormSection
          title="模型映射"
          description={loading ? '加载中...' : summary}
          actions={(
            <>
              <Button variant="ghost" size="sm" icon={RefreshCw} onClick={load} loading={loading}>刷新</Button>
              <Button size="sm" icon={Plus} disabled={busy} onClick={() => setDialogTarget({})}>添加模型</Button>
            </>
          )}
        >
          {loading && bindings.length === 0 ? (
            <FormBlock><LoadingSpinner text="加载模型映射..." /></FormBlock>
          ) : bindings.length === 0 ? (
            <button type="button" onClick={() => setDialogTarget({})} className="block w-full rounded-[10px] hover:bg-surface-sub">
              <ListEmpty icon={Route} title="还没有模型映射" description="添加一个模型，并勾选它可以使用的上游端点。" />
            </button>
          ) : bindings.map((binding) => (
            <ModelBindingRow
              key={binding.assignment_id}
              binding={binding}
              busy={busy}
              onEdit={() => setDialogTarget(binding)}
              onDelete={() => removeBinding(binding)}
              onPatch={(patch) => patchBinding(binding, patch)}
            />
          ))}
        </FormSection>
        <RouteTestBox onTest={testModelRouting} />
      </SidePanel>

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
    </>
  );
};

export default ModelRoutingDrawer;
