import SoftFailurePolicyFields from '@components/ui/SoftFailurePolicyFields.jsx';

import { useRef, useState } from 'react';
import { createPortal } from 'react-dom';
import { AlertCircle, ChevronDown, ChevronUp, Eye, EyeOff, Plus, Save, Trash2 } from 'lucide-react';
import { Button } from '@components/ui';
import useModalLifecycle from '@hooks/useModalLifecycle.js';
import { createEmptyEndpointModelRewriteRule } from '../utils/modelRewrite.js';
import {
  buildEndpointFormPayload,
  createEndpointFormState,
  validateEndpointFormState
} from '../utils/endpointFormState.js';

const FormInput = ({ label, name, value, onChange, type = 'text', placeholder, disabled, help, min, step }) => (
  <label className="block space-y-1">
    <span className="block text-sm font-medium text-fg-body">{label}</span>
    <input
      type={type}
      name={name}
      value={value ?? ''}
      onChange={onChange}
      placeholder={placeholder}
      disabled={disabled}
      min={min}
      step={step}
      className="w-full rounded-lg border border-line bg-surface px-3 py-2 text-sm text-fg outline-none transition focus:border-accent-ring focus:ring-2 focus:ring-accent-ring/20 disabled:cursor-not-allowed disabled:bg-surface-mut disabled:text-fg-subtle"
    />
    {help && <span className="block text-xs leading-5 text-fg-subtle">{help}</span>}
  </label>
);

const PasswordInput = ({ label, name, value, onChange, placeholder, help, disabled }) => {
  const [visible, setVisible] = useState(false);
  return (
    <label className="block space-y-1">
      <span className="block text-sm font-medium text-fg-body">{label}</span>
      <span className="relative block">
        <input
          type={visible ? 'text' : 'password'}
          name={name}
          value={value || ''}
          onChange={onChange}
          placeholder={placeholder}
          disabled={disabled}
          className="w-full rounded-lg border border-line bg-surface px-3 py-2 pr-10 text-sm text-fg outline-none transition focus:border-accent-ring focus:ring-2 focus:ring-accent-ring/20 disabled:bg-surface-mut"
        />
        <button
          type="button"
          onClick={() => setVisible((current) => !current)}
          className="absolute right-2 top-1/2 -translate-y-1/2 rounded p-1 text-fg-subtle hover:bg-surface-mut hover:text-fg-body"
          aria-label={visible ? `隐藏${label}` : `显示${label}`}
        >
          {visible ? <EyeOff size={17} /> : <Eye size={17} />}
        </button>
      </span>
      {help && <span className="block text-xs leading-5 text-fg-subtle">{help}</span>}
    </label>
  );
};

const Checkbox = ({ label, help, checked, onChange }) => (
  <label className="flex items-start gap-3 rounded-xl border border-line bg-surface-sub px-3 py-2.5">
    <input type="checkbox" checked={checked} onChange={onChange} className="mt-1 app-checkbox" />
    <span>
      <span className="block text-sm font-medium text-fg-body">{label}</span>
      <span className="block text-xs leading-5 text-fg-subtle">{help}</span>
    </span>
  </label>
);

const SecretField = ({ label, field, clearField, maskedValue, state, setState }) => {
  const hasStoredValue = Boolean(maskedValue);
  const cleared = state[clearField] === true;
  return (
    <div className="rounded-xl border border-line p-3">
      <div className="mb-2 flex items-center justify-between gap-3">
        <div>
          <div className="text-sm font-medium text-fg-body">{label}</div>
          <div className="text-xs text-fg-subtle">
            {hasStoredValue ? `当前已保存：${maskedValue}` : '当前未配置'}
          </div>
        </div>
        {hasStoredValue && (
          <button
            type="button"
            onClick={() => setState((current) => ({
              ...current,
              [clearField]: !current[clearField],
              [field]: !current[clearField] ? '' : current[field]
            }))}
            className={`rounded-lg border px-2.5 py-1.5 text-xs font-medium transition ${cleared ? 'tone-amber' : 'border-danger-line text-danger hover:bg-danger-soft'}`}
          >
            {cleared ? '撤销移除' : `移除 ${label}`}
          </button>
        )}
      </div>
      <PasswordInput
        label={hasStoredValue ? `替换 ${label}` : label}
        name={field}
        value={state[field]}
        onChange={(event) => setState((current) => ({ ...current, [field]: event.target.value, [clearField]: false }))}
        disabled={cleared}
        placeholder={hasStoredValue ? '留空表示保留当前值' : '可选'}
        help={cleared ? `保存后会明确删除已存 ${label}` : '前端不会读取或回填已保存的明文凭据'}
      />
    </div>
  );
};

