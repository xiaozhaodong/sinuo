// 表单保留输入文本，提交时才转换。可选价格/倍率留空与显式零有不同含义。
export const RATE_SOURCE_LABELS = { official: '官方', custom: '自定义', legacy: '历史基础价' };

export const COST_MODE_OPTIONS = [
  { value: 'unified', label: '统一' },
  { value: 'split', label: '分项' }
];

export const SPLIT_MULTIPLIER_KEYS = [
  'inputCostMultiplier',
  'outputCostMultiplier',
  'cacheReadCostMultiplier',
  'cacheCreationCostMultiplier',
  'cacheCreationCostMultiplier1h'
];

export const splitMultiplierKeysForChannel = (channelType) => (
  channelType === 'account'
    ? SPLIT_MULTIPLIER_KEYS.filter((key) => key !== 'cacheCreationCostMultiplier1h')
    : SPLIT_MULTIPLIER_KEYS
);

// 总倍率恰为 0 表示改用分项倍率结算；留空不算 0。
export const isSplitMultiplier = (value) => String(value ?? '').trim() !== '' && Number(value) === 0;

// 统一模式下总倍率须是大于 0 的有限数。allowEmpty 为真时留空放行（账号表单留空由后端按 1 处理）。
export const isUnifiedMultiplierInvalid = (value, allowEmpty = false) => {
  const raw = String(value ?? '').trim();
  if (raw === '') return !allowEmpty;
  const n = Number(raw);
  return !(Number.isFinite(n) && n > 0);
};

// 校验渠道倍率配置：统一模式校验总倍率，分项模式拦截非空且不大于 0 的可见分项（避免保存时被静默改回 1.0）。
export const validateCostMultipliers = (mode, values = {}, { allowEmptyUnified = false, channelType = '' } = {}) => {
  if (mode === 'unified') {
    return isUnifiedMultiplierInvalid(values.costMultiplier, allowEmptyUnified)
      ? { error: '统一倍率须大于 0', invalidFields: ['costMultiplier'] }
      : { error: '', invalidFields: [] };
  }
  const keys = splitMultiplierKeysForChannel(channelType);
  const invalidFields = keys.filter((key) => isUnifiedMultiplierInvalid(values[key], true));
  return invalidFields.length > 0
    ? { error: '分项倍率须大于 0，留空按 1.0 处理', invalidFields }
    : { error: '', invalidFields: [] };
};

export const priceColumns = [
  ['input', '输入'],
  ['output', '输出'],
  ['cache_read', '缓存读'],
  ['cache_5m', '写入 5m'],
  ['cache_1h', '写入 1h'],
  ['cache_30m', '写入 30m'],
  ['cache_12h', '写入 12h']
];

const decimal = (value, label, optional = false) => {
  const raw = String(value ?? '').trim();
  if (!raw && optional) return null;
  if (!raw || !/^(?:\d+(?:\.\d*)?|\.\d+)(?:e[+-]?\d+)?$/i.test(raw) || !Number.isFinite(Number(raw))) {
    throw new Error(`${label}须为非负有限数`);
  }
  return Number(raw);
};

const threshold = (value) => {
  const n = decimal(value, '上下文阈值');
  if (!Number.isSafeInteger(n) || n < 1) throw new Error('上下文阈值须为正整数');
  return n;
};

const checkLimits = (limits) => {
  if (limits.some((n, i) => i && n <= limits[i - 1])) throw new Error('上下文阈值须从小到大排列');
};

export const timeText = (minute) =>
  [Math.floor(minute / 60), minute % 60].map((n) => String(n).padStart(2, '0')).join(':');

const clockTime = /^([01]\d|2[0-3]):[0-5]\d$/;

// 按计费时间串起所有分界点。新加且尚未填写的时段依次接在已填时段之后。
export function nextPeriodIndices(periods) {
  const ordered = periods.map((_, i) => i).sort((a, b) =>
    (periods[a].time || '24:00').localeCompare(periods[b].time || '24:00')
  );
  const next = [];
  ordered.forEach((index, i) => { next[index] = ordered[(i + 1) % ordered.length]; });
  return next;
}

export const sheetDraft = (sheet) => ({
  timezone: sheet?.periods?.length ? sheet.timezone || '' : '',
  periods: (sheet?.periods || []).map((p) => ({ key: p.name, name: p.name, time: timeText(p.start_minute) })),
  limits: sheet?.context_limits?.length ? sheet.context_limits.join(', ') : null,
  rows: (sheet?.rows || []).map((row) => ({
    ...row,
    period: row.period || '',
    tier: row.tier ?? 0,
    fast: Boolean(row.fast),
    ...Object.fromEntries(priceColumns.map(([key]) => [key, String(row[key] ?? '')]))
  }))
});

