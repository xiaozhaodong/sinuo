import { useEffect, useMemo, useState } from 'react';
import { AlertCircle, Code2, Trash2 } from 'lucide-react';
import { Button } from '@components/ui';
import { ToolbarButton } from '@components/layout/ToolbarButton.jsx';
import { Inspector, InspectorBody, InspectorFooter, InspectorHeader, InspectorSection, Property, PropertyList } from '@components/desktop/Inspector.jsx';
import { resolveErrorMessage } from '@utils/errors.js';
import {
  DEFAULT_INPUT_MODALITIES, hasValidCapabilities, parseCapabilities,
  toggleReasoningLevel, tryParseCapabilities, validateModelDraft
} from '../../account-pool/utils/codexModelCatalog.js';
import CodexCapabilityFields from './CodexCapabilityFields.jsx';
import CodexCapabilitiesSheet from './CodexCapabilitiesSheet.jsx';
import { codexModelStatusTags, STATUS_TAG_CLASSES } from './codexCatalogView.js';

// 保留损坏的原文，交给能力 JSON 弹窗修复。
const createInitialState = (model) => {
  const { metadata, error } = tryParseCapabilities(model);
  return { metadata, rawCapabilities: error ? model.capabilities_json : null, error: error || model.capabilities_error || '' };
};

// 按解析后的内容比较，忽略 JSON 的纯格式差异。
const capabilityFingerprint = ({ metadata, rawCapabilities }) => {
  if (rawCapabilities === null) return JSON.stringify(metadata);
  if (!rawCapabilities.trim()) return 'null';
  try {
    return JSON.stringify(JSON.parse(rawCapabilities));
  } catch {
    return rawCapabilities;
  }
};

