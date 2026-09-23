import { useEffect, useMemo, useState } from 'react';
import { ArrowLeft, Check, Code2 } from 'lucide-react';
import { CustomSelect } from '@components/ui';
import { resolveErrorMessage } from '@utils/errors.js';
import {
  DEFAULT_INPUT_MODALITIES,
  parseCapabilities,
  REASONING_LEVELS,
  toggleReasoningLevel,
  tryParseCapabilities,
  validateCapabilities,
  validateModelDraft
} from '../utils/codexModelCatalog.js';

const inputClass = 'w-full rounded-lg border border-line bg-surface px-3 py-2 text-sm text-fg focus:border-inverted focus:outline-none focus:ring-1 focus:ring-inverted disabled:bg-surface-mut disabled:text-fg-subtle';

const VISIBILITY_OPTIONS = [
  { value: 'list', label: '显示在模型菜单' },
  { value: 'hide', label: '隐藏' },
  { value: 'none', label: '不列出（none）' }
];

const MODALITY_OPTIONS = [
  { value: 'text', label: '文本输入' },
  { value: 'image', label: '图片输入' }
];

const Field = ({ label, hint, children, as: Container = 'label' }) => (
  <Container className="block space-y-1.5">
    <span className="text-xs font-medium text-fg-muted">{label}</span>
    {children}
    {hint && <span className="block text-xs leading-5 text-fg-subtle">{hint}</span>}
  </Container>
);

const CheckboxLabel = ({ checked, onChange, className = '', children }) => (
  <label className={`inline-flex cursor-pointer items-center gap-2 ${className}`}>
    <input type="checkbox" className="app-checkbox" checked={checked} onChange={event => onChange(event.target.checked)} />
    {children}
  </label>
);

// 能力 JSON 已损坏时直接进入高级编辑并保留原文，让用户修复而不是让抽屉渲染失败。
const createInitialState = model => {
  const { metadata, error } = tryParseCapabilities(model);
  return {
    metadata,
    advanced: Boolean(error),
    advancedText: error ? model.capabilities_json : '',
    error: error || model.capabilities_error || ''
  };
};

// 用于判断是否有未应用的修改：高级模式下按解析后的内容比较，忽略纯格式差异。
const capabilityFingerprint = ({ advanced, metadata, advancedText }) => {
  if (!advanced) return JSON.stringify(metadata);
  if (!advancedText.trim()) return 'null';
  try {
    return JSON.stringify(JSON.parse(advancedText));
  } catch {
    return advancedText;
  }
};

