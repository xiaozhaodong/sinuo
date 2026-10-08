import SoftFailurePolicyFields from '@components/ui/SoftFailurePolicyFields.jsx';

import { useRef, useState } from 'react';
import { AlertCircle, ChevronDown, ChevronUp, Eye, EyeOff, Plus, Save, Trash2 } from 'lucide-react';
import { Button } from '@components/ui';
import Sheet from '@components/desktop/Sheet.jsx';
import Switch from '@components/desktop/Switch.jsx';
import { FormBlock, FormFieldRow, FormRow, FormSection } from '@components/desktop/FormSection.jsx';
import { createEmptyEndpointModelRewriteRule } from '../utils/modelRewrite.js';
import {
  buildEndpointFormPayload,
  createEndpointFormState,
  validateEndpointFormState
} from '../utils/endpointFormState.js';

const FormInput = ({ label, name, value, onChange, type = 'text', placeholder, disabled, help, min, step, error, inputRef, numberWidth = 'w-24' }) => (
  <FormFieldRow label={label} htmlFor={name} hint={help} error={error}>
    <input ref={inputRef} id={name} type={type} name={name} value={value ?? ''} onChange={onChange}
      placeholder={placeholder} disabled={disabled} min={min} step={step} aria-invalid={Boolean(error)}
      className={`desk-field ${type === 'number' ? `${numberWidth} text-right` : 'w-full'}`} />
  </FormFieldRow>
);

const PasswordInput = ({
  label, name, value, onChange, placeholder, help, disabled,
  showLabel = true, visibilityLabel = label
}) => {
  const [visible, setVisible] = useState(false);
  const input = (
    <span className="relative block">
      <input id={name} type={visible ? 'text' : 'password'} name={name} value={value || ''}
        onChange={onChange} placeholder={placeholder} disabled={disabled} className="desk-field w-full pr-8" />
      <button type="button" onClick={() => setVisible((current) => !current)}
        className="absolute right-0 top-1/2 inline-flex h-6 w-6 -translate-y-1/2 items-center justify-center rounded text-fg-muted hover:bg-surface-mut hover:text-fg"
        aria-label={visible ? `隐藏${visibilityLabel}` : `显示${visibilityLabel}`}>
        {visible ? <EyeOff size={14} /> : <Eye size={14} />}
      </button>
    </span>
  );
  return showLabel ? (
    <FormFieldRow label={label} htmlFor={name} hint={help}>{input}</FormFieldRow>
  ) : input;
};

const Checkbox = ({ label, help, checked, onChange }) => (
  <FormRow label={label} description={help}>
    <Switch label={label} checked={checked} onChange={onChange} />
  </FormRow>
);

const SecretField = ({ label, field, clearField, maskedValue, state, setState }) => {
  const hasStoredValue = Boolean(maskedValue);
  const cleared = state[clearField] === true;
  return (
    <FormFieldRow
      label={label}
      htmlFor={field}
      hint={cleared ? (
        <span className="text-warn">保存后会明确删除已存 {label}</span>
      ) : hasStoredValue ? (
        `当前已保存：${maskedValue}，留空表示保留当前值`
      ) : '当前未配置 · 前端不会读取或回填已保存的明文凭据'}
    >
      <div className="flex items-center gap-2">
        <div className="min-w-0 flex-1">
          <PasswordInput label={label} name={field} value={state[field]}
            showLabel={false}
            visibilityLabel={hasStoredValue ? `替换 ${label}` : label}
            onChange={(event) => setState((current) => ({ ...current, [field]: event.target.value, [clearField]: false }))}
            disabled={cleared} placeholder={hasStoredValue ? '留空表示保留当前值' : '可选'} />
        </div>
        {hasStoredValue && (
          <Button type="button" variant="ghost" size="sm"
            onClick={() => setState((current) => ({
              ...current, [clearField]: !current[clearField],
              [field]: !current[clearField] ? '' : current[field]
            }))}
            className={`shrink-0 ${cleared ? 'text-warn' : 'text-danger'}`}>
            {cleared ? '撤销移除' : `移除 ${label}`}
          </Button>
        )}
      </div>
    </FormFieldRow>
  );
};

