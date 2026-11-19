import { useEffect, useLayoutEffect, useMemo, useRef, useState } from 'react';
import { Plus, Trash2 } from 'lucide-react';
import Sheet from '@components/desktop/Sheet.jsx';
import { FormRow, FormSection } from '@components/desktop/FormSection.jsx';
import { Button, CustomSelect } from '@components/ui';
import { resolveErrorMessage } from '@utils/errors.js';
import { getModelPricings } from '@utils/wailsApi.js';
import { getRateEditor, getChannelRule, getChannelRules, saveChannelRule } from './pricingApi.js';
import { buildRule, normalizeRuleDraft, ruleDraft } from './billingForms.js';

const emptyTier = () => ({ threshold: '', multiplier: '' });

export function ChannelFormSheet({ billingChannel: { type, id, name = '' }, backLabel, children, ...parent }) {
  const target = useMemo(() => ({ type, id, name }), [type, id, name]);
  const [editing, setEditing] = useState(!children);
  const channel = editing ? target : null;
  const bodyRef = useRef(null);
  const origin = useRef(null);
  const ruleFields = useRef(null);
  const [models, setModels] = useState(null);
  const [selectedModel, setModel] = useState('');
  const [rules, setRules] = useState(null);
  const [listError, setListError] = useState('');
  const [revision, refreshRules] = useState(0);
  const reloadRules = () => refreshRules((n) => n + 1);
  const orderedModels = useMemo(() => {
    if (!models) return [];
    const configured = (m) => Object.hasOwn(rules || {}, m.modelName);
    return [...models.filter(configured), ...models.filter((m) => !configured(m))];
  }, [models, rules]);
  const model = selectedModel || ((rules || listError) && orderedModels[0]?.modelName) || '';
  const [record, setRecord] = useState(null);
  const [draft, setDraft] = useState(null);
  const [error, setError] = useState('');
  const [attempt, retry] = useState(0);
  const [saving, setSaving] = useState(false);
  const [saved, setSaved] = useState('');
  // 空字符串代表返回上一页，模型 ID 代表切换模型。
  const [pending, setPending] = useState(null);
  const ready = record?.model === model && draft;
  const dirty = ready && JSON.stringify(normalizeRuleDraft(draft)) !== JSON.stringify(normalizeRuleDraft(record.base));
  const openRules = (trigger, preferredModel = '') => {
    origin.current = { trigger, scrollTop: bodyRef.current?.scrollTop ?? 0 };
    setModel(preferredModel);
    setEditing(true);
  };
  const navigate = (next) => {
    setPending(null);
    setRecord(null);
    setDraft(null);
    setSaved('');
    setError('');
    setModel(next);
    if (!next) {
      setModels(null);
      if (children) setEditing(false);
      else parent.onClose();
    }
  };
  const requestNavigation = (next) => {
    if (saving) return;
    if (dirty) setPending(next);
    else navigate(next);
  };

  useLayoutEffect(() => {
    if (channel) {
      if (bodyRef.current) bodyRef.current.scrollTop = 0;
      ruleFields.current?.focus({ preventScroll: true });
    } else if (origin.current) {
      if (bodyRef.current) bodyRef.current.scrollTop = origin.current.scrollTop;
      origin.current.trigger?.focus?.({ preventScroll: true });
      origin.current = null;
    }
  }, [channel]);

  useEffect(() => {
    if (!target.id && target.type !== 'subscription') return;
    let active = true;
    getChannelRules(target).then((current) => {
      if (active) {
        setRules(current);
        setListError('');
      }
    }).catch((err) => {
      if (active) setListError(resolveErrorMessage(err, '读取已配置的模型规则失败'));
    });
    return () => { active = false; };
  }, [target, revision]);

  useEffect(() => {
    if (!channel) return;
    let active = true;
    getModelPricings()
      .then((list) => {
        if (!active) return;
        setModels(list);
      })
      .catch((err) => {
        if (active) setError(resolveErrorMessage(err, '获取模型列表失败'));
      });
    return () => {
      active = false;
    };
  }, [channel, attempt]);

  useEffect(() => {
    if (!channel || !model) return;
    let active = true;
    Promise.all([getRateEditor(model), getChannelRule(channel, model)])
      .then(([info, rule]) => {
        if (!active) return;
        const base = ruleDraft(rule);
        setRecord({ model, base, info });
        setDraft(base);
      })
      .catch((err) => {
        if (active) setError(resolveErrorMessage(err, '读取渠道规则失败'));
      });
    return () => {
      active = false;
    };
  }, [channel, model, attempt]);

  const save = async (e) => {
    e.preventDefault();
    e.stopPropagation();
    if (!dirty || saving || pending !== null) return;
    setError('');
    try {
      const rule = buildRule(draft, record.info.sheet.context_limits || []);
      setSaving(true);
      await saveChannelRule(channel, model, rule);
      setModel(model);
      reloadRules();
      const base = ruleDraft(rule);
      setDraft(base);
      setRecord({ ...record, base });
      setSaved('已保存');
    } catch (err) {
      setError(resolveErrorMessage(err, '保存渠道规则失败'));
    } finally {
      setSaving(false);
    }
  };
  const change = (patch) => {
    setSaved('');
    setError('');
    setDraft({ ...draft, ...patch });
  };
  const limits = record?.info?.sheet?.context_limits || [];
  const periodNames = (record?.info?.sheet?.periods || []).map((p) => p.name);
  const periodRemoved = Boolean(draft?.period && !periodNames.includes(draft.period));
  // 价表没有时段、且当前没有固定时段时，时段选择没有意义，整行隐藏
  const showPeriod = periodNames.length > 0 || Boolean(draft?.period);
  const periodOptions = [
    { value: '', label: '跟随实际时段' },
    ...(periodRemoved ? [{ value: draft.period, label: `固定 · ${draft.period}（已移除，需调整）` }] : []),
    ...periodNames.map((name) => ({ value: name, label: `固定 · ${name}` }))
  ];
  const tierCount = Math.max(limits.length, draft?.tiers.length || 0);
  const editTier = (i, field, value) => {
    const tiers = Array.from({ length: tierCount }, (_, n) => draft.tiers[n] || emptyTier());
    tiers[i] = { ...tiers[i], [field]: value };
    change({ tiers });
  };

  const rulePage = channel && {
    title: '模型计费规则',
    label: '模型计费规则',
    subtitle: children ? channel.name : parent.subtitle,
    backLabel,
    onClose: () => pending !== null ? setPending(null) : requestNavigation(''),
    onSubmit: save,
    closeDisabled: saving,
    footerStart: pending !== null
      ? <span role="alert">当前模型的修改尚未保存，要放弃吗？</span>
      : <span role="status">{saved || (dirty ? '规则有未保存的修改' : '')}</span>,
    footer: pending !== null ? (
      <>
        <Button type="button" variant="secondary" autoFocus onClick={() => setPending(null)}>继续编辑</Button>
        <Button type="button" variant="danger" onClick={(event) => {
          // 返回后同一位置是主表单的提交按钮，取消这次点击的默认提交行为。
          event.preventDefault();
          navigate(pending);
        }}>放弃修改</Button>
      </>
    ) : (
      <>
        <Button type="button" variant="secondary" disabled={!ready || saving} onClick={() => change(ruleDraft({}))}>
          恢复跟随官方
        </Button>
        <Button type="submit" loading={saving} disabled={!dirty}>保存模型规则</Button>
      </>
    )
  };

  return (
    <Sheet {...parent} {...rulePage} bodyRef={bodyRef}>
      {children && (
        <fieldset className="min-w-0" hidden={Boolean(channel)} disabled={Boolean(channel)}>
          {children({ channel: target, onEditRules: openRules, rules, listError, reloadRules })}
        </fieldset>
      )}
      {channel && (
        <fieldset ref={ruleFields} tabIndex={-1} className="min-w-0 outline-none" aria-label="编辑模型计费规则"
          disabled={saving || pending !== null}>
          <FormSection title="计费模型">
            <FormRow label="模型">
              <CustomSelect
                className="w-64 max-w-full"
                size="md"
                ariaLabel="计费模型"
                options={orderedModels.map(({ modelName }) => ({
                  value: modelName,
                  label: Object.hasOwn(rules || {}, modelName) ? `${modelName} · 已配置` : modelName
                }))}
                value={model}
                disabled={!models?.length}
                onChange={(next) => { if (next !== model) requestNavigation(next); }}
              />
            </FormRow>
          </FormSection>
          {listError && <p role="alert" className="py-2 text-xs text-danger">{listError} <button type="button" className="underline" onClick={reloadRules}>重试</button></p>}
          {!ready && !error && (
            <p className="py-3 text-xs text-fg-muted">
              {models?.length === 0 ? '请先在模型定价页添加模型。' : '正在加载计费配置…'}
            </p>
          )}
          {ready && (
            <>
              <FormSection title={showPeriod ? '速度与时段' : '速度'}>
                <FormRow
                  label="Fast 倍率"
                  description="填写后按标准速度价 × 倍率"
                >
                  <input
                    aria-label="Fast 倍率"
                    inputMode="decimal"
                    className="desk-field w-28 text-right"
                    value={draft.fast}
                    placeholder="跟随官方"
                    onChange={(e) => change({ fast: e.target.value })}
                  />
                </FormRow>
                {showPeriod && (
                  <FormRow
                    label="渠道计费时段"
                    description={periodRemoved ? '该时段已从基础价表移除，请改选有效时段或跟随实际时段' : undefined}
                  >
                    <CustomSelect
                      className="w-48"
                      size="md"
                      ariaLabel="渠道计费时段"
                      options={periodOptions}
                      value={draft.period}
                      onChange={(period) => change({ period })}
                    />
                  </FormRow>
                )}
              </FormSection>
              <FormSection
                title="长上下文"
                description="输入超过阈值时进入对应档。"
                actions={
                  <Button
                    type="button"
                    size="sm"
                    variant="ghost"
                    icon={Plus}
                    onClick={() =>
                      change({
                        tiers: [
                          ...Array.from({ length: tierCount }, (_, n) => draft.tiers[n] || emptyTier()),
                          emptyTier()
                        ]
                      })
                    }
                  >
                    添加长档
                  </Button>
                }
              >
                {tierCount === 0 && <div className="p-3 text-xs text-fg-muted">此模型使用基础上下文价。</div>}
                {Array.from({ length: tierCount }, (_, i) => (
                  <div
                    key={i}
                    className="flex flex-wrap items-end gap-3 border-b border-line-soft p-3 text-xs last:border-0"
                  >
                    <span className="mb-1.5 font-medium">长档 {i + 1}</span>
                    <label className="min-w-28 flex-1 text-fg-muted">
                      输入超过（Token）
                      <input
                        inputMode="numeric"
                        aria-label={`长档 ${i + 1} 阈值`}
                        className="desk-field mt-1 w-full"
                        value={draft.tiers[i]?.threshold ?? ''}
                        placeholder={limits[i] ? `继承 ${limits[i]}` : '填写阈值'}
                        onChange={(e) => editTier(i, 'threshold', e.target.value)}
                      />
                    </label>
                    <label className="min-w-24 flex-1 text-fg-muted">
                      倍率
                      <input
                        inputMode="decimal"
                        aria-label={`长档 ${i + 1} 倍率`}
                        className="desk-field mt-1 w-full"
                        value={draft.tiers[i]?.multiplier ?? ''}
                        placeholder={limits[i] ? '跟随官方' : '填写倍率'}
                        onChange={(e) => editTier(i, 'multiplier', e.target.value)}
                      />
                    </label>
                    {i >= limits.length && (
                      <button
                        type="button"
                        aria-label={`移除长档 ${i + 1}`}
                        onClick={() => change({ tiers: draft.tiers.filter((_, n) => n !== i) })}
                        className="mb-1 p-1 text-fg-muted hover:text-danger"
                      >
                        <Trash2 size={14} />
                      </button>
                    )}
                  </div>
                ))}
              </FormSection>
              {[draft.fast, ...draft.tiers.map((t) => t.multiplier)].some(
                (v) => v.trim() !== '' && Number(v) === 0
              ) && <p className="mt-3 text-xs text-warn">命中 ×0 后，整笔渠道费用为 0。</p>}
            </>
          )}
          {error && (
            <div role="alert" className="mt-3 text-xs text-danger">
              {error}
              {!ready && (
                <button
                  type="button"
                  className="ml-2 underline"
                  onClick={() => {
                    setError('');
                    retry((n) => n + 1);
                  }}
                >
                  重试
                </button>
              )}
            </div>
          )}
        </fieldset>
      )}
    </Sheet>
  );
}

export default function ChannelRuleEditor({ type, id = 0, name, label, disabled = false }) {
  const [channel, setChannel] = useState(null);
  return (
    <>
      <Button
        type="button"
        size="sm"
        variant="secondary"
        disabled={disabled}
        onClick={() => setChannel({ type, id, name })}
      >
        配置模型规则
      </Button>
      {channel && <ChannelFormSheet billingChannel={channel} subtitle={label || name} width={630} onClose={() => setChannel(null)} />}
    </>
  );
}
