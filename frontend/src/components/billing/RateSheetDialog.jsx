import { useState } from 'react';
import { Plus, Trash2 } from 'lucide-react';
import Sheet from '@components/desktop/Sheet.jsx';
import { FormSection } from '@components/desktop/FormSection.jsx';
import { Button, CustomSelect } from '@components/ui';
import useConfirm from '@hooks/useConfirm.js';
import { useTimezone } from '@contexts/TimezoneContext.jsx';
import { resolveErrorMessage } from '@utils/errors.js';
import { RATE_SOURCE_LABELS, appendPeriod, buildSheet, nextPeriodIndices, parseLimits, priceColumns, sheetDraft, unconfiguredRateRows, withoutContextTiers, withoutPeriods } from './billingForms.js';
import { publishRateSheet } from './pricingApi.js';
import RatePeriods from './RatePeriods.jsx';
import BillingTimezoneSelect from './BillingTimezoneSelect.jsx';

const field = 'desk-field w-full min-w-0';
const remove = 'inline-flex p-1 text-fg-muted hover:text-danger';
const SOURCE_OPTIONS = [
  { value: 'custom', label: RATE_SOURCE_LABELS.custom },
  { value: 'official', label: RATE_SOURCE_LABELS.official }
];
const SPEED_OPTIONS = [
  { value: 'standard', label: '标准' },
  { value: 'fast', label: 'Fast' }
];
const tierLabel = (tier) => (tier ? `长档 ${tier}` : '基础档');

