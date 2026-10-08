// ============================================
// 模型路由：模型映射编辑弹窗
// 2026-09-03
// ============================================

import { useMemo, useRef, useState } from 'react';
import { AlertTriangle, Copy } from 'lucide-react';
import { Button, CustomSelect } from '@components/ui';
import Sheet from '@components/desktop/Sheet.jsx';
import Switch from '@components/desktop/Switch.jsx';
import { FormBlock, FormFieldRow, FormRow, FormSection } from '@components/desktop/FormSection.jsx';
import {
  BINDING_KINDS,
  ROUTE_MODES,
  bindingDisplayName,
  bindingPayload,
  createBindingDraft,
  endpointNames,
  setDraftRouteMode,
  toggleDraftEndpoint,
  normalizeDraftTarget,
  validateBindingDraft
} from '../../utils/modelRouting.js';
import EndpointPicker from './EndpointPicker.jsx';
// 调用方以 key 控制挂载，因此草稿只需在挂载时初始化一次，
// 不需要 effect 把 prop 同步回 state（那会引发级联渲染）。
const ModelBindingDialog = ({ binding, bindings = [], endpoints = [], busy = false, onSave, onClose }) => {
  const [draft, setDraft] = useState(() => createBindingDraft(binding));
  const [error, setError] = useState('');
  const modelInputRef = useRef(null);

  const noteInputRef = useRef(null);

  const isWebTools = draft.kind === 'web_tools';
  // 联网映射全局只有一条：已存在（且不是正在编辑的这条）时不再提供该选项。
  const webToolsTaken = bindings.some((item) => item.kind === 'web_tools' && item.assignment_id !== draft.assignmentId);
  const kindOptions = BINDING_KINDS.filter((kind) => kind.value !== 'web_tools' || isWebTools || !webToolsTaken);
  const availableNames = useMemo(() => new Set(endpoints.map((endpoint) => endpoint.name)), [endpoints]);
  const copySources = useMemo(
    () => bindings.filter((item) => item.assignment_id !== draft.assignmentId && item.endpoints.length > 0),
    [bindings, draft.assignmentId]
  );
  // 组是后端实现细节，但成员端点被删掉时用户必须看见——否则保存会撞上后端的存在性校验。
  const missingEndpoints = draft.endpoints.filter((name) => !availableNames.has(name));

  const patch = (next) => {
    setDraft(next);
    setError('');
  };

  const copyFrom = (assignmentId) => {
    const source = bindings.find((item) => item.assignment_id === Number(assignmentId));
    if (!source) return;
    // 只复制端点与策略，模型名保持用户正在输入的值。已删除的端点不带过来。
    patch(
      normalizeDraftTarget({
        ...draft,
        endpoints: endpointNames(source).filter((name) => availableNames.has(name)),
        routeMode: source.route_mode,
        routeTarget: source.route_target
      })
    );
  };

  const submit = async () => {
    const message = validateBindingDraft(draft, bindings);
    if (message) {
      setError(message);
      return;
    }
    try {
      await onSave(draft.assignmentId, bindingPayload(draft));
    } catch (saveError) {
      setError(saveError?.message || '保存模型映射失败');
    }
  };

  return (
    <Sheet title={draft.assignmentId ? '编辑模型映射' : '新建模型映射'}
      label={draft.assignmentId ? '编辑模型映射' : '新建模型映射'} width={640} closeLabel="关闭" overlayLabel="关闭编辑弹窗"
      subtitle={isWebTools ? '带 web_search / web_fetch 工具的 Claude 请求只会在这里选中的端点之间调度，优先于模型映射。' : '该模型的 Claude 请求只会在这里选中的端点之间调度。'}
      onClose={onClose} initialFocusRef={isWebTools ? noteInputRef : modelInputRef}
      footerStart={error && <span role="alert" className="flex items-start gap-2 text-danger"><AlertTriangle size={14} className="shrink-0" />{error}</span>}
      footer={<><Button variant="ghost" size="sm" onClick={onClose} disabled={busy}>取消</Button><Button size="sm" onClick={submit} loading={busy}>保存</Button></>}>
      <FormSection title="匹配方式">
        <FormFieldRow label="匹配方式">
          <CustomSelect options={kindOptions} value={draft.kind} disabled={busy || Boolean(draft.assignmentId)} size="sm" className="w-full"
            onChange={(kind) => patch({ ...draft, kind, model: kind === 'web_tools' ? '' : draft.model })} />
        </FormFieldRow>
        <FormFieldRow label={isWebTools ? '匹配条件' : '模型名（精确匹配，大小写不敏感）'} htmlFor="model-binding-name">
          <input id="model-binding-name" ref={modelInputRef} className="desk-field w-full font-mono"
            value={isWebTools ? 'tools[].type = web_search_* / web_fetch_*' : draft.model} disabled={busy || isWebTools} readOnly={isWebTools}
            onChange={(event) => patch({ ...draft, model: event.target.value })} placeholder="claude-opus-4-5" />
        </FormFieldRow>
        <FormFieldRow label="从现有模型复制端点">
          <CustomSelect options={copySources.map((item) => ({ value: item.assignment_id, label: `${bindingDisplayName(item)}（${item.endpoints.length} 个端点）` }))}
            value="" disabled={busy || copySources.length === 0} size="sm" placeholder={copySources.length === 0 ? '暂无可复制的模型' : '选择模型...'} className="w-full" onChange={copyFrom} />
        </FormFieldRow>
      </FormSection>
      <FormSection title="上游端点">
        {copySources.length > 0 && <FormBlock><span className="flex items-center gap-1 text-[11.5px] text-fg-muted"><Copy size={11} />每个模型持有自己的端点集合，复制后互不影响</span></FormBlock>}
        <FormBlock>
          <EndpointPicker endpoints={endpoints} selected={draft.endpoints} disabled={busy}
            onToggle={(name) => patch(toggleDraftEndpoint(draft, name))} onReplace={(names) => patch(normalizeDraftTarget({ ...draft, endpoints: names }))} />
          {missingEndpoints.length > 0 && <div role="alert" className="mt-2 flex items-start gap-2 rounded-lg border border-danger-line bg-danger-soft px-3 py-2 text-xs text-danger">
            <AlertTriangle size={14} className="shrink-0" /><span>端点 {missingEndpoints.join('、')} 已不存在，保存前请取消选择。</span>
          </div>}
        </FormBlock>
      </FormSection>
      <FormSection title="调度策略">
        <FormFieldRow label="调度策略" hint={ROUTE_MODES.find((mode) => mode.value === draft.routeMode)?.hint}>
          <CustomSelect options={ROUTE_MODES} value={draft.routeMode} disabled={busy} size="sm" className="w-full" onChange={(mode) => patch(setDraftRouteMode(draft, mode))} />
        </FormFieldRow>
        <FormFieldRow label="目标端点">
          <CustomSelect options={draft.endpoints.map((name) => ({ value: name, label: name }))} value={draft.routeTarget}
            disabled={busy || draft.routeMode === 'auto'} size="sm" placeholder={draft.routeMode === 'auto' ? '自动模式无需目标' : '选择目标端点'} className="w-full"
            onChange={(routeTarget) => patch({ ...draft, routeTarget })} />
        </FormFieldRow>
        {isWebTools && <FormBlock><p className="text-[11.5px] text-fg-muted">{BINDING_KINDS.find((kind) => kind.value === 'web_tools')?.hint}；没有这条映射时联网请求按模型映射正常调度。</p></FormBlock>}
        <FormFieldRow label="备注（可选）" htmlFor="model-binding-note">
          <input ref={noteInputRef} id="model-binding-note" className="desk-field w-full" value={draft.note} disabled={busy}
            onChange={(event) => patch({ ...draft, note: event.target.value })} placeholder="例如：日常主力，走高配通道" />
        </FormFieldRow>
        <FormRow label={draft.enabled ? '已启用' : '已停用'}>
          <Switch checked={draft.enabled} disabled={busy} title="启用该映射" label="启用该映射" onChange={(enabled) => patch({ ...draft, enabled })} />
        </FormRow>
      </FormSection>
    </Sheet>
  );
};

export default ModelBindingDialog;
