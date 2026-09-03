// ============================================
// 模型路由：模型映射编辑弹窗
// 2026-09-03
// ============================================

import { useMemo, useRef, useState } from 'react';
import { createPortal } from 'react-dom';
import { AlertTriangle, Copy, X } from 'lucide-react';
import { Button, CustomSelect } from '@components/ui';
import useModalLifecycle from '@hooks/useModalLifecycle.js';
import {
  ROUTE_MODES,
  bindingPayload,
  createBindingDraft,
  endpointNames,
  setDraftRouteMode,
  toggleDraftEndpoint,
  normalizeDraftTarget,
  validateBindingDraft
} from '../../utils/modelRouting.js';
import EndpointPicker from './EndpointPicker.jsx';
import ToggleSwitch from '../ToggleSwitch.jsx';

const fieldClass =
  'w-full rounded-lg border border-line bg-surface px-3 py-2 text-sm text-fg-body outline-none focus:border-accent-line';
// label 必须是 block：inline 的 <label> 行框会被 body 的 strut(1.5) 撑到 24px，
// 和 block 的 <div>(text-xs = 16px) 混用会让同一行的左右两列错开 8px。
const labelClass = 'block text-xs font-medium text-fg-muted';

// 调用方以 key 控制挂载，因此草稿只需在挂载时初始化一次，
// 不需要 effect 把 prop 同步回 state（那会引发级联渲染）。
const ModelBindingDialog = ({ binding, bindings = [], endpoints = [], busy = false, onSave, onClose }) => {
  const [draft, setDraft] = useState(() => createBindingDraft(binding));
  const [error, setError] = useState('');
  const modelInputRef = useRef(null);

  useModalLifecycle({ open: true, onClose, initialFocusRef: modelInputRef });

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

  return createPortal(
    <div className="fixed inset-0 z-[60] flex items-start justify-center px-4 pt-[12vh]">
      <button type="button" aria-label="关闭编辑弹窗" className="absolute inset-0 bg-overlay backdrop-blur-[2px]" onClick={onClose} />
      <div
        role="dialog"
        aria-modal="true"
        aria-label={draft.assignmentId ? '编辑模型映射' : '新建模型映射'}
        className="relative flex max-h-[76vh] w-full max-w-2xl flex-col overflow-hidden rounded-2xl border border-line bg-surface shadow-2xl"
      >
        <div className="flex items-center justify-between border-b border-line-soft px-5 py-4">
          <div>
            <h3 className="font-semibold text-fg">{draft.assignmentId ? '编辑模型映射' : '新建模型映射'}</h3>
            <p className="mt-0.5 text-xs text-fg-muted">该模型的 Claude 请求只会在这里选中的端点之间调度。</p>
          </div>
          <button
            type="button"
            aria-label="关闭"
            onClick={onClose}
            className="rounded-lg border border-line bg-surface p-1.5 text-fg-subtle hover:border-line-strong hover:text-fg-body"
          >
            <X size={15} />
          </button>
        </div>

        <div className="flex-1 space-y-4 overflow-y-auto px-5 py-4">
          <div className="grid gap-3 sm:grid-cols-[1fr_200px]">
            <div>
              <label className={labelClass} htmlFor="model-binding-name">模型名（精确匹配，大小写不敏感）</label>
              <input
                id="model-binding-name"
                ref={modelInputRef}
                className={`${fieldClass} mt-1 font-mono`}
                value={draft.model}
                disabled={busy}
                onChange={(event) => patch({ ...draft, model: event.target.value })}
                placeholder="claude-opus-4-5"
              />
            </div>
            <div>
              <div className={labelClass}>从现有模型复制端点</div>
              {/* 动作型下拉：value 恒为空，选中即执行复制并回到占位文案。 */}
              <CustomSelect
                options={copySources.map((item) => ({
                  value: item.assignment_id,
                  label: `${item.model}（${item.endpoints.length} 个端点）`
                }))}
                value=""
                disabled={busy || copySources.length === 0}
                size="md"
                placeholder={copySources.length === 0 ? '暂无可复制的模型' : '选择模型...'}
                className="mt-1 w-full"
                onChange={copyFrom}
              />
            </div>
          </div>

          <div>
            <div className="mb-1 flex items-center justify-between">
              <span className={labelClass}>上游端点</span>
              {copySources.length > 0 && (
                <span className="inline-flex items-center gap-1 text-[11px] text-fg-subtle">
                  <Copy size={11} />每个模型持有自己的端点集合，复制后互不影响
                </span>
              )}
            </div>
            <EndpointPicker
              endpoints={endpoints}
              selected={draft.endpoints}
              disabled={busy}
              onToggle={(name) => patch(toggleDraftEndpoint(draft, name))}
              onReplace={(names) => patch(normalizeDraftTarget({ ...draft, endpoints: names }))}
            />
            {missingEndpoints.length > 0 && (
              <div className="tone-rose mt-2 flex items-start gap-1.5 rounded-lg border px-2.5 py-1.5 text-[11px]">
                <AlertTriangle size={12} className="mt-0.5 shrink-0" />
                <span>端点 {missingEndpoints.join('、')} 已不存在，保存前请取消选择。</span>
              </div>
            )}
          </div>

          <div className="grid gap-3 sm:grid-cols-[200px_1fr]">
            <div>
              <div className={labelClass}>调度策略</div>
              <CustomSelect
                options={ROUTE_MODES}
                value={draft.routeMode}
                disabled={busy}
                size="md"
                className="mt-1 w-full"
                onChange={(mode) => patch(setDraftRouteMode(draft, mode))}
              />
            </div>
            <div>
              <div className={labelClass}>目标端点</div>
              <CustomSelect
                options={draft.endpoints.map((name) => ({ value: name, label: name }))}
                value={draft.routeTarget}
                disabled={busy || draft.routeMode === 'auto'}
                size="md"
                placeholder={draft.routeMode === 'auto' ? '自动模式无需目标' : '选择目标端点'}
                className="mt-1 w-full"
                onChange={(routeTarget) => patch({ ...draft, routeTarget })}
              />
            </div>
          </div>
          <p className="text-[11px] text-fg-subtle">
            {ROUTE_MODES.find((mode) => mode.value === draft.routeMode)?.hint}
          </p>

          <div className="grid gap-3 sm:grid-cols-[1fr_auto] sm:items-end">
            <div>
              <label className={labelClass} htmlFor="model-binding-note">备注（可选）</label>
              <input
                id="model-binding-note"
                className={`${fieldClass} mt-1`}
                value={draft.note}
                disabled={busy}
                onChange={(event) => patch({ ...draft, note: event.target.value })}
                placeholder="例如：日常主力，走高配通道"
              />
            </div>
            <div className="flex items-center gap-2 pb-2.5">
              <ToggleSwitch
                enabled={draft.enabled}
                disabled={busy}
                title="启用该映射"
                onChange={() => patch({ ...draft, enabled: !draft.enabled })}
              />
              <span className="text-xs text-fg-body">{draft.enabled ? '已启用' : '已停用'}</span>
            </div>
          </div>

          {error && (
            <div className="tone-rose flex items-start gap-1.5 rounded-lg border px-3 py-2 text-xs">
              <AlertTriangle size={13} className="mt-0.5 shrink-0" />
              {error}
            </div>
          )}
        </div>

        <div className="flex justify-end gap-2 border-t border-line-soft px-5 py-3">
          <Button variant="ghost" size="sm" onClick={onClose} disabled={busy}>取消</Button>
          <Button size="sm" onClick={submit} loading={busy}>保存</Button>
        </div>
      </div>
    </div>,
    document.body
  );
};

export default ModelBindingDialog;