const EndpointForm = ({ endpoint = null, onSave, onCancel, loading = false }) => {
  const isEditMode = Boolean(endpoint);
  const [state, setState] = useState(() => createEndpointFormState(endpoint));
  const [errors, setErrors] = useState({});
  const [showAdvanced, setShowAdvanced] = useState(false);
  const nameInputRef = useRef(null);
  const urlInputRef = useRef(null);

  const requestClose = () => {
    if (!loading) onCancel?.();
  };

  const change = (event) => {
    const { name, value, type, checked } = event.target;
    setState((current) => ({ ...current, [name]: type === 'checkbox' ? checked : value }));
    if (errors[name]) setErrors((current) => ({ ...current, [name]: undefined }));
  };

  const updateRule = (index, key, value) => {
    setState((current) => ({
      ...current,
      modelRewriteRules: current.modelRewriteRules.map((rule, ruleIndex) => (
        ruleIndex === index ? { ...rule, [key]: value } : rule
      ))
    }));
  };

  const clearRuleError = () => {
    if (errors.modelRewriteRules) setErrors((current) => ({ ...current, modelRewriteRules: undefined }));
  };

  // 删空最后一条规则等价于停用模型改写，保存时写入空规则
  const removeRule = (index) => {
    setState((current) => {
      const rules = current.modelRewriteRules.filter((_, ruleIndex) => ruleIndex !== index);
      return { ...current, modelRewriteEnabled: rules.length > 0, modelRewriteRules: rules };
    });
    clearRuleError();
  };

  const toggleModelRewrite = (enabled) => {
    setState((current) => ({
      ...current,
      modelRewriteEnabled: enabled,
      modelRewriteRules: enabled && current.modelRewriteRules.length === 0
        ? [createEmptyEndpointModelRewriteRule()]
        : current.modelRewriteRules
    }));
    clearRuleError();
  };

  const submit = async (event) => {
    event.preventDefault();
    const nextErrors = validateEndpointFormState(state);
    const rules = Array.isArray(state.modelRewriteRules) ? state.modelRewriteRules : [];
    if (state.modelRewriteEnabled && (
      rules.length === 0
      || rules.some((rule) => !String(rule.source || '').trim() || !String(rule.target || '').trim())
    )) {
      nextErrors.modelRewriteRules = '请完整填写至少一条模型改写规则';
    }
    setErrors(nextErrors);
    if (Object.keys(nextErrors).length > 0) return;
    try {
      await onSave?.(buildEndpointFormPayload(state));
    } catch (error) {
      setErrors({ submit: error?.message || '保存失败' });
    }
  };

  return (
    <Sheet title={isEditMode ? '编辑 Claude 端点' : '新建 Claude 端点'}
      label={isEditMode ? '编辑 Claude 端点' : '新建 Claude 端点'} width={600}
      subtitle="一个端点对应一组固定认证信息；凭据始终以掩码展示。"
      onClose={requestClose} closeDisabled={loading} onSubmit={submit}
      initialFocusRef={isEditMode ? urlInputRef : nameInputRef}
      footerStart={errors.submit && <span role="alert" className="flex items-start gap-2 text-danger"><AlertCircle size={14} className="shrink-0" />{errors.submit}</span>}
      footer={<>
        <Button type="button" variant="ghost" onClick={requestClose} disabled={loading}>取消</Button>
        <Button type="submit" icon={Save} loading={loading}>{isEditMode ? '保存修改' : '创建端点'}</Button>
      </>}>
      <FormSection title="连接">
        <FormInput label="名称" name="name" inputRef={nameInputRef} value={state.name} onChange={change} disabled={isEditMode} placeholder="claude-primary" help={isEditMode ? '稳定唯一标识，编辑时不可修改' : '保存后作为请求上游名称'} error={errors.name} />
        <FormInput label="URL" name="url" inputRef={urlInputRef} value={state.url} onChange={change} placeholder="https://api.example.com" help="仅接受不含账号密码的 HTTP(S) URL" error={errors.url} />
      </FormSection>
      <FormSection title="认证">
        <SecretField label="Token" field="token" clearField="clearToken" maskedValue={endpoint?.tokenMasked} state={state} setState={setState} />
        <SecretField label="API Key" field="apiKey" clearField="clearApiKey" maskedValue={endpoint?.apiKeyMasked} state={state} setState={setState} />
        <FormBlock>
          <div className="mb-2 flex items-center justify-between gap-2">
            <div><div className="text-[13px] text-fg-body">自定义 Header</div><div className="text-xs text-fg-muted">用于端点要求的附加请求头。</div></div>
            <Button type="button" variant="ghost" size="sm" icon={Plus} onClick={() => setState((current) => ({ ...current, headerRows: [...current.headerRows, { name: '', value: '' }] }))}>添加</Button>
          </div>
          {state.headerRows.length === 0 ? <p className="text-xs text-fg-muted">未配置自定义 Header</p> : (
            <div className="space-y-2">
              {state.headerRows.map((row, index) => (
                <div key={index} className="grid grid-cols-[1fr_1fr_auto] gap-2">
                  <input aria-label={`Header 名称 ${index + 1}`} value={row.name} aria-invalid={Boolean(errors.headers)} onChange={(event) => setState((current) => ({ ...current, headerRows: current.headerRows.map((item, itemIndex) => itemIndex === index ? { ...item, name: event.target.value } : item) }))} placeholder="X-Tenant" className="desk-field min-w-0 w-full" />
                  <input aria-label={`Header 值 ${index + 1}`} value={row.value} aria-invalid={Boolean(errors.headers)} onChange={(event) => setState((current) => ({ ...current, headerRows: current.headerRows.map((item, itemIndex) => itemIndex === index ? { ...item, value: event.target.value } : item) }))} placeholder="value" className="desk-field min-w-0 w-full" />
                  <button type="button" aria-label={`删除 Header ${index + 1}`} onClick={() => setState((current) => ({ ...current, headerRows: current.headerRows.filter((_, itemIndex) => itemIndex !== index) }))} className="inline-flex h-6 w-6 items-center justify-center rounded text-fg-muted hover:bg-surface-mut hover:text-danger"><Trash2 size={14} /></button>
                </div>
              ))}
            </div>
          )}
          {errors.headers && <p className="mt-1 text-[11.5px] text-danger">{errors.headers}</p>}
        </FormBlock>
      </FormSection>
      <FormSection title="调度">
        <FormInput label="优先级" name="priority" value={state.priority} onChange={change} type="number" numberWidth="w-32" min="0" help="允许重复，数字越小越优先" />
        <FormInput label="请求超时（秒）" name="timeoutSeconds" value={state.timeoutSeconds} onChange={change} type="number" numberWidth="w-32" min="1" />
        <FormInput label="冷却时间（秒）" name="cooldownSeconds" value={state.cooldownSeconds} onChange={change} type="number" numberWidth="w-32" min="0" placeholder="使用全局配置" />
        <Checkbox label="硬启用" help="关闭后任何路由模式都不会使用" checked={state.availabilityEnabled} onChange={(availabilityEnabled) => setState((current) => ({ ...current, availabilityEnabled }))} />
        <Checkbox label="参与自动调度" help="进入 Auto 与 fallback 候选" checked={state.failoverEnabled} onChange={(failoverEnabled) => setState((current) => ({ ...current, failoverEnabled }))} />
        <Checkbox label="支持 count_tokens" help="允许转发 Token 计数请求" checked={state.supportsCountTokens} onChange={(supportsCountTokens) => setState((current) => ({ ...current, supportsCountTokens }))} />
      </FormSection>
      <SoftFailurePolicyFields value={state} disabled={loading} error={errors.softFailurePolicy}
        onChange={(patch) => {
          setState((current) => ({ ...current, ...patch }));
          setErrors((current) => ({ ...current, softFailurePolicy: undefined }));
        }} />
      <FormSection title="模型改写" description="仅作用于 Claude Messages 与 count_tokens，按精确匹配执行。"
        actions={<Switch checked={state.modelRewriteEnabled} label="启用模型改写" onChange={toggleModelRewrite} />}>
        {state.modelRewriteEnabled && (
          <FormBlock>
            <div className="space-y-2">
              {state.modelRewriteRules.map((rule, index) => (
                <div key={index} className="grid grid-cols-[1fr_1fr_auto] gap-2">
                  <input aria-label={`来源模型 ${index + 1}`} value={rule.source || ''} aria-invalid={Boolean(errors.modelRewriteRules)} onChange={(event) => updateRule(index, 'source', event.target.value)} placeholder="claude-sonnet-4-5" className="desk-field min-w-0 w-full" />
                  <input aria-label={`目标模型 ${index + 1}`} value={rule.target || ''} aria-invalid={Boolean(errors.modelRewriteRules)} onChange={(event) => updateRule(index, 'target', event.target.value)} placeholder="provider-sonnet" className="desk-field min-w-0 w-full" />
                  <button type="button" onClick={() => removeRule(index)} className="inline-flex h-6 w-6 items-center justify-center rounded text-fg-muted hover:bg-surface-mut hover:text-danger" aria-label={`删除模型规则 ${index + 1}`}><Trash2 size={14} /></button>
                </div>
              ))}
              <Button type="button" variant="ghost" size="sm" icon={Plus} onClick={() => setState((current) => ({ ...current, modelRewriteRules: [...current.modelRewriteRules, createEmptyEndpointModelRewriteRule()] }))}>添加规则</Button>
              {errors.modelRewriteRules && <p className="text-[11.5px] text-danger">{errors.modelRewriteRules}</p>}
            </div>
          </FormBlock>
        )}
      </FormSection>
      <FormSection title="高级" actions={<Button type="button" variant="ghost" size="sm" icon={showAdvanced ? ChevronUp : ChevronDown} onClick={() => setShowAdvanced((current) => !current)}>成本倍率</Button>}>
        {showAdvanced && [
          ['costMultiplier', '总成本倍率'], ['inputCostMultiplier', '输入倍率'], ['outputCostMultiplier', '输出倍率'],
          ['cacheCreationCostMultiplier', '5 分钟缓存创建'], ['cacheCreationCostMultiplier1h', '1 小时缓存创建'], ['cacheReadCostMultiplier', '缓存读取倍率']
        ].map(([name, label]) => <FormInput key={name} label={label} name={name} value={state[name]} onChange={change} type="number" min="0" step="0.1" />)}
      </FormSection>
    </Sheet>
  );
};

export default EndpointForm;