const CodexModelInspector = ({ model, models, isNew, saving, onSave, onCancelNew, onDelete, onDirtyChange }) => {
  const [base] = useState(model);
  const [initial] = useState(() => createInitialState(model));
  const [draft, setDraft] = useState(base);
  const [metadata, setMetadata] = useState(initial.metadata);
  const [rawCapabilities, setRawCapabilities] = useState(initial.rawCapabilities);
  const [error, setError] = useState(initial.error);
  const [jsonOpen, setJsonOpen] = useState(false);
  const templates = useMemo(() => models.filter((entry) => hasValidCapabilities(entry) && (isNew || entry.id !== model.id)), [isNew, model.id, models]);
  const tags = useMemo(() => codexModelStatusTags(model), [model]);
  const dirty = isNew || JSON.stringify(draft) !== JSON.stringify(base)
    || capabilityFingerprint({ metadata, rawCapabilities }) !== capabilityFingerprint(initial);

  useEffect(() => {
    onDirtyChange?.(dirty);
  }, [dirty, onDirtyChange]);

  const update = (key, value) => setDraft((previous) => ({ ...previous, [key]: value }));
  const updateCapability = (key, value) => setMetadata((previous) => ({ ...previous, [key]: value }));
  const selectTemplate = (id) => {
    const template = templates.find((entry) => entry.id === id);
    const parsed = template && tryParseCapabilities(template);
    if (!parsed?.metadata) return;
    setMetadata(parsed.metadata);
    setRawCapabilities(null);
    setError('');
  };

  // 删除当前默认等级时清空默认值，避免留下不在支持集合中的值。
  const handleToggleLevel = (effort) => setMetadata((previous) => {
    const nextLevels = toggleReasoningLevel(previous.supported_reasoning_levels || [], effort, templates);
    const current = previous.default_reasoning_level;
    const keepsDefault = current == null || nextLevels.some((level) => level.effort === current);
    return { ...previous, supported_reasoning_levels: nextLevels, ...(keepsDefault ? {} : { default_reasoning_level: null }) };
  });

  const handleToggleModality = (modality, checked) => {
    const modalities = metadata?.input_modalities ?? DEFAULT_INPUT_MODALITIES;
    const next = checked ? [...new Set([...modalities, modality])] : modalities.filter((value) => value !== modality);
    updateCapability('input_modalities', next);
  };

  const submit = async (event) => {
    event.preventDefault();
    setError('');
    try {
      const capabilities = rawCapabilities !== null ? parseCapabilities({ capabilities_json: rawCapabilities }) : metadata;
      const validated = validateModelDraft({ ...draft, capabilities_json: capabilities ? JSON.stringify(capabilities) : '' });
      await onSave(validated, isNew ? '' : base.id);
    } catch (failure) {
      setError(resolveErrorMessage(failure, '保存模型失败'));
    }
  };

  const restore = () => {
    setDraft(base);
    setMetadata(initial.metadata);
    setRawCapabilities(initial.rawCapabilities);
    setError(initial.error);
  };

  return (
    <Inspector label="Codex 模型详情" width={360}>
      <form onSubmit={submit} className="flex min-h-0 flex-1 flex-col">
        <InspectorHeader title={isNew ? '新建模型' : model.display_name || model.id} subtitle={isNew ? '保存后出现在 Codex 模型目录' : model.id}
          badges={<div className="flex shrink-0 flex-wrap gap-1">
            <span className={`rounded border px-1.5 text-[11px] leading-[18px] ${STATUS_TAG_CLASSES.muted}`}>{model.source === 'official' ? '官方' : '自定义'}</span>
            {tags.map((tag) => <span key={tag.key} title={tag.title} className={`rounded border px-1.5 text-[11px] leading-[18px] ${STATUS_TAG_CLASSES[tag.tone]}`}>{tag.label}</span>)}
          </div>} />
        <InspectorBody>
          {(error || rawCapabilities !== null) && (
            <div role="alert" className="selectable mx-4 mt-3 rounded-md border border-danger-line bg-danger-soft px-2.5 py-2 text-xs text-danger">
              <div className="flex items-start gap-2"><AlertCircle size={14} className="mt-px shrink-0" /><span>{rawCapabilities !== null ? `能力 JSON 无法解析：${error || initial.error}` : error}</span></div>
              {rawCapabilities !== null && model.capabilities_error && <p className="mt-1">{model.capabilities_error}</p>}
              {rawCapabilities !== null && <Button type="button" variant="secondary" size="sm" className="mt-2" disabled={saving} onClick={() => setJsonOpen(true)}>修复能力 JSON…</Button>}
            </div>
          )}
          {!metadata && rawCapabilities === null && (
            <p className="mx-4 mt-3 rounded-md border border-warn-line bg-warn-soft px-2.5 py-2 text-xs leading-relaxed text-warn">能力待补全，不会出现在 Codex 模型菜单。请从「能力模板」选一个已有模型，或导入完整目录。</p>
          )}
          <InspectorSection title="基本信息">
            <PropertyList labelWidth={76}>
              <Property label="模型 ID" truncate={false}>
                {isNew ? <input aria-label="模型 ID" className="desk-field w-full font-mono" autoFocus value={draft.id} disabled={saving} onChange={(event) => update('id', event.target.value)} />
                  : <span className="font-mono text-[12px]" title="已有模型 ID 保持稳定；需要新 ID 时请右键「复制为新模型」">{draft.id}</span>}
              </Property>
              <Property label="显示名称" truncate={false}>
                <input aria-label="显示名称" className="desk-field w-full" value={draft.display_name} disabled={saving} onChange={(event) => update('display_name', event.target.value)} />
              </Property>
              <Property label="描述" truncate={false}>
                <textarea aria-label="描述" className="desk-field h-auto w-full py-1.5 leading-relaxed" rows={2} value={draft.description} disabled={saving} onChange={(event) => update('description', event.target.value)} />
              </Property>
            </PropertyList>
          </InspectorSection>
          {rawCapabilities === null && <InspectorSection title="能力">
            <CodexCapabilityFields metadata={metadata} templates={templates} disabled={saving} onSelectTemplate={selectTemplate}
              onUpdate={updateCapability} onToggleLevel={handleToggleLevel} onToggleModality={handleToggleModality} />
          </InspectorSection>}
          <InspectorSection title="高级">
            <Button type="button" variant="secondary" size="sm" icon={Code2} disabled={saving} onClick={() => setJsonOpen(true)}>编辑能力 JSON…</Button>
            <p className="mt-2 text-[11.5px] text-fg-subtle">完整保留工具协议、指令和扩展字段</p>
          </InspectorSection>
        </InspectorBody>
        <InspectorFooter>
          {!isNew && <ToolbarButton icon={Trash2} title={model.source === 'custom' ? '删除模型' : '官方模型不能删除'} disabled={saving || model.source !== 'custom'} onClick={() => onDelete(model)} />}
          <span className="flex-1" />
          <Button type="button" variant="secondary" onClick={isNew ? onCancelNew : restore} disabled={saving || (!isNew && !dirty)}>{isNew ? '取消' : '还原'}</Button>
          <Button type="submit" loading={saving} disabled={!dirty}>{isNew ? '创建' : '保存'}</Button>
        </InspectorFooter>
      </form>
      {jsonOpen && <CodexCapabilitiesSheet modelId={draft.id} metadata={metadata} rawCapabilities={rawCapabilities}
        onApply={(next) => { setMetadata(next); setRawCapabilities(null); setError(''); }} onClose={() => setJsonOpen(false)} />}
    </Inspector>
  );
};

export default CodexModelInspector;
