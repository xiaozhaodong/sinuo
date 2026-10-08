// 本地敏感值弹窗的展示层；草稿、校验与导入流程由原面板持有。
import { Button, CustomSelect } from '@components/ui';
import Sheet from '@components/desktop/Sheet.jsx';
import Switch from '@components/desktop/Switch.jsx';
import { FormBlock, FormFieldRow, FormRow, FormSection } from '@components/desktop/FormSection.jsx';
import { PRIVACY_EXACT_SECRET_CATEGORY_OPTIONS, exactSecretCategoryLabel } from '../utils/privacyRules.js';

export const ExactSecretSheet = ({ open, form, errors, submitError, saving, initialFocusRef, onClose, onSubmit, update, onCategoryChange }) => (
  <Sheet open={open} title={form.id > 0 ? '编辑本地敏感值' : '新增本地敏感值'}
    label={form.id > 0 ? '编辑本地敏感值' : '新增本地敏感值'} width={512}
    onClose={onClose} closeDisabled={saving} onSubmit={onSubmit} initialFocusRef={initialFocusRef}
    footerStart={submitError && <span role="alert" className="break-all text-danger">{submitError}</span>}
    footer={<><Button type="button" variant="ghost" onClick={onClose} disabled={saving}>取消</Button><Button type="submit" loading={saving}>保存并生效</Button></>}>
    <FormSection title="本地敏感值">
      <FormFieldRow label="名称 *" htmlFor="exact-secret-name" error={errors.name}>
        <input id="exact-secret-name" ref={initialFocusRef} value={form.name} aria-invalid={Boolean(errors.name)} onChange={(e) => update({ name: e.target.value })} placeholder="例如 生产 OpenAI Key" className="desk-field w-full" />
      </FormFieldRow>
      <FormFieldRow label="分类">
        <CustomSelect options={PRIVACY_EXACT_SECRET_CATEGORY_OPTIONS} value={form.category} onChange={onCategoryChange} size="sm" className="w-full" />
      </FormFieldRow>
      <FormFieldRow label="占位符 *" htmlFor="exact-secret-placeholder" error={errors.placeholder}>
        <input id="exact-secret-placeholder" value={form.placeholder} aria-invalid={Boolean(errors.placeholder)} onChange={(e) => update({ placeholder: e.target.value })} className="desk-field w-full" />
      </FormFieldRow>
      <FormFieldRow label={form.id > 0 ? '替换敏感值' : '敏感值 *'} htmlFor="exact-secret-value" error={errors.secret_value}>
        <input id="exact-secret-value" type="password" value={form.secret_value} aria-invalid={Boolean(errors.secret_value)}
          onChange={(e) => update({ secret_value: e.target.value })} placeholder={form.id > 0 ? '留空则只更新名称/分类/占位符' : '粘贴完整敏感值'} className="desk-field w-full" />
      </FormFieldRow>
      <FormRow label="启用" description="关闭后保留记录但不参与出站扫描">
        <Switch checked={form.enabled} disabled={saving} label="启用" onChange={(enabled) => update({ enabled })} />
      </FormRow>
      <FormFieldRow label="描述" htmlFor="exact-secret-description">
        <textarea id="exact-secret-description" value={form.description} onChange={(e) => update({ description: e.target.value })} rows={3} className="desk-field h-auto w-full py-1.5 leading-relaxed" />
      </FormFieldRow>
      {form.id > 0 && <FormBlock><p className="text-xs text-fg-muted">当前值不会回显；列表只展示掩码和长度。</p></FormBlock>}
    </FormSection>
  </Sheet>
);

export const ImportCandidatesSheet = ({ open, loading, candidates, importingKey, error, initialFocusRef, onClose, onLoad, onImport }) => (
  <Sheet open={open} title="导入候选" label="导入候选" width={640} closeLabel="关闭弹窗"
    onClose={onClose} initialFocusRef={initialFocusRef}
    footerStart={error && <span role="alert" className="break-all text-danger">{error}</span>}>
    <FormSection title="导入候选" actions={<span ref={initialFocusRef} tabIndex={-1} className="outline-none"><Button type="button" size="sm" variant="secondary" onClick={onLoad} loading={loading}>刷新候选</Button></span>}>
      {candidates.map((candidate) => {
        const key = `${candidate.source_type}:${candidate.source_ref}`;
        return (
          <div key={key} className="flex items-center gap-3 px-3.5 py-2">
            <div className="min-w-0 flex-1">
              <div className="text-[13px] font-medium text-fg">{candidate.name}</div>
              <div className="mt-0.5 text-xs text-fg-muted">{exactSecretCategoryLabel(candidate.category)} · {candidate.masked_value} · {candidate.value_length} 字符</div>
            </div>
            <Button type="button" size="sm" variant={candidate.already_exists ? 'secondary' : 'primary'}
              loading={importingKey === key} disabled={candidate.already_exists || Boolean(importingKey)} onClick={() => onImport(candidate)}>
              {candidate.already_exists ? '已存在' : '导入'}
            </Button>
          </div>
        );
      })}
      {!loading && candidates.length === 0 && <p className="py-6 text-center text-xs text-fg-muted">暂无可导入候选</p>}
    </FormSection>
  </Sheet>
);

export const ManualImportSheet = ({ open, form, importing, error, initialFocusRef, onClose, onSubmit, update, onCategoryChange }) => (
  <Sheet open={open} title="粘贴导入" label="粘贴导入" width={640} closeLabel="关闭弹窗"
    onClose={onClose} onSubmit={onSubmit} initialFocusRef={initialFocusRef}
    footerStart={error && <span role="alert" className="break-all text-danger">{error}</span>}
    footer={<><Button type="button" variant="secondary" onClick={onClose} disabled={importing}>取消</Button><Button type="submit" loading={importing}>导入并生效</Button></>}>
    <FormSection title="粘贴导入">
      <FormFieldRow label="名称前缀" htmlFor="manual-secret-prefix">
        <input id="manual-secret-prefix" value={form.namePrefix} onChange={(e) => update({ namePrefix: e.target.value })} className="desk-field w-full" />
      </FormFieldRow>
      <FormFieldRow label="分类">
        <CustomSelect options={PRIVACY_EXACT_SECRET_CATEGORY_OPTIONS} value={form.category} onChange={onCategoryChange} size="sm" className="w-full" />
      </FormFieldRow>
      <FormFieldRow label="占位符 *" htmlFor="manual-secret-placeholder">
        <input id="manual-secret-placeholder" value={form.placeholder} onChange={(e) => update({ placeholder: e.target.value })} className="desk-field w-full" />
      </FormFieldRow>
      <FormFieldRow label="敏感值列表 *" htmlFor="manual-secret-values" hint="导入前会逐行 TrimSpace，并按完整字符串精确匹配。">
        <textarea id="manual-secret-values" ref={initialFocusRef} value={form.values} onChange={(e) => update({ values: e.target.value })}
          rows={9} placeholder="每行一个完整敏感值，空行会自动跳过" className="desk-field h-auto w-full py-1.5 font-mono leading-relaxed" />
      </FormFieldRow>
      <FormFieldRow label="描述" htmlFor="manual-secret-description">
        <textarea id="manual-secret-description" value={form.description} onChange={(e) => update({ description: e.target.value })} rows={2} className="desk-field h-auto w-full py-1.5 leading-relaxed" />
      </FormFieldRow>
    </FormSection>
  </Sheet>
);