export default function RateSheetDialog({ model, info, onClose, onPublished }) {
  const { timezone: appTimezone } = useTimezone();
  const initialSource = info.source === 'official' ? 'official' : 'custom';
  const [original] = useState(() => sheetDraft(info.sheet));
  const [draft, setDraft] = useState(original);
  const [source, setSource] = useState(initialSource);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState('');
  // 输入小时和分钟时可能短暂出现空值、重复或乱序；整次编辑始终修改同一个相邻时段。
  const [endLinks, setEndLinks] = useState(null);
  const { confirm, confirmDialog } = useConfirm();
  const dirty = JSON.stringify(original) !== JSON.stringify(draft) || source !== initialSource;
  // 历史基础价可直接发布为所选来源的新版本，无需强迫改动单价；未做修改直接关闭则不弹放弃确认。
  const canPublish = dirty || info.source === 'legacy';
  const close = async () => {
    if (
      !saving &&
      (!dirty ||
        (await confirm({
          title: '放弃价表修改？',
          message: '草稿尚未发布。',
          confirmText: '放弃修改',
          danger: true
        })))
    )
      onClose();
  };
  const updateRow = (i, patch) => {
    setError('');
    setDraft((d) => ({ ...d, rows: d.rows.map((r, n) => (n === i ? { ...r, ...patch } : r)) }));
  };
  const updatePeriod = (i, patch) => {
    setError('');
    setDraft((d) => ({
      ...d,
      periods: d.periods.map((p, n) => (n === i ? { ...p, ...patch } : p))
    }));
  };
  const addPeriod = () => {
    setError('');
    setEndLinks(null);
    setDraft((d) => appendPeriod(d, appTimezone));
  };
  const removePeriod = (i) => {
    setError('');
    setEndLinks(null);
    setDraft((d) => d.periods.length === 1 ? withoutPeriods(d) : ({
      ...d,
      periods: d.periods.filter((_, n) => n !== i),
      rows: d.rows.filter((r) => r.period !== d.periods[i].key)
    }));
  };
  const periodEnabled = draft.periods.length > 0;
  const contextEnabled = draft.limits !== null;
  const togglePeriods = async (enabled) => {
    if (enabled) {
      addPeriod();
      return;
    }
    if (draft.periods.length > 1 && !(await confirm({
      title: '关闭分时计费？',
      message: `将「${draft.periods[0].name || '第一个时段'}」的费率用于全天，并从草稿中移除其他时段和对应费率。`,
      confirmText: '关闭分时计费',
      danger: true
    }))) return;
    setError('');
    setEndLinks(null);
    setDraft(withoutPeriods);
  };
  const toggleContext = async (enabled) => {
    if (enabled) {
      setError('');
      setDraft((d) => ({ ...d, limits: '' }));
      return;
    }
    if (draft.rows.some((r) => r.tier > 0) && !(await confirm({
      title: '关闭上下文分档？',
      message: '将保留各时段的基础档费率，并从草稿中移除阈值和所有长档费率。',
      confirmText: '关闭上下文分档',
      danger: true
    }))) return;
    setError('');
    setDraft(withoutContextTiers);
  };
  let limits;
  try {
    limits = parseLimits(draft.limits);
  } catch {
    limits = null;
  }
  const tiers = [0, ...(limits || []).map((_, i) => i + 1)];
  const periods = draft.periods.length ? draft.periods.map((p) => p.key) : [''];
  const nextIndices = endLinks ?? nextPeriodIndices(draft.periods);
  // 阈值能解析且为空、所有行都在基础档时才隐藏；阈值有误或有行在不存在的档位（需调整）时保留
  const showTierColumn = !(limits && limits.length === 0 && draft.rows.every((r) => Number(r.tier) === 0));
  const periodOptions = periods.map((v, j) => ({
    value: v,
    label: periodEnabled ? draft.periods[j].name.trim() || `时段 ${j + 1}` : '全天'
  }));
  const tierOptions = (row) => [
    ...(tiers.includes(row.tier) ? [] : [{ value: row.tier, label: `长档 ${row.tier}（需调整）` }]),
    ...tiers.map((v) => ({ value: v, label: tierLabel(v) }))
  ];
  const { missing, next } = unconfiguredRateRows(draft.rows, periods, tiers);
  const showMissing = missing.length > 0 && (!contextEnabled || limits?.length > 0);
  const describeRow = ({ period, tier, fast }) => [
    periodEnabled && (periodOptions.find(({ value }) => value === period)?.label || period),
    contextEnabled && tierLabel(tier),
    SPEED_OPTIONS[Number(fast)].label
  ].filter(Boolean).join(' · ');
  const addRow = () => {
    if (next) {
      setError('');
      setDraft((d) => ({ ...d, rows: [...d.rows, next] }));
    } else setError('现有时段、上下文和速度组合均已配置。');
  };
  const publish = async (e) => {
    e.preventDefault();
    e.stopPropagation();
    setError('');
    try {
      const sheet = buildSheet(draft, endLinks);
      setSaving(true);
      await publishRateSheet(model, source, sheet);
      await onPublished();
    } catch (err) {
      setError(resolveErrorMessage(err, '发布价表失败'));
    } finally {
      setSaving(false);
    }
  };

  return (
    <>
      <Sheet
        centered
        noValidate
        title="模型价表"
        subtitle={model}
        width={940}
        onClose={close}
        onSubmit={publish}
        closeDisabled={saving}
        footerStart={`当前版本 #${info.version_id} · ${RATE_SOURCE_LABELS[info.source] || info.source}`}
        footer={
          <>
            <Button type="button" variant="ghost" onClick={close} disabled={saving}>
              取消
            </Button>
            <Button type="submit" loading={saving} disabled={!canPublish}>
              发布新版本
            </Button>
          </>
        }
      >
        <fieldset className="@container min-w-0" disabled={saving}>
          <section aria-label="价表设置" className="mt-4 divide-y divide-line-soft rounded-[10px] border border-line bg-surface">
            <div className="flex flex-wrap items-start justify-between gap-x-8 gap-y-3 px-3.5 py-3">
              <div className="min-w-0" role="group" aria-labelledby="rate-sheet-source-label">
                <div id="rate-sheet-source-label" className="mb-1.5 text-xs font-medium text-fg">价表来源</div>
                <div className="flex flex-wrap items-center gap-x-3 gap-y-2">
                  <CustomSelect
                    className="w-32 shrink-0"
                    size="md"
                    ariaLabel="价表来源"
                    options={SOURCE_OPTIONS}
                    value={source}
                    onChange={setSource}
                  />
                  {info.builtin && (
                    <Button
                      type="button"
                      size="sm"
                      variant="secondary"
                      onClick={() => {
                        setDraft(sheetDraft(info.builtin));
                        setEndLinks(null);
                        setSource('official');
                        setError('');
                      }}
                    >
                      载入内置官方价
                    </Button>
                  )}
                </div>
                {source === 'official' && <p className="mt-1 text-[11px] text-fg-muted">官方来源支持历史价复算。</p>}
                {info.source === 'legacy' && <p className="mt-1 text-[11px] text-fg-muted">当前为历史基础价，发布后将转为所选来源的新版本。</p>}
              </div>
              <fieldset className="min-w-0">
                <legend className="mb-1.5 text-xs font-medium text-fg">计费规则</legend>
                <div className="flex min-h-6 flex-wrap items-center gap-x-5 gap-y-2 text-xs text-fg-body">
                  <label className="inline-flex cursor-pointer items-center gap-1.5">
                    <input type="checkbox" className="app-checkbox" checked={periodEnabled} onChange={(e) => togglePeriods(e.target.checked)} />
                    分时计费
                  </label>
                  <label className="inline-flex cursor-pointer items-center gap-1.5">
                    <input type="checkbox" className="app-checkbox" checked={contextEnabled} onChange={(e) => toggleContext(e.target.checked)} />
                    上下文分档
                  </label>
                </div>
              </fieldset>
            </div>
            {(periodEnabled || contextEnabled) && (
              <div className="grid gap-x-6 gap-y-3 px-3.5 py-3 @sm:grid-cols-2">
                {periodEnabled && (
                  <BillingTimezoneSelect
                    value={draft.timezone}
                    appTimezone={appTimezone}
                    onChange={(timezone) => setDraft((d) => ({ ...d, timezone }))}
                  />
                )}
                {contextEnabled && (
                  <label className="min-w-0">
                    <span className="mb-1.5 block text-xs font-medium text-fg">上下文阈值 <span className="font-normal text-fg-muted">（Token）</span></span>
                    <input
                      aria-label="上下文阈值"
                      aria-describedby="rate-sheet-context-hint"
                      required
                      className={field}
                      placeholder="例如 128000, 200000"
                      value={draft.limits}
                      onChange={(e) => {
                        setError('');
                        setDraft((d) => ({ ...d, limits: e.target.value }));
                      }}
                    />
                    <span id="rate-sheet-context-hint" className="mt-1 block text-[11px] text-fg-muted">超过阈值进入下一档，多个值用逗号分隔。</span>
                  </label>
                )}
              </div>
            )}
          </section>
          {periodEnabled && (
            <RatePeriods
              periods={draft.periods}
              nextIndices={nextIndices}
              onAdd={addPeriod}
              onRemove={removePeriod}
              onName={(i, name) => updatePeriod(i, { name })}
              onStart={(i, time) => {
                setEndLinks(null);
                updatePeriod(i, { time });
              }}
              onEnd={(next, time) => {
                setEndLinks(nextIndices);
                updatePeriod(next, { time });
              }}
            />
          )}
          <FormSection
            title="各档单价"
            description="美元 / 百万 Token。缓存价留空表示未知，填 0 表示免费。"
            actions={
              <Button type="button" size="sm" variant="ghost" icon={Plus} onClick={addRow}>
                添加费率行
              </Button>
            }
          >
            <div className="overflow-x-auto rounded-[10px]">
              <table className="w-full min-w-[760px] table-fixed text-[12px]">
                <thead className="bg-surface-mut/60 text-fg-muted">
                  <tr>
                    {[
                      periodEnabled && '时段',
                      showTierColumn && '上下文',
                      '速度',
                      ...priceColumns.map((c) => c[1])
                    ]
                      .filter(Boolean)
                      .map((h) => (
                        <th key={h} className={`px-2 py-2 font-medium ${priceColumns.some(([, label]) => label === h) ? 'text-right' : 'text-left'}`}>
                          {h}
                        </th>
                      ))}
                    <th className="w-7" aria-label="操作" />
                  </tr>
                </thead>
                <tbody>
                  {draft.rows.map((row, i) => (
                    <tr key={i} className="border-t border-line-soft">
                      {periodEnabled && (
                        <td className="p-1">
                          <CustomSelect
                            size="xs"
                            className="w-full"
                            ariaLabel={`第 ${i + 1} 行时段`}
                            options={periodOptions}
                            value={row.period}
                            onChange={(period) => updateRow(i, { period })}
                          />
                        </td>
                      )}
                      {showTierColumn && (
                        <td className="p-1">
                          <CustomSelect
                            size="xs"
                            className="w-full"
                            ariaLabel={`第 ${i + 1} 行上下文`}
                            options={tierOptions(row)}
                            value={row.tier}
                            onChange={(tier) => updateRow(i, { tier: Number(tier) })}
                          />
                        </td>
                      )}
                      <td className="p-1">
                        <CustomSelect
                          size="xs"
                          className="w-full"
                          ariaLabel={`第 ${i + 1} 行速度`}
                          options={SPEED_OPTIONS}
                          value={row.fast ? 'fast' : 'standard'}
                          onChange={(speed) => updateRow(i, { fast: speed === 'fast' })}
                        />
                      </td>
                      {priceColumns.map(([key, label]) => (
                        <td key={key} className="p-1">
                          <input
                            aria-label={`第 ${i + 1} 行${label}价`}
                            inputMode="decimal"
                            className={`${field} text-right tabular-nums`}
                            value={row[key] ?? ''}
                            placeholder="—"
                            onChange={(e) => updateRow(i, { [key]: e.target.value })}
                          />
                        </td>
                      ))}
                      <td>
                        <button
                          type="button"
                          className={remove}
                          aria-label={`移除第 ${i + 1} 行`}
                          onClick={() => setDraft((d) => ({ ...d, rows: d.rows.filter((_, n) => n !== i) }))}
                        >
                          <Trash2 size={13} />
                        </button>
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
            {showMissing && (
              <div className="px-3 py-2 text-xs">
                <p className="text-warn">{missing.length} 个费率组合未配置</p>
                <p className="mt-1 break-words text-fg-muted">
                  {missing.slice(0, 4).map(describeRow).join('、')}
                </p>
                {missing.length > 4 && (
                  <details className="mt-1 text-fg-muted">
                    <summary className="cursor-pointer">其余 {missing.length - 4} 个组合</summary>
                    <p className="mt-1 max-h-32 overflow-auto break-words">{missing.slice(4).map(describeRow).join('、')}</p>
                  </details>
                )}
              </div>
            )}
          </FormSection>
          {error && (
            <p role="alert" className="mt-3 text-xs text-danger">
              {error}
            </p>
          )}
        </fieldset>
      </Sheet>
      {confirmDialog}
    </>
  );
}
