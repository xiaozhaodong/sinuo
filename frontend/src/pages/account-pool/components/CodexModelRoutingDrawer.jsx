import { useCallback, useEffect, useState } from 'react';
import { AlertTriangle, Pencil, Plus, RefreshCw, Route, Trash2 } from 'lucide-react';
import { Button, CustomSelect, LoadingSpinner } from '@components/ui';
import { FormBlock, FormRow, FormSection } from '@components/desktop/FormSection.jsx';
import ListEmpty from '@components/desktop/ListEmpty.jsx';
import SidePanel from '@components/desktop/SidePanel.jsx';
import Switch from '@components/desktop/Switch.jsx';
import useConfirm from '@hooks/useConfirm.js';
import { subscribeToEvent } from '@utils/wailsApi.js';
import { createCodexModelRouteBinding, deleteCodexModelRouteBinding, fetchCodexModelRouteBindings, fetchModelRoutingSettings, testCodexModelRouting, updateCodexModelRouteBinding, updateModelRoutingSettings } from '@utils/api.js';
import CodexRouteTestBox from './CodexRouteTestBox.jsx';
import CodexBindingEditor, { MODES, accountId, accountName } from './CodexBindingEditor.jsx';

const CodexModelRoutingDrawer = ({
  open,
  onClose,
  accounts = []
}) => {
  const [settings, setSettings] = useState({
    codex_enabled: false
  });
  const [bindings, setBindings] = useState([]);
  const [loading, setLoading] = useState(true);
  const {
    confirm,
    confirmDialog
  } = useConfirm();
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  const [editor, setEditor] = useState(null);
  const load = useCallback(async () => {
    setError('');
    try {
      const [nextSettings, nextBindings] = await Promise.all([fetchModelRoutingSettings(), fetchCodexModelRouteBindings()]);
      setSettings(nextSettings || {});
      setBindings(nextBindings || []);
    } catch (err) {
      setError(err.message || '加载 Codex 模型路由失败');
    } finally {
      setLoading(false);
    }
  }, []);
  useEffect(() => {
    if (open) {
      setLoading(true);
      load();
    }
  }, [open, load]);
  useEffect(() => {
    if (!open) return undefined;
    return subscribeToEvent('model-routing:update', () => load().catch(() => {}));
  }, [open, load]);
  if (!open) return null;
  const run = async operation => {
    setBusy(true);
    setError('');
    try {
      await operation();
      await load();
      return true;
    } catch (err) {
      setError(err.message || '操作失败');
      return false;
    } finally {
      setBusy(false);
    }
  };
  const toggle = async () => {
    if (!settings.codex_enabled && bindings.filter(binding => binding.enabled).length === 0 && !(await confirm({
      title: '开启 Codex 模型路由',
      message: '当前没有启用中的 Codex 模型路由，开启后未绑定模型会返回 422。仍要开启吗？',
      confirmText: '仍要开启'
    }))) return;
    return run(() => updateModelRoutingSettings({
      codex_enabled: !settings.codex_enabled
    }));
  };
  const save = async (assignmentId, payload) => {
    const ok = await run(() => assignmentId ? updateCodexModelRouteBinding(assignmentId, payload) : createCodexModelRouteBinding(payload));
    if (ok) setEditor(null);
  };
  const patchBinding = (binding, patch) => {
    const next = {
      ...binding,
      ...patch
    };
    const targetId = next.route_mode === 'auto' ? 0 : Number(next.route_target_account_id) || Number(next.account_ids?.[0]) || 0;
    return run(() => updateCodexModelRouteBinding(binding.assignment_id, {
      model: next.model,
      account_ids: next.account_ids,
      member_enabled: next.member_enabled,
      route_mode: next.route_mode,
      route_target_account_id: targetId,
      enabled: next.enabled,
      note: next.note
    }));
  };
  const remove = async binding => {
    if (!(await confirm({
      title: '删除模型路由',
      message: `确定删除模型「${binding.model}」的路由吗？`,
      confirmText: '确认删除',
      danger: true
    }))) return;
    run(() => deleteCodexModelRouteBinding(binding.assignment_id));
  };
  const accountById = new Map(accounts.map(account => [accountId(account), account]));
  return (
    <>
      <SidePanel
        open={open}
        onClose={onClose}
        label="Codex 模型路由"
        title="Codex 模型路由"
        subtitle="为每个模型指定账号集合、顺序和固定策略。"
        width={880}
        layer={46}
        closeOnOverlay={true}
        overlayLabel="关闭 Codex 模型路由"
      >
        <FormSection title="路由开关">
          <FormRow
            label="启用 Codex 模型路由"
            description={settings.codex_enabled
              ? '已生效：Codex 请求按模型绑定选择账号。'
              : '关闭中：Codex 请求继续使用全局账号池调度。'}
          >
            <Switch checked={settings.codex_enabled === true} disabled={busy || loading} label="启用 Codex 模型路由" onChange={toggle} />
          </FormRow>
        </FormSection>

        {error && (
          <div className="mt-5 flex items-start gap-2 rounded-lg border border-danger-line bg-danger-soft px-3 py-2 text-xs text-danger">
            <AlertTriangle size={14} className="mt-0.5 shrink-0" />
            <span className="flex-1">{error}</span>
            <button type="button" onClick={load} className="underline">重试</button>
          </div>
        )}
        {settings.codex_enabled && bindings.filter((binding) => binding.enabled).length === 0 && (
          <div className="mt-5 flex items-start gap-2 rounded-lg border border-warn-line bg-warn-soft px-3 py-2 text-xs text-warn">
            <AlertTriangle size={14} className="mt-0.5 shrink-0" />
            开启后未绑定模型不会回落到全局账号池。
          </div>
        )}

        <FormSection
          title="模型绑定"
          description={loading ? '加载中...' : `${bindings.length} 个模型 · ${bindings.filter((binding) => binding.enabled).length} 个已启用`}
          actions={(
            <>
              <Button variant="ghost" size="sm" icon={RefreshCw} onClick={load} loading={loading}>刷新</Button>
              <Button size="sm" icon={Plus} disabled={busy} onClick={() => setEditor({})}>添加模型</Button>
            </>
          )}
        >
          {loading && bindings.length === 0 ? (
            <FormBlock><LoadingSpinner text="加载模型绑定..." /></FormBlock>
          ) : bindings.length === 0 ? (
            <button type="button" onClick={() => setEditor({})} className="block w-full rounded-[10px] hover:bg-surface-sub">
              <ListEmpty icon={Route} title="还没有模型绑定" description="添加模型并选择账号后，可在行内切换自动、优选或固定。" />
            </button>
          ) : bindings.map((binding) => (
            <div key={binding.assignment_id} className="px-3.5 py-2">
              <div className="flex flex-wrap items-center gap-2">
                <div className="min-w-[180px] flex-1">
                  <div className={`truncate font-mono text-[13px] ${binding.enabled ? 'text-fg-body' : 'text-fg-muted line-through'}`}>
                    {binding.model}
                  </div>
                  {binding.note && <div className="mt-0.5 truncate text-[11.5px] text-fg-muted">{binding.note}</div>}
                </div>
                <CustomSelect
                  options={MODES}
                  value={binding.route_mode}
                  disabled={busy}
                  size="sm"
                  className="w-[100px] shrink-0"
                  onChange={(route_mode) => patchBinding(binding, { route_mode })}
                />
                <CustomSelect
                  options={(binding.account_ids || []).map((id) => ({ value: id, label: `${accountName(accountById.get(id))} (#${id})` }))}
                  value={binding.route_target_account_id || ''}
                  disabled={busy || binding.route_mode === 'auto'}
                  size="sm"
                  className="w-[170px] shrink-0"
                  placeholder={binding.route_mode === 'auto' ? '自动挑选' : '选择目标账号'}
                  onChange={(value) => patchBinding(binding, { route_target_account_id: Number(value) })}
                />
                <button type="button" title="编辑模型与账号" disabled={busy} onClick={() => setEditor(binding)}
                  className="inline-flex h-6 w-6 items-center justify-center rounded text-fg-muted hover:bg-surface-mut hover:text-fg disabled:opacity-50">
                  <Pencil size={14} />
                </button>
                <button type="button" title="删除模型绑定" disabled={busy} onClick={() => remove(binding)}
                  className="inline-flex h-6 w-6 items-center justify-center rounded text-fg-muted hover:bg-surface-mut hover:text-danger disabled:opacity-50">
                  <Trash2 size={14} />
                </button>
              </div>
              <div className="mt-1.5 flex flex-wrap items-center gap-1.5">
                {(binding.account_ids || []).map((id, index) => (
                  <span key={id} className={`rounded border px-1.5 text-[11px] leading-[18px] ${id === binding.route_target_account_id ? 'border-accent-line bg-accent-soft text-accent-fg' : 'border-line bg-pane text-fg-muted'}`}>
                    {index + 1}. {accountName(accountById.get(id))}
                    {id === binding.route_target_account_id && binding.route_mode !== 'auto' ? ' · 目标' : ''}
                  </span>
                ))}
                {binding.last_account_id ? (
                  <span className="ml-1 text-[11px] text-fg-muted">
                    最近命中：{accountName(accountById.get(binding.last_account_id))} · {binding.last_outcome || '已完成'}
                  </span>
                ) : null}
              </div>
            </div>
          ))}
        </FormSection>
        <CodexRouteTestBox onTest={testCodexModelRouting} />
      </SidePanel>
      {editor && <CodexBindingEditor binding={editor.assignment_id ? editor : null} accounts={accounts} bindings={bindings} busy={busy} onSave={save} onClose={() => setEditor(null)} />}
      {confirmDialog}
    </>
  );
};

export default CodexModelRoutingDrawer;