const CodexModelEditor = ({ model, isNew, templates, onApply, onCancel, onDirtyChange }) => {
  const [initial] = useState(() => createInitialState(model));
  const [draft, setDraft] = useState(model);
  const [metadata, setMetadata] = useState(initial.metadata);
  const [advanced, setAdvanced] = useState(initial.advanced);
  const [advancedText, setAdvancedText] = useState(initial.advancedText);
  const [error, setError] = useState(initial.error);

  const changed = useMemo(() => (
    JSON.stringify(draft) !== JSON.stringify(model)
    || capabilityFingerprint({ advanced, metadata, advancedText }) !== capabilityFingerprint(initial)
  ), [advanced, advancedText, draft, initial, metadata, model]);

  useEffect(() => {
    onDirtyChange?.(changed);
  }, [changed, onDirtyChange]);

  const update = (key, value) => setDraft(previous => ({ ...previous, [key]: value }));
  const updateCapability = (key, value) => setMetadata(previous => ({ ...previous, [key]: value }));
  const numeric = (key, value) => updateCapability(key, value === '' ? null : Number(value));

  const levels = metadata?.supported_reasoning_levels || [];
  const effortOptions = [...REASONING_LEVELS, ...levels.map(level => level.effort).filter(effort => !REASONING_LEVELS.includes(effort))];
  const defaultLevelOptions = [
    { value: '', label: '由客户端选择' },
    ...levels.map(level => ({ value: level.effort, label: level.effort }))
  ];
  const modalities = metadata?.input_modalities ?? DEFAULT_INPUT_MODALITIES;

  const selectTemplate = id => {
    const template = templates.find(entry => entry.id === id);
    const parsed = template && tryParseCapabilities(template);
    if (!parsed?.metadata) return;
    setMetadata(parsed.metadata);
    setAdvanced(false);
    setError('');
  };

  // 取消勾选当前默认等级时一并清空默认值，避免留下不在支持集合里的默认等级。
  const handleToggleLevel = effort => setMetadata(previous => {
    const nextLevels = toggleReasoningLevel(previous.supported_reasoning_levels || [], effort, templates);
    const current = previous.default_reasoning_level;
    const keepsDefault = current == null || nextLevels.some(level => level.effort === current);
    return { ...previous, supported_reasoning_levels: nextLevels, ...(keepsDefault ? {} : { default_reasoning_level: null }) };
  });

  const handleToggleModality = (modality, checked) => {
    const next = checked ? [...new Set([...modalities, modality])] : modalities.filter(value => value !== modality);
    updateCapability('input_modalities', next);
  };

  const enterAdvanced = () => {
    setAdvancedText(metadata ? JSON.stringify(metadata, null, 2) : '');
    setAdvanced(true);
  };

  const applyAdvanced = () => {
    try {
      const next = parseCapabilities({ capabilities_json: advancedText });
      if (next) validateCapabilities(next);
      setMetadata(next);
      setAdvanced(false);
      setError('');
    } catch (err) {
      setError(resolveErrorMessage(err, '能力 JSON 校验失败'));
    }
  };

  const save = () => {
    try {
      const capabilities = advanced ? parseCapabilities({ capabilities_json: advancedText }) : metadata;
      onApply(validateModelDraft({ ...draft, capabilities_json: capabilities ? JSON.stringify(capabilities) : '' }));
    } catch (err) {
      setError(resolveErrorMessage(err, '模型校验失败'));
    }
  };

  return (
    <section className="space-y-5" aria-label="编辑模型能力">
      <div className="flex items-center justify-between gap-3 border-b border-line-soft pb-4">
        <button type="button" onClick={() => onCancel(changed)} className="inline-flex items-center gap-1.5 text-sm text-fg-muted transition-colors hover:text-fg">
          <ArrowLeft size={16} />
          返回目录
        </button>
        <span className="text-sm font-semibold text-fg">{isNew ? '新建模型' : model.id}</span>
      </div>

      {error && <p role="alert" className="rounded-lg bg-danger-soft px-3 py-2 text-sm text-danger">{error}</p>}

      <div className="grid gap-4 sm:grid-cols-2">
        <Field label="模型 ID" hint={isNew ? '与上游模型或已配置的模型改写规则对应。' : '已有模型 ID 保持稳定；需要新 ID 时请复制模型。'}>
          <input aria-label="模型 ID" autoFocus={isNew} className={inputClass} value={draft.id} disabled={!isNew} onChange={event => update('id', event.target.value)} />
        </Field>
        <Field label="显示名称">
          <input className={inputClass} value={draft.display_name} onChange={event => update('display_name', event.target.value)} />
        </Field>
      </div>
      <Field label="描述">
        <textarea className={inputClass} rows={2} value={draft.description} onChange={event => update('description', event.target.value)} />
      </Field>

      <Field as="div" label="从已有模型复制完整能力" hint="会替换当前编辑草稿的能力设置，保留 ID、名称和描述。复制后请按上游实际能力调整。">
        <CustomSelect
          options={templates.map(template => ({ value: template.id, label: `${template.display_name || template.id} · ${template.id}` }))}
          value=""
          placeholder={templates.length ? '选择能力模板…' : '暂无可用能力模板'}
          disabled={!templates.length}
          size="md"
          className="w-full"
          onChange={selectTemplate}
        />
      </Field>

      {!metadata && !advanced && (
        <div className="rounded-lg border border-warn-line bg-warn-soft p-4 text-sm leading-6 text-warn">
          能力待补全。请先导入完整 Codex 目录，或选择一个已有能力模板。仅有 ID 的模型不会进入 Codex 能力目录。
        </div>
      )}

      {metadata && !advanced && (
        <>
          <div className="grid gap-4 sm:grid-cols-3">
            <Field label="上下文窗口（tokens）">
              <input type="number" min="1" step="1" className={inputClass} value={metadata.context_window ?? ''} onChange={event => numeric('context_window', event.target.value)} />
            </Field>
            <Field label="最大上下文（tokens）">
              <input type="number" min="1" step="1" className={inputClass} value={metadata.max_context_window ?? ''} onChange={event => numeric('max_context_window', event.target.value)} />
            </Field>
            <Field label="有效上下文比例（%）">
              <input type="number" min="1" max="100" step="1" className={inputClass} value={metadata.effective_context_window_percent ?? ''} onChange={event => numeric('effective_context_window_percent', event.target.value)} />
            </Field>
          </div>

          <div className="space-y-2">
            <span className="text-xs font-medium text-fg-muted">支持的思考等级</span>
            <div className="flex flex-wrap gap-2">
              {effortOptions.map(effort => (
                <CheckboxLabel
                  key={effort}
                  checked={levels.some(level => level.effort === effort)}
                  onChange={() => handleToggleLevel(effort)}
                  className="rounded-lg border border-line px-3 py-2 text-xs text-fg-body"
                >
                  {effort}
                </CheckboxLabel>
              ))}
            </div>
          </div>

          <div className="grid gap-4 sm:grid-cols-2">
            <Field as="div" label="默认思考等级">
              <CustomSelect
                options={defaultLevelOptions}
                value={metadata.default_reasoning_level ?? ''}
                placeholder="由客户端选择"
                size="md"
                className="w-full"
                onChange={value => updateCapability('default_reasoning_level', value || null)}
              />
            </Field>
            <Field as="div" label="模型菜单可见性">
              <CustomSelect
                options={VISIBILITY_OPTIONS}
                value={metadata.visibility}
                size="md"
                className="w-full"
                onChange={value => updateCapability('visibility', value)}
              />
            </Field>
          </div>

          <div className="flex flex-wrap gap-5 rounded-lg border border-line bg-surface-sub p-3 text-sm text-fg-body">
            {MODALITY_OPTIONS.map(option => (
              <CheckboxLabel key={option.value} checked={modalities.includes(option.value)} onChange={checked => handleToggleModality(option.value, checked)}>
                {option.label}
              </CheckboxLabel>
            ))}
            <CheckboxLabel checked={metadata.supported_in_api === true} onChange={checked => updateCapability('supported_in_api', checked)}>
              API Key 可用
            </CheckboxLabel>
          </div>
        </>
      )}

      <div className="border-t border-line-soft pt-4">
        <button
          type="button"
          className="inline-flex items-center gap-2 text-xs font-medium text-fg-muted transition-colors hover:text-fg"
          onClick={advanced ? applyAdvanced : enterAdvanced}
        >
          <Code2 size={15} />
          {advanced ? '应用高级编辑并返回表单' : '高级能力字段'}
        </button>
        {advanced && (
          <div className="mt-3 space-y-2">
            <p className="text-xs leading-5 text-fg-subtle">完整保留工具协议、指令和扩展字段。ID、名称、描述使用上方基本信息；清空内容表示不提供能力。保存时会校验能力内容。</p>
            <textarea
              aria-label="高级能力 JSON"
              spellCheck={false}
              placeholder="{ }"
              className={`${inputClass} min-h-80 font-mono text-xs`}
              value={advancedText}
              onChange={event => setAdvancedText(event.target.value)}
            />
          </div>
        )}
      </div>

      <div className="flex items-center justify-between border-t border-line-soft pt-4">
        <CheckboxLabel checked={draft.enabled} onChange={checked => update('enabled', checked)} className="text-sm text-fg-body">
          启用模型
        </CheckboxLabel>
        <button type="button" onClick={save} className="inline-flex items-center gap-2 rounded-lg bg-inverted px-4 py-2 text-sm text-fg-inverted transition-colors hover:bg-inverted/85">
          <Check size={16} />
          应用到目录
        </button>
      </div>
      <p className="text-xs text-fg-subtle">应用后仍需保存目录，客户端才能获取修改。</p>
    </section>
  );
};

export default CodexModelEditor;