export const appendPeriod = (draft, appTimezone) => {
  let n = draft.periods.length + 1;
  while (draft.periods.some((p) => p.name === `时段 ${n}` || p.key === `时段 ${n}`)) n++;
  const name = `时段 ${n}`;
  const first = draft.periods.length === 0;
  return {
    ...draft,
    timezone: first ? appTimezone : draft.timezone,
    periods: [...draft.periods, { key: name, name, time: first ? '00:00' : '' }],
    rows: first ? draft.rows.map((r) => ({ ...r, period: name })) : draft.rows
  };
};

export const parseLimits = (text) => {
  if (text == null || !text.trim()) return [];
  const limits = text.split(/[,，]/).map(threshold);
  checkLimits(limits);
  return limits;
};

export function unconfiguredRateRows(rows, periods, tiers) {
  const key = ({ period, tier, fast }) => JSON.stringify([period, tier, fast]);
  const configured = new Set(rows.map(key));
  const candidates = periods.flatMap((period) =>
    tiers.flatMap((tier) => [false, true].map((fast) => ({ period, tier, fast })))
  ).filter((row) => !configured.has(key(row)));
  const missing = rows.some((row) => row.fast) ? candidates : candidates.filter((row) => !row.fast);
  return { missing, next: missing[0] ?? candidates[0] };
}

export const withoutContextTiers = (draft) => ({
  ...draft,
  limits: null,
  rows: draft.rows.filter((row) => row.tier === 0)
});

export const withoutPeriods = (draft) => {
  const firstKey = draft.periods?.[0]?.key;
  return {
    ...draft,
    timezone: '',
    periods: [],
    rows: (firstKey == null ? draft.rows : draft.rows.filter((row) => row.period === firstKey))
      .map((row) => ({ ...row, period: '' }))
  };
};

export const buildSheet = (draft, endLinks) => {
  const context_limits = parseLimits(draft.limits);
  if (draft.limits != null && !context_limits.length) throw new Error('请填写上下文阈值，或关闭上下文分档');
  const periods = draft.periods
    .map((p) => {
      const name = p.name.trim();
      if (!name || !clockTime.test(p.time)) throw new Error('请填写时段名称与开始时间');
      const [hour, minute] = p.time.split(':').map(Number);
      return { name, start_minute: hour * 60 + minute };
    })
    .sort((a, b) => a.start_minute - b.start_minute);
  const names = new Map(draft.periods.map((p) => [p.key, p.name.trim()]));
  if (new Set(names.values()).size !== periods.length) throw new Error('时段名称必须唯一');
  if (periods.some((p, i) => i > 0 && p.start_minute === periods[i - 1].start_minute)) {
    throw new Error('时段开始时间不能重复');
  }
  if (endLinks && nextPeriodIndices(draft.periods).some((next, i) => next !== endLinks[i])) {
    throw new Error('结束时间跨过了其他时段，请调整分界时间后再发布');
  }
  const rows = draft.rows.map((r, i) => ({
    period: names.get(r.period) ?? r.period,
    tier: Number(r.tier),
    fast: r.fast,
    ...Object.fromEntries(
      priceColumns.map(([key, label]) => [
        key,
        decimal(r[key], `第 ${i + 1} 行${label}价`, key !== 'input' && key !== 'output')
      ])
    )
  }));
  return { timezone: periods.length ? draft.timezone.trim() : '', context_limits, periods, rows };
};

export const ruleDraft = (rule) => ({
  period: rule.period || '',
  fast: String(rule.fast_multiplier ?? ''),
  tiers: (rule.tiers || []).map((t) => ({
    threshold: String(t.threshold ?? ''),
    multiplier: String(t.multiplier ?? '')
  }))
});

export const normalizeRuleDraft = (draft) => {
  if (!draft) return draft;
  const tiers = (draft.tiers || []).map((t) => ({
    threshold: String(t?.threshold ?? '').trim(),
    multiplier: String(t?.multiplier ?? '').trim()
  }));
  while (tiers.length && !tiers.at(-1).threshold && !tiers.at(-1).multiplier) tiers.pop();
  return {
    period: draft.period || '',
    fast: String(draft.fast ?? '').trim(),
    tiers
  };
};

export const buildRule = (draft, limits) => {
  const tiers = draft.tiers.map((t) => ({
    threshold: String(t.threshold ?? '').trim() === '' ? null : threshold(t.threshold),
    multiplier: decimal(t.multiplier, '长档倍率', true)
  }));
  while (tiers.length && Object.values(tiers.at(-1)).every((v) => v === null)) tiers.pop();
  const effective = [...limits];
  tiers.forEach((t, i) => {
    if (i >= limits.length && (t.threshold == null || t.multiplier == null)) {
      throw new Error('该模型没有对应的官方长档，请同时填写阈值和倍率');
    }
    effective[i] = t.threshold ?? limits[i];
  });
  checkLimits(effective);
  return { period: draft.period, fast_multiplier: decimal(draft.fast, 'Fast 倍率', true), tiers };
};
