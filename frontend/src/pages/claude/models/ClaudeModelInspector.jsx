import { useEffect, useState } from 'react';
import { AlertCircle, Trash2 } from 'lucide-react';
import { Button } from '@components/ui';
import { ToolbarButton } from '@components/layout/ToolbarButton.jsx';
import {
  Inspector,
  InspectorBody,
  InspectorFooter,
  InspectorHeader,
  InspectorSection,
  Property,
  PropertyList
} from '@components/desktop/Inspector.jsx';
import { resolveErrorMessage } from '@utils/errors.js';
import { validateClaudeModelDraft } from './claudeModelCatalog.js';

const ClaudeModelInspector = ({ model, models = [], isNew, saving, onSave, onCancelNew, onDelete, onDirtyChange }) => {
  const [base] = useState(() => ({
    id: model?.id || '',
    display_name: model?.display_name || ''
  }));
  const [draft, setDraft] = useState(base);
  const [errors, setErrors] = useState({});
  const [submitError, setSubmitError] = useState(null);
  const dirty = isNew || draft.id !== base.id || draft.display_name !== base.display_name;

  useEffect(() => {
    onDirtyChange?.(dirty);
  }, [dirty, onDirtyChange]);

  const update = (field, value) => {
    setDraft((current) => ({ ...current, [field]: value }));
    if (errors[field]) setErrors((current) => ({ ...current, [field]: undefined }));
    setSubmitError(null);
  };

  const submit = async (event) => {
    event.preventDefault();
    const nextErrors = validateClaudeModelDraft(draft, models, isNew ? '' : base.id);
    setErrors(nextErrors);
    if (Object.keys(nextErrors).length > 0) return;
    setSubmitError(null);
    try {
      await onSave(draft, isNew ? '' : base.id);
    } catch (error) {
      setSubmitError(resolveErrorMessage(error, '保存模型失败'));
    }
  };

  const title = isNew ? '添加模型' : (model.display_name || model.id);
  const subtitle = isNew ? '保存后出现在 Claude Code 的 /model 列表' : model.id;

  return (
    <Inspector label="模型详情">
      <form onSubmit={submit} className="flex min-h-0 flex-1 flex-col">
        <InspectorHeader
          title={title}
          subtitle={subtitle}
          badges={!isNew && !model.enabled && (
            <span className="shrink-0 rounded border border-line px-1.5 text-[11px] leading-[18px] text-fg-muted">已停用</span>
          )}
        />
        <InspectorBody>
          {submitError && (
            <div role="alert" className="selectable mx-4 mt-3 flex items-start gap-2 rounded-md border border-danger-line bg-danger-soft px-2.5 py-2 text-xs text-danger">
              <AlertCircle size={14} className="mt-px shrink-0" />
              <span>{submitError}</span>
            </div>
          )}
          <InspectorSection title="基本信息">
            <PropertyList>
              <Property label="模型 ID" truncate={false}>
                <input
                  className="desk-field w-full font-mono"
                  value={draft.id}
                  onChange={(event) => update('id', event.target.value)}
                  placeholder="例如 deepseek-v4-flash[1m]"
                  spellCheck={false}
                  maxLength={256}
                  autoFocus={isNew}
                  aria-invalid={Boolean(errors.id)}
                />
              </Property>
              <Property label="显示名称" truncate={false}>
                <input
                  className="desk-field w-full"
                  value={draft.display_name}
                  onChange={(event) => update('display_name', event.target.value)}
                  placeholder="留空时使用模型 ID"
                  maxLength={256}
                  aria-invalid={Boolean(errors.name)}
                />
              </Property>
            </PropertyList>
            {(errors.id || errors.name) && (
              <div className="mt-2 pl-[94px] text-[11.5px] text-danger">
                {errors.id || errors.name}
              </div>
            )}
          </InspectorSection>
          {!isNew && (
            <InspectorSection>
              <p className="text-[11.5px] leading-relaxed text-fg-subtle">是否出现在 /model 列表由表格的「启用」勾选控制，勾选后立即生效。</p>
            </InspectorSection>
          )}
        </InspectorBody>
        <InspectorFooter>
          {!isNew && <ToolbarButton icon={Trash2} title="删除模型" disabled={saving} onClick={() => onDelete(model)} />}
          <span className="flex-1" />
          {isNew ? (
            <Button type="button" variant="secondary" onClick={onCancelNew} disabled={saving}>取消</Button>
          ) : (
            <Button
              type="button"
              variant="secondary"
              onClick={() => { setDraft(base); setErrors({}); setSubmitError(null); }}
              disabled={!dirty || saving}
            >
              还原
            </Button>
          )}
          <Button type="submit" loading={saving} disabled={!dirty}>{isNew ? '添加' : '保存'}</Button>
        </InspectorFooter>
      </form>
    </Inspector>
  );
};

export default ClaudeModelInspector;
