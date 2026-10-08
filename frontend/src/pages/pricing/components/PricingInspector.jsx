// ============================================
// 基础定价检查器：选中模型后就地编辑，或新建一条定价
// 2026-10-07：替代原来的编辑弹窗
// ============================================
// 父组件用 key 在切换模型 / 保存后重挂载本组件，表单初值直接取自 pricing。

import { useEffect, useState } from 'react';
import { AlertCircle, Sparkles, Star, Trash2 } from 'lucide-react';
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
import { useTimezone } from '@contexts/TimezoneContext.jsx';
import { resolveErrorMessage } from '@utils/errors.js';
import {
  PRICE_FIELDS,
  isPricingFormDirty,
  suggestCachePrices,
  toPricingForm,
  toPricingPayload,
  validatePricingForm
} from '../utils/pricingForm.js';

const PricingInspector = ({ pricing, isNew, saving, onSave, onCancelNew, onDelete, onDirtyChange }) => {
  const { formatTimestamp } = useTimezone();
  const [base] = useState(() => toPricingForm(isNew ? null : pricing));
  const [form, setForm] = useState(base);
  const [errors, setErrors] = useState({});
  const [submitError, setSubmitError] = useState(null);
  const dirty = isPricingFormDirty(form, base);

  useEffect(() => {
    onDirtyChange?.(isNew || dirty);
  }, [isNew, dirty, onDirtyChange]);

  const update = (field, value) => {
    setForm((previous) => ({ ...previous, [field]: value }));
    if (errors[field]) setErrors((previous) => ({ ...previous, [field]: undefined }));
  };

  const submit = async (event) => {
    event.preventDefault();
    const nextErrors = validatePricingForm(form, { isNew });
    setErrors(nextErrors);
    if (Object.keys(nextErrors).length > 0) return;
    setSubmitError(null);
    try {
      await onSave(toPricingPayload(form));
    } catch (error) {
      setSubmitError(resolveErrorMessage(error, '保存失败'));
    }
  };

  const priceError = PRICE_FIELDS.some(({ key }) => errors[key]);

  return (
    <Inspector label="定价详情">
      <form onSubmit={submit} className="flex min-h-0 flex-1 flex-col">
        <InspectorHeader
          title={isNew ? '新建定价' : (pricing.displayName || pricing.modelName)}
          subtitle={isNew ? 'USD / 1M tokens' : pricing.modelName}
          badges={!isNew && pricing.isDefault && (
            <span className="inline-flex shrink-0 items-center gap-1 rounded border border-warn-line px-1.5 text-[11px] font-medium text-warn">
              <Star size={10} fill="currentColor" />默认
            </span>
          )}
        />

        <InspectorBody>
          {submitError && (
            <div role="alert" className="selectable mx-4 mt-3 flex items-start gap-2 rounded-md border border-danger-line bg-danger-soft px-2.5 py-2 text-xs text-danger">
              <AlertCircle size={14} className="mt-px shrink-0" />{submitError}
            </div>
          )}

          <InspectorSection title="基本信息">
            <PropertyList>
              <Property label="模型 ID" truncate={!isNew}>
                {isNew ? (
                  <input
                    className="desk-field w-full font-mono"
                    value={form.modelName}
                    onChange={(event) => update('modelName', event.target.value)}
                    placeholder="例如 claude-sonnet-5"
                    aria-invalid={Boolean(errors.modelName)}
                    title={errors.modelName}
                    autoFocus
                  />
                ) : <span className="font-mono text-[12px]">{pricing.modelName}</span>}
              </Property>
              <Property label="显示名" truncate={false}>
                <input className="desk-field w-full" value={form.displayName} onChange={(event) => update('displayName', event.target.value)} placeholder="例如 Claude Sonnet 5" />
              </Property>
              <Property label="说明" truncate={false}>
                <input className="desk-field w-full" value={form.description} onChange={(event) => update('description', event.target.value)} placeholder="可选" />
              </Property>
            </PropertyList>
            {errors.modelName && <p className="mt-2 pl-[94px] text-[11.5px] text-danger">{errors.modelName}</p>}
          </InspectorSection>

          <InspectorSection
            title="价格 · USD / 1M tokens"
            action={(
              <button type="button" onClick={() => setForm(suggestCachePrices)} className="inline-flex items-center gap-1 text-[11.5px] text-accent hover:text-accent-fg" title="缓存写 5m = 输入 × 1.25，1h = 输入 × 2，读取 = 输入 × 0.1">
                <Sparkles size={12} />按输入价推算缓存价
              </button>
            )}
          >
            <PropertyList>
              {PRICE_FIELDS.map(({ key, label, hint }) => (
                <Property key={key} label={label} truncate={false}>
                  <span className="relative inline-flex w-[132px] items-center">
                    <span className="pointer-events-none absolute left-2 text-xs text-fg-subtle">$</span>
                    <input
                      className="desk-field w-full pl-[18px] text-right tabular-nums"
                      inputMode="decimal"
                      value={form[key]}
                      onChange={(event) => update(key, event.target.value)}
                      aria-invalid={Boolean(errors[key])}
                      aria-label={`${label}价格`}
                      title={errors[key] || hint}
                    />
                  </span>
                </Property>
              ))}
            </PropertyList>
            {priceError && <p className="mt-2 pl-[94px] text-[11.5px] text-danger">价格须为不小于 0 的数字</p>}
          </InspectorSection>

          <InspectorSection>
            <label className="flex items-center gap-2 text-[12.5px] text-fg-body">
              <input
                type="checkbox"
                className="app-checkbox"
                checked={form.isDefault}
                disabled={!isNew && pricing.isDefault}
                onChange={(event) => update('isDefault', event.target.checked)}
              />
              未知模型使用此定价
            </label>
            {!isNew && pricing.isDefault && <p className="mt-1.5 pl-6 text-[11.5px] text-fg-subtle">当前默认定价；把其他模型设为默认后这里会自动取消</p>}
            {!isNew && <p className="mt-2 text-[11.5px] text-fg-subtle">更新于 {pricing.updatedAt ? formatTimestamp(pricing.updatedAt) : '—'}</p>}
          </InspectorSection>
        </InspectorBody>

        <InspectorFooter>
          {!isNew && (
            <ToolbarButton
              icon={Trash2}
              title={pricing.isDefault ? '默认定价不能删除' : '删除定价'}
              disabled={pricing.isDefault || saving}
              onClick={() => onDelete(pricing)}
            />
          )}
          <span className="flex-1" />
          {isNew
            ? <Button type="button" variant="secondary" onClick={onCancelNew} disabled={saving}>取消</Button>
            : <Button type="button" variant="secondary" onClick={() => { setForm(base); setErrors({}); setSubmitError(null); }} disabled={!dirty || saving}>还原</Button>}
          <Button type="submit" loading={saving} disabled={!isNew && !dirty}>{isNew ? '创建' : '保存'}</Button>
        </InspectorFooter>
      </form>
    </Inspector>
  );
};

export default PricingInspector;