const EndpointForm = ({ endpoint = null, onSave, onCancel, loading = false }) => {
  const isEditMode = Boolean(endpoint);
  const [state, setState] = useState(() => createEndpointFormState(endpoint));
  const [errors, setErrors] = useState({});
  const [showAdvanced, setShowAdvanced] = useState(false);
  const closeButtonRef = useRef(null);

  const requestClose = () => {
    if (!loading) onCancel?.();
  };
  useModalLifecycle({ open: true, onClose: requestClose, initialFocusRef: closeButtonRef });

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

  return createPortal(
    <div className="fixed inset-0 z-[60] flex items-start justify-center px-4 pt-[15vh]">
      <div className="absolute inset-0 bg-overlay" />
      <div role="dialog" aria-modal="true" aria-label={isEditMode ? '编辑 Claude 端点' : '新建 Claude 端点'} className="relative flex max-h-[75vh] w-full max-w-2xl flex-col overflow-hidden rounded-2xl border border-line bg-surface shadow-2xl">
        <div className="flex items-center justify-between border-b border-line-soft px-6 py-4">
          <div>
            <h2 className="text-lg font-semibold text-fg">{isEditMode ? '编辑 Claude 端点' : '新建 Claude 端点'}</h2>
            <p className="mt-0.5 text-xs text-fg-subtle">一个端点对应一组固定认证信息；凭据始终以掩码展示。</p>
          </div>
          <button ref={closeButtonRef} type="button" onClick={requestClose} disabled={loading} className="text-sm text-fg-subtle hover:text-fg-muted">关闭</button>
        </div>

        <form onSubmit={submit} className="flex min-h-0 flex-1 flex-col">
          <div className="flex-1 space-y-7 overflow-y-auto px-6 py-5">
            {errors.submit && <div className="tone-rose flex items-center gap-2 rounded-xl border px-3 py-2 text-sm"><AlertCircle size={16} />{errors.submit}</div>}

            <section>
              <h3 className="mb-3 text-xs font-semibold uppercase tracking-[0.14em] text-fg-subtle">连接</h3>
              <div className="grid gap-4 sm:grid-cols-2">
                <div>
                  <FormInput label="名称" name="name" value={state.name} onChange={change} disabled={isEditMode} placeholder="claude-primary" help={isEditMode ? '稳定唯一标识，编辑时不可修改' : '保存后作为请求上游名称'} />
                  {errors.name && <p className="mt-1 text-xs text-danger">{errors.name}</p>}
                </div>
                <div>
                  <FormInput label="URL" name="url" value={state.url} onChange={change} placeholder="https://api.example.com" help="仅接受不含账号密码的 HTTP(S) URL" />
                  {errors.url && <p className="mt-1 text-xs text-danger">{errors.url}</p>}
                </div>
              </div>
            </section>

            <section>
              <h3 className="mb-3 text-xs font-semibold uppercase tracking-[0.14em] text-fg-subtle">认证</h3>
              <div className="grid gap-3 sm:grid-cols-2">
                <SecretField label="Token" field="token" clearField="clearToken" maskedValue={endpoint?.tokenMasked} state={state} setState={setState} />
                <SecretField label="API Key" field="apiKey" clearField="clearApiKey" maskedValue={endpoint?.apiKeyMasked} state={state} setState={setState} />
              </div>

              <div className="mt-3 rounded-xl border border-line p-3">
                <div className="mb-2 flex items-center justify-between">
                  <div>
                    <div className="text-sm font-medium text-fg-body">自定义 Header</div>
                    <div className="text-xs text-fg-subtle">用于端点要求的附加请求头。</div>
                  </div>
                  <button type="button" onClick={() => setState((current) => ({ ...current, headerRows: [...current.headerRows, { name: '', value: '' }] }))} className="inline-flex items-center gap-1 rounded-lg border border-line px-2.5 py-1.5 text-xs text-fg-body hover:border-accent-line hover:bg-accent-soft hover:text-accent-fg"><Plus size={13} />添加</button>
                </div>
                {state.headerRows.length === 0 ? (
                  <div className="rounded-lg bg-surface-sub px-3 py-2 text-xs text-fg-subtle">未配置自定义 Header</div>
                ) : (
                  <div className="space-y-2">
                    {state.headerRows.map((row, index) => (
                      <div key={index} className="grid grid-cols-[1fr_1fr_auto] gap-2">
                        <input aria-label={`Header 名称 ${index + 1}`} value={row.name} onChange={(event) => setState((current) => ({ ...current, headerRows: current.headerRows.map((item, itemIndex) => itemIndex === index ? { ...item, name: event.target.value } : item) }))} placeholder="X-Tenant" className="rounded-lg border border-line bg-surface px-3 py-2 text-sm text-fg outline-none focus:border-accent-ring" />
                        <input aria-label={`Header 值 ${index + 1}`} value={row.value} onChange={(event) => setState((current) => ({ ...current, headerRows: current.headerRows.map((item, itemIndex) => itemIndex === index ? { ...item, value: event.target.value } : item) }))} placeholder="value" className="rounded-lg border border-line bg-surface px-3 py-2 text-sm text-fg outline-none focus:border-accent-ring" />
                        <button type="button" aria-label={`删除 Header ${index + 1}`} onClick={() => setState((current) => ({ ...current, headerRows: current.headerRows.filter((_, itemIndex) => itemIndex !== index) }))} className="rounded-lg border border-line p-2 text-fg-subtle hover:border-danger-line hover:bg-danger-soft hover:text-danger"><Trash2 size={15} /></button>
                      </div>
                    ))}
                  </div>
                )}
                {errors.headers && <p className="mt-2 text-xs text-danger">{errors.headers}</p>}
              </div>
            </section>

            <section>
              <h3 className="mb-3 text-xs font-semibold uppercase tracking-[0.14em] text-fg-subtle">调度</h3>
              <div className="grid gap-3 sm:grid-cols-3">
                <FormInput label="优先级" name="priority" value={state.priority} onChange={change} type="number" min="0" help="允许重复，数字越小越优先" />
                <FormInput label="请求超时（秒）" name="timeoutSeconds" value={state.timeoutSeconds} onChange={change} type="number" min="1" />
                <FormInput label="冷却时间（秒）" name="cooldownSeconds" value={state.cooldownSeconds} onChange={change} type="number" min="0" placeholder="使用全局配置" />
              </div>
              <div className="mt-3 grid gap-3 lg:grid-cols-3">
                <Checkbox label="硬启用" help="关闭后任何路由模式都不会使用" checked={state.availabilityEnabled} onChange={(event) => setState((current) => ({ ...current, availabilityEnabled: event.target.checked }))} />
                <Checkbox label="参与自动调度" help="进入 Auto 与 fallback 候选" checked={state.failoverEnabled} onChange={(event) => setState((current) => ({ ...current, failoverEnabled: event.target.checked }))} />
                <Checkbox label="支持 count_tokens" help="允许转发 Token 计数请求" checked={state.supportsCountTokens} onChange={(event) => setState((current) => ({ ...current, supportsCountTokens: event.target.checked }))} />
              </div>
            </section>

            <SoftFailurePolicyFields
              value={state}
              disabled={loading}
              error={errors.softFailurePolicy}
              onChange={(patch) => {
                setState((current) => ({ ...current, ...patch }));
                setErrors((current) => ({ ...current, softFailurePolicy: undefined }));
              }}
            />

            <section>
              <div className="mb-3 flex items-center justify-between">
                <div>
                  <h3 className="text-xs font-semibold uppercase tracking-[0.14em] text-fg-subtle">模型改写</h3>
                  <p className="mt-1 text-xs text-fg-subtle">仅作用于 Claude Messages 与 count_tokens，按精确匹配执行。</p>
                </div>
                <input type="checkbox" checked={state.modelRewriteEnabled} onChange={(event) => toggleModelRewrite(event.target.checked)} className="app-checkbox" aria-label="启用模型改写" />
              </div>
              {state.modelRewriteEnabled && (
                <div className="space-y-2 rounded-xl border border-line bg-surface-sub p-3">
                  {state.modelRewriteRules.map((rule, index) => (
                    <div key={index} className="grid gap-2 sm:grid-cols-[1fr_1fr_auto]">
                      <input aria-label={`来源模型 ${index + 1}`} value={rule.source || ''} onChange={(event) => updateRule(index, 'source', event.target.value)} placeholder="claude-sonnet-4-5" className="rounded-lg border border-line bg-surface px-3 py-2 text-sm text-fg outline-none focus:border-accent-ring" />
                      <input aria-label={`目标模型 ${index + 1}`} value={rule.target || ''} onChange={(event) => updateRule(index, 'target', event.target.value)} placeholder="provider-sonnet" className="rounded-lg border border-line bg-surface px-3 py-2 text-sm text-fg outline-none focus:border-accent-ring" />
                      <button type="button" onClick={() => removeRule(index)} className="rounded-lg border border-line p-2 text-fg-subtle hover:border-danger-line hover:bg-danger-soft hover:text-danger" aria-label={`删除模型规则 ${index + 1}`}><Trash2 size={15} /></button>
                    </div>
                  ))}
                  <button type="button" onClick={() => setState((current) => ({ ...current, modelRewriteRules: [...current.modelRewriteRules, createEmptyEndpointModelRewriteRule()] }))} className="inline-flex items-center gap-1 rounded-lg border border-line bg-surface px-2.5 py-1.5 text-xs text-fg-body hover:border-accent-line hover:text-accent-fg"><Plus size={13} />添加规则</button>
                  {errors.modelRewriteRules && <p className="text-xs text-danger">{errors.modelRewriteRules}</p>}
                </div>
              )}
            </section>

            <section>
              <button type="button" onClick={() => setShowAdvanced((current) => !current)} className="flex items-center gap-2 text-xs font-semibold uppercase tracking-[0.14em] text-fg-subtle hover:text-fg-muted">成本倍率 {showAdvanced ? <ChevronUp size={15} /> : <ChevronDown size={15} />}</button>
              {showAdvanced && (
                <div className="mt-3 grid gap-3 rounded-xl border border-line bg-surface-sub p-3 sm:grid-cols-2 lg:grid-cols-3">
                  {[
                    ['costMultiplier', '总成本倍率'],
                    ['inputCostMultiplier', '输入倍率'],
                    ['outputCostMultiplier', '输出倍率'],
                    ['cacheCreationCostMultiplier', '5 分钟缓存创建'],
                    ['cacheCreationCostMultiplier1h', '1 小时缓存创建'],
                    ['cacheReadCostMultiplier', '缓存读取倍率']
                  ].map(([name, label]) => <FormInput key={name} label={label} name={name} value={state[name]} onChange={change} type="number" min="0" step="0.1" />)}
                </div>
              )}
            </section>
          </div>

          <div className="flex justify-end gap-3 border-t border-line-soft bg-surface px-6 py-4">
            <Button type="button" variant="ghost" onClick={requestClose} disabled={loading}>取消</Button>
            <Button type="submit" icon={Save} loading={loading}>{isEditMode ? '保存修改' : '创建端点'}</Button>
          </div>
        </form>
      </div>
    </div>,
    document.body
  );
};

export default EndpointForm;
