// ============================================
// 基础定价检查器：选中模型后就地编辑，或新建一条定价
// 2026-10-07：替代原来的编辑弹窗
// ============================================
// 父组件用 key 在切换模型 / 保存后重挂载本组件，表单初值直接取自 pricing。

import RateSheetDialog from '@components/billing/RateSheetDialog.jsx';
import { RATE_SOURCE_LABELS, priceColumns } from '@components/billing/billingForms.js';
import { getRateEditor } from '@components/billing/pricingApi.js';
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

const PricingInspector = ({ pricing, isNew, saving, onSave, onCancelNew, onDelete, onDirtyChange, onPublished }) => {
  const { formatTimestamp } = useTimezone();
  const [base] = useState(() => toPricingForm(isNew ? null : pricing));
  const [form, setForm] = useState(base);
  const [errors, setErrors] = useState({});
  const [submitError, setSubmitError] = useState(null);
  const [rateInfo, setRateInfo] = useState(null);
  const [rateError, setRateError] = useState('');
  const [rateOpen, setRateOpen] = useState(false);
  const [retry, setRetry] = useState(0);
  const model = pricing?.modelName;
  useEffect(() => {
    if (!model) return;
    let active = true;
    getRateEditor(model).then(info => { if (active) setRateInfo(info); })
      .catch(err => { if (active) setRateError(resolveErrorMessage(err, '读取价表失败')); });
    return () => { active = false; };
  }, [model, retry]);
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
      await onSave(toPricingPayload(form, isNew ? null : base));
    } catch (error) {
      setSubmitError(resolveErrorMessage(error, '保存失败'));
    }
  };

  // 数字表单不能表达空价；包含未知缓存价的历史模型也通过价表编辑。
  const rateRows = rateInfo?.sheet?.rows || [];
  const basePeriod = rateInfo?.sheet?.periods?.[0]?.name || '';
  const baseRow = rateRows.find(row => (row.period || '') === basePeriod && !row.tier && !row.fast);
  const needsSheetEditor = rateInfo?.structured ||
    (baseRow && [baseRow.cache_5m, baseRow.cache_1h, baseRow.cache_read].some(price => price == null));
  const rateState = isNew ? 'new'
    : rateError ? 'error'
    : !rateInfo ? 'loading'
    : needsSheetEditor ? 'sheet' : 'legacy';
  const showEditableFields = rateState === 'new' || rateState === 'legacy';
  const priceTitle = rateState === 'sheet' && rateRows.length > 1
    ? '基础价 · USD / 1M tokens'
    : '价格 · USD / 1M tokens';
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
            title={priceTitle}
            action={showEditableFields && (
              <button type="button" onClick={() => setForm(suggestCachePrices)} className="inline-flex items-center gap-1 text-[11.5px] text-accent hover:text-accent-fg" title="缓存写 5m = 输入 × 1.25，1h = 输入 × 2，读取 = 输入 × 0.1">
                <Sparkles size={12} />按输入价推算缓存价
              </button>
            )}
          >
            {rateState === 'loading' && <p className="text-xs text-fg-muted">正在读取价表…</p>}
            {rateState === 'error' && (
              <div role="alert" className="flex items-center justify-between gap-2 text-xs text-danger">
                <span className="min-w-0">{rateError}</span>
                <Button type="button" size="sm" variant="secondary" onClick={() => { setRateError(''); setRetry(n => n + 1); }}>重试</Button>
              </div>
            )}
            {rateState === 'sheet' && (
              <PropertyList>
                {priceColumns.map(([key, label]) => (
                  <Property key={key} label={label}>
                    <span className="tabular-nums">{baseRow?.[key] == null ? '未配置' : `$${baseRow[key]}`}</span>
                  </Property>
                ))}
              </PropertyList>
            )}
            {showEditableFields && (
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
            )}
            {rateInfo && !rateError && !isNew && (
              <div className="mt-2 flex items-center justify-between gap-2">
                <span className="min-w-0 text-xs text-fg-muted">
                  版本 #{rateInfo.version_id} · {RATE_SOURCE_LABELS[rateInfo.source] || rateInfo.source} · {rateInfo.sheet.rows.length} 条费率
                </span>
                <Button
                  type="button"
                  size="sm"
                  variant="secondary"
                  disabled={dirty || saving}
                  title={dirty || saving ? '保存或还原当前修改后可编辑价表' : undefined}
                  onClick={() => setRateOpen(true)}
                >
                  编辑价表
                </Button>
              </div>
            )}
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
              设为默认定价
            </label>
            <p className="mt-1.5 pl-6 text-[11.5px] text-fg-subtle">未单独定价的模型按默认定价的基础单价估算费用。</p>
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
      {rateOpen && (
        <RateSheetDialog
          model={model}
          info={rateInfo}
          onClose={() => setRateOpen(false)}
          onPublished={async () => {
            setRateOpen(false);
            setRateError('');
            setRetry((n) => n + 1);
            await onPublished();
          }}
        />
      )}
    </Inspector>
  );
};

export default PricingInspector;
