// ============================================
// 隐私规则编辑抽屉
// 2026-06-11 (v6.1 新增)
// ============================================

import { useId, useState } from 'react';
import { Button } from '@components/ui';
import { FormBlock, FormFieldRow, FormRow, FormSection } from '@components/desktop/FormSection.jsx';
import SegmentedControl from '@components/desktop/SegmentedControl.jsx';
import SidePanel from '@components/desktop/SidePanel.jsx';
import Switch from '@components/desktop/Switch.jsx';
import PrivacyPatternEditor from './PrivacyPatternEditor.jsx';
import PrivacyScopeEditor from './PrivacyScopeEditor.jsx';
import {
  PRIVACY_ACTION_OPTIONS,
  ruleToForm,
  validatePrivacyRuleForm
} from '../utils/privacyRules.js';

// 注意：调用方通过 key 在每次打开时重挂载本组件，初始 state 直接取自 rule
const PrivacyRuleDrawer = ({
  open,
  rule,
  saving,
  onSave,
  onClose,
  endpointOptions,
  accountOptions
}) => {
  const [form, setForm] = useState(() => ruleToForm(rule || {}));
  const [errors, setErrors] = useState({});
  const [submitError, setSubmitError] = useState('');
  const isBuiltin = form.match_type === 'builtin';
  const formId = useId();
  const handleRequestClose = () => {
    if (!saving) onClose();
  };

  if (!open) return null;

  const update = (patch) => {
    setForm((prev) => ({ ...prev, ...patch }));
    setErrors((prev) => {
      const next = { ...prev };
      Object.keys(patch).forEach((key) => delete next[key]);
      return next;
    });
  };

  const handleSubmit = async (e) => {
    e.preventDefault();
    const nextErrors = validatePrivacyRuleForm(form);
    setErrors(nextErrors);
    if (Object.keys(nextErrors).length > 0) return;

    setSubmitError('');
    try {
      await onSave(form);
    } catch (err) {
      // 后端编译失败等错误在抽屉内提示，不丢失用户输入
      setSubmitError(err.message || String(err));
    }
  };

  return (
    <SidePanel
      open={open}
      onClose={handleRequestClose}
      label={isBuiltin ? '编辑内置规则' : (form.id > 0 ? '编辑规则' : '新增规则')}
      title={isBuiltin ? '编辑内置规则' : (form.id > 0 ? '编辑规则' : '新增规则')}
      width={576}
      layer={45}
      closeOnOverlay={true}
      footer={(
        <>
          {submitError && <p className="min-w-0 flex-1 break-all text-xs text-danger">{submitError}</p>}
          <div className="ml-auto flex shrink-0 gap-2">
            <Button type="button" variant="secondary" onClick={handleRequestClose} disabled={saving}>取消</Button>
            <Button type="submit" form={formId} loading={saving}>保存并生效</Button>
          </div>
        </>
      )}
    >
      <form id={formId} onSubmit={handleSubmit}>
        {isBuiltin ? (
          <FormSection title="内置规则">
            <FormBlock>
              <div className="text-[13px] font-semibold text-fg">{form.name}</div>
              {form.description && <p className="mt-1 text-xs leading-relaxed text-fg-muted">{form.description}</p>}
              <div className="mt-1 text-xs text-fg-muted">优先级 {form.priority}</div>
            </FormBlock>
          </FormSection>
        ) : (
          <FormSection title="规则信息">
            <FormFieldRow label="规则名 *" htmlFor={`${formId}-name`} error={errors.name}>
              <input
                id={`${formId}-name`}
                value={form.name}
                aria-invalid={Boolean(errors.name)}
                onChange={(e) => update({ name: e.target.value })}
                placeholder="例如 OpenAI API Key"
                className="desk-field w-full"
              />
            </FormFieldRow>
            <FormFieldRow label="优先级（越小越先匹配）" htmlFor={`${formId}-priority`} error={errors.priority}>
              <input
                id={`${formId}-priority`}
                type="number"
                value={form.priority}
                aria-invalid={Boolean(errors.priority)}
                onChange={(e) => update({ priority: e.target.value })}
                className="desk-field w-24 text-right"
              />
            </FormFieldRow>
            <FormFieldRow label="描述" htmlFor={`${formId}-description`}>
              <textarea
                id={`${formId}-description`}
                value={form.description}
                onChange={(e) => update({ description: e.target.value })}
                rows={2}
                className="desk-field h-auto w-full py-1.5 leading-relaxed"
              />
            </FormFieldRow>
          </FormSection>
        )}

        <FormSection title="处理方式">
          <FormRow label="启用">
            <Switch checked={form.enabled} onChange={(enabled) => update({ enabled })} />
          </FormRow>
          <FormRow label="动作">
            <SegmentedControl options={PRIVACY_ACTION_OPTIONS} value={form.action} onChange={(action) => update({ action })} />
          </FormRow>
          {form.action === 'redact' && (
            <FormFieldRow label="占位符 *" htmlFor={`${formId}-placeholder`} error={errors.placeholder}>
              <input
                id={`${formId}-placeholder`}
                value={form.placeholder}
                aria-invalid={Boolean(errors.placeholder)}
                onChange={(e) => update({ placeholder: e.target.value })}
                placeholder="[已脱敏]"
                className="desk-field w-full"
              />
            </FormFieldRow>
          )}
        </FormSection>

        {isBuiltin ? (
          <FormSection title="内置规则 ID">
            <FormBlock>
              <div className="font-mono text-xs text-fg-body">{form.pattern}</div>
              <p className="mt-1 text-xs text-fg-muted">内置规则的匹配器由代码提供，只能调整启用、动作、占位符和作用域。</p>
            </FormBlock>
          </FormSection>
        ) : (
          <FormSection title="匹配规则">
            <FormBlock>
              <PrivacyPatternEditor
                matchType={form.match_type}
                pattern={form.pattern}
                error={errors.pattern}
                onMatchTypeChange={(value) => update({ match_type: value })}
                onPatternChange={(value) => update({ pattern: value })}
              />
            </FormBlock>
          </FormSection>
        )}

        <FormSection title="作用域">
          <FormBlock>
            <PrivacyScopeEditor
              scope={form.scope}
              onChange={(scope) => update({ scope })}
              endpointOptions={endpointOptions}
              accountOptions={accountOptions}
            />
          </FormBlock>
        </FormSection>
      </form>
    </SidePanel>
  );
};

export default PrivacyRuleDrawer;
