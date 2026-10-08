// ============================================
// Account Pool Dashboard 状态 Hook
// 2026-03-21
// ============================================

import { useCallback, useDeferredValue, useMemo, useState } from 'react';

const ALL_FILTER_VALUE = 'all';

export const DEFAULT_INVENTORY_SAVED_VIEWS = [
  {
    key: 'primary-issues',
    label: '主组异常',
    description: '仅查看主组里需要优先处理的风险账号'
  },
  {
    key: 'oauth-pending',
    label: '待处理 OAuth',
    description: '仅查看需要保养或补测的 OAuth 账号'
  },
  {
    key: 'quota-risk',
    label: '额度风险',
    description: '仅查看 5h / d7 存在额度风险的账号'
  },
  {
    key: 'cold-free',
    label: '冷备 free 账号',
    description: '仅查看冷备中的 free 账号'
  }
];

export const INVENTORY_BATCH_ACTIONS = [
  {
    key: 'batch-test',
    label: '批量测试',
    tone: 'indigo'
  },
  {
    key: 'batch-refresh-profile',
    label: '批量刷新画像',
    tone: 'sky'
  },
  {
    key: 'batch-toggle',
    label: '批量启用-停用',
    tone: 'emerald',
    variants: [
      { key: 'enable', label: '批量启用', payload: { enabled: true } },
      { key: 'disable', label: '批量停用', payload: { enabled: false } }
    ]
  },
  {
    key: 'batch-move-tier',
    label: '批量移到主组-备组-冷备',
    tone: 'amber',
    variants: [
      { key: 'primary', label: '移到主组', payload: { targetTier: 'primary' } },
      { key: 'backup', label: '移到备组', payload: { targetTier: 'backup' } },
      { key: 'cold', label: '移到冷备', payload: { targetTier: 'cold' } }
    ]
  }
];

export const DEFAULT_INVENTORY_PAGE_SIZES = [20, 50, 100];

// 第一项即默认排序（见 defaultFilterValuesFromConfigs）
const DEFAULT_SORT_OPTIONS = [
  { value: 'recent_success_desc', label: '最近成功' },
  { value: 'risk_desc', label: '风险优先' },
  { value: 'group_asc', label: '组别优先' },
  { value: 'name_asc', label: '账号名' }
];

const DEFAULT_FILTER_OPTIONS = {
  auth: [],
  plan: [],
  group: [],
  status: [],
  risk: [],
  sort: DEFAULT_SORT_OPTIONS
};

const TONE_RANK = {
  rose: 4,
  red: 4,
  danger: 4,
  amber: 3,
  warning: 3,
  yellow: 3,
  sky: 2,
  blue: 2,
  indigo: 2,
  slate: 1,
  gray: 1,
  emerald: 0,
  green: 0,
  success: 0
};

const normalizeText = (value) => String(value ?? '').trim().toLowerCase();

const compactText = (value) => normalizeText(value).replace(/[\s_-]+/g, '');

const asArray = (value) => (Array.isArray(value) ? value : []);

const flattenValues = (value, result = []) => {
  if (Array.isArray(value)) {
    value.forEach((item) => flattenValues(item, result));
    return result;
  }

  if (value && typeof value === 'object') {
    Object.values(value).forEach((item) => flattenValues(item, result));
    return result;
  }

  if (value !== undefined && value !== null && value !== '') {
    result.push(String(value));
  }

  return result;
};

const statusBadgeText = (badge) => {
  if (typeof badge === 'string') return badge;
  if (!badge || typeof badge !== 'object') return '';
  return badge.text || badge.label || badge.value || '';
};

const normalizeOption = (option) => {
  if (typeof option === 'string') {
    return { value: option, label: option };
  }

  if (!option || typeof option !== 'object') {
    return { value: '', label: '' };
  }

  const value = option.value ?? option.key ?? option.label ?? '';
  const label = option.label ?? option.text ?? option.value ?? option.key ?? '';

  return {
    ...option,
    value,
    label
  };
};

const normalizeOptions = (options = [], { includeAll = false } = {}) => {
  const unique = [];
  const seen = new Set();

  asArray(options).forEach((option) => {
    const normalized = normalizeOption(option);
    const signature = `${normalized.value}::${normalized.label}`;

    if (!normalized.value || seen.has(signature)) {
      return;
    }

    unique.push(normalized);
    seen.add(signature);
  });

  if (!includeAll) {
    return unique;
  }

  // 视图模型自带的「全部计划」等同值项去掉：否则菜单里两项「全部」同时被勾上
  return [
    { value: ALL_FILTER_VALUE, label: '全部' },
    ...unique.filter((option) => option.value !== ALL_FILTER_VALUE)
  ];
};

const mergeSavedViews = (savedViews = []) => {
  const merged = new Map(DEFAULT_INVENTORY_SAVED_VIEWS.map((item) => [item.key, item]));

  asArray(savedViews).forEach((item) => {
    if (!item || !item.key) return;
    merged.set(item.key, {
      ...merged.get(item.key),
      ...item
    });
  });

  return Array.from(merged.values());
};

const mergeBatchActions = (actions = []) => {
  const merged = new Map(INVENTORY_BATCH_ACTIONS.map((item) => [item.key, item]));

  asArray(actions).forEach((item) => {
    const normalized = {
      ...item,
      key: item?.key || item?.label || '',
      label: item?.label || item?.key || '',
      variants: asArray(item?.variants)
    };

    if (!normalized.key || !normalized.label) return;

    merged.set(normalized.key, {
      ...merged.get(normalized.key),
      ...normalized
    });
  });

  return Array.from(merged.values());
};

const parseTimestamp = (...values) => {
  for (const value of values) {
    if (typeof value === 'number' && Number.isFinite(value)) {
      return value;
    }

    if (typeof value !== 'string' || !value.trim()) {
      continue;
    }

    const parsed = Date.parse(value);
    if (!Number.isNaN(parsed)) {
      return parsed;
    }
  }

  return 0;
};

const buildSearchCorpus = (row) => compactText(flattenValues([
  row.name,
  row.authLabel,
  row.planLabel,
  row.groupLabel,
  row.stateLabel,
  row.quota5hText,
  row.quota7dText,
  row.lastSuccessText,
  row.refreshedAtText,
  asArray(row.statusBadges).map(statusBadgeText),
  row.detail
]).join(' '));

const groupRank = (row) => {
  const text = compactText([row.groupLabel, row.detail?.groupKey, row.detail?.tierLabel].filter(Boolean).join(' '));

  if (text.includes('主组') || text.includes('primary') || text.includes('main')) return 0;
  if (text.includes('备组') || text.includes('backup') || text.includes('secondary')) return 1;
  if (text.includes('冷备') || text.includes('cold')) return 2;
  return 9;
};

const getRiskRank = (row) => {
  const tone = normalizeText(row.stateTone);
  const directRank = TONE_RANK[tone];
  if (directRank !== undefined) {
    return directRank;
  }

  const riskCorpus = [
    row.stateLabel,
    row.quota5hText,
    row.quota7dText,
    row.detail?.quotaStatusLabel,
    row.detail?.healthLabel,
    row.detail?.riskLabel,
    ...asArray(row.statusBadges).map(statusBadgeText)
  ].join(' ');

  const text = compactText(riskCorpus);

  if (/(auth|失效|异常|故障|失败|紧急|p1|风险|耗尽|不足)/.test(text)) return 4;
  if (/(warning|告警|接近|低额|维护|刷新慢|未成功|p2)/.test(text)) return 3;
  if (/(观察|待处理|提示|p3)/.test(text)) return 2;
  return 0;
};

const fieldTokens = (row, field) => {
  switch (field) {
    case 'auth':
      return [
        row.authLabel,
        row.detail?.authLabel,
        row.detail?.providerType,
        row.detail?.credentialType
      ];
    case 'plan':
      return [
        row.planLabel,
        row.detail?.planLabel,
        row.detail?.planType
      ];
    case 'group':
      return [
        row.groupLabel,
        row.detail?.groupLabel,
        row.detail?.groupKey,
        row.detail?.tierLabel
      ];
    case 'status':
      return [
        row.stateLabel,
        row.detail?.stateLabel,
        row.detail?.quotaStatusLabel,
        row.detail?.healthLabel,
        ...asArray(row.statusBadges).map(statusBadgeText)
      ];
    case 'risk':
      return [
        row.stateTone,
        row.detail?.riskLabel,
        row.detail?.quotaStatusLabel,
        row.detail?.healthLabel,
        row.stateLabel,
        row.quota5hText,
        row.quota7dText,
        ...asArray(row.statusBadges).map(statusBadgeText)
      ];
    default:
      return [];
  }
};

const matchAliases = (value, tokens) => {
  const normalizedValue = compactText(value);
  const normalizedTokens = tokens.map((item) => compactText(item)).filter(Boolean);

  if (!normalizedValue || normalizedValue === ALL_FILTER_VALUE) {
    return true;
  }

  if (fieldAliasMatchers[normalizedValue]) {
    return fieldAliasMatchers[normalizedValue](normalizedTokens);
  }

  return normalizedTokens.some((item) => item.includes(normalizedValue) || normalizedValue.includes(item));
};

const fieldAliasMatchers = {
  oauth: (tokens) => tokens.some((item) => /(oauth|refresh|session|idtoken|chatgpt)/.test(item)),
  apikey: (tokens) => tokens.some((item) => /(apikey|api_key|key)/.test(item)),
  api_key: (tokens) => tokens.some((item) => /(apikey|api_key|key)/.test(item)),
  primary: (tokens) => tokens.some((item) => /(主组|primary|main)/.test(item)),
  main: (tokens) => tokens.some((item) => /(主组|primary|main)/.test(item)),
  backup: (tokens) => tokens.some((item) => /(备组|backup|secondary)/.test(item)),
  secondary: (tokens) => tokens.some((item) => /(备组|backup|secondary)/.test(item)),
  cold: (tokens) => tokens.some((item) => /(冷备|cold)/.test(item)),
  risk: (tokens) => tokens.some((item) => /(风险|异常|warning|danger|紧急|auth|失效|耗尽)/.test(item)),
  healthy: (tokens) => !tokens.some((item) => /(风险|异常|warning|danger|紧急|auth|失效|耗尽)/.test(item))
};

const matchesSavedView = (row, savedViewKey) => {
  switch (savedViewKey) {
    case 'primary-issues':
      return groupRank(row) === 0 && getRiskRank(row) >= 3;
    case 'oauth-pending':
      return matchAliases('oauth', fieldTokens(row, 'auth')) && getRiskRank(row) >= 2;
    case 'quota-risk':
      return /(\b[0-2]?\d%|将尽|耗尽|额度风险|低额)/.test(normalizeText([row.quota5hText, row.quota7dText, row.detail?.quotaStatusLabel].join(' ')))
        || getRiskRank(row) >= 3;
    case 'cold-free':
      return groupRank(row) === 2 && matchAliases('free', fieldTokens(row, 'plan'));
    default:
      return true;
  }
};

const compareByName = (left, right) => String(left.name || '').localeCompare(String(right.name || ''), 'zh-Hans-CN');

const compareByGroupThenName = (left, right) => groupRank(left) - groupRank(right) || compareByName(left, right);

const sortRows = (rows, sortValue) => {
  const value = normalizeText(sortValue);
  const sorted = [...rows];

  sorted.sort((left, right) => {
    if (value === 'name_asc' || value === 'name' || value === '账号名') {
      return compareByName(left, right);
    }

    // 时间相同（含从未成功的账号）时按组别、账号名稳定排序
    if (value === 'recent_success_desc' || value === 'success' || value === '最近成功') {
      return parseTimestamp(right.detail?.lastSuccessAt, right.lastSuccessText)
        - parseTimestamp(left.detail?.lastSuccessAt, left.lastSuccessText)
        || compareByGroupThenName(left, right);
    }

    if (value === 'refreshed' || value === '最近刷新') {
      return parseTimestamp(right.detail?.refreshedAt, right.detail?.quotaRefreshedAt, right.refreshedAtText)
        - parseTimestamp(left.detail?.refreshedAt, left.detail?.quotaRefreshedAt, left.refreshedAtText);
    }

    if (value === 'group_asc' || value === 'group' || value === '组别') {
      return compareByGroupThenName(left, right);
    }

    return getRiskRank(right) - getRiskRank(left) || compareByGroupThenName(left, right);
  });

  return sorted;
};

const buildFilterConfigs = (filters = {}) => ([
  {
    key: 'auth',
    label: '授权方式',
    options: normalizeOptions(filters.authOptions || DEFAULT_FILTER_OPTIONS.auth, { includeAll: true })
  },
  {
    key: 'plan',
    label: '计划类型',
    options: normalizeOptions(filters.planOptions || DEFAULT_FILTER_OPTIONS.plan, { includeAll: true })
  },
  {
    key: 'group',
    label: '组别',
    options: normalizeOptions(filters.groupOptions || DEFAULT_FILTER_OPTIONS.group, { includeAll: true })
  },
  {
    key: 'status',
    label: '运行状态',
    options: normalizeOptions(filters.statusOptions || DEFAULT_FILTER_OPTIONS.status, { includeAll: true })
  },
  {
    key: 'risk',
    label: '风险状态',
    options: normalizeOptions(filters.riskOptions || DEFAULT_FILTER_OPTIONS.risk, { includeAll: true })
  },
  {
    key: 'sort',
    label: '排序方式',
    options: normalizeOptions(filters.sortOptions || DEFAULT_FILTER_OPTIONS.sort, { includeAll: false })
  }
]);

const defaultFilterValuesFromConfigs = (filterConfigs) => {
  const initial = {};

  filterConfigs.forEach((config) => {
    initial[config.key] = config.key === 'sort'
      ? config.options[0]?.value || 'recent_success_desc'
      : ALL_FILTER_VALUE;
  });

  return initial;
};

export const buildBatchActionNotice = (actionLabel, count, { phase = 'intent' } = {}) => {
  const safeCount = Number.isFinite(Number(count)) ? Number(count) : 0;

  if (phase === 'success') {
    return `已对 ${safeCount} 个账号完成${actionLabel}`;
  }

  if (phase === 'failure') {
    return `${actionLabel}失败，涉及 ${safeCount} 个账号`;
  }

  return `准备对 ${safeCount} 个账号执行${actionLabel}`;
};

export const paginateInventoryRows = (rows = [], { currentPage = 1, pageSize = DEFAULT_INVENTORY_PAGE_SIZES[0] } = {}) => {
  const normalizedRows = asArray(rows);
  const normalizedPageSize = DEFAULT_INVENTORY_PAGE_SIZES.includes(Number(pageSize))
    ? Number(pageSize)
    : DEFAULT_INVENTORY_PAGE_SIZES[0];
  const totalCount = normalizedRows.length;
  const totalPages = Math.max(1, Math.ceil(totalCount / normalizedPageSize) || 1);
  const safeCurrentPage = Math.min(Math.max(Number(currentPage) || 1, 1), totalPages);
  const startIndex = (safeCurrentPage - 1) * normalizedPageSize;

  return {
    rows: normalizedRows.slice(startIndex, startIndex + normalizedPageSize),
    currentPage: safeCurrentPage,
    pageSize: normalizedPageSize,
    totalPages,
    totalCount
  };
};

const useAccountPoolDashboardState = ({ inventory = {}, onBatchAction, externalViewRequest = null } = {}) => {
  const rows = useMemo(() => asArray(inventory.rows), [inventory.rows]);
  const filters = useMemo(() => inventory.filters || {}, [inventory.filters]);
  const filterConfigs = useMemo(() => buildFilterConfigs(filters), [filters]);
  const defaultFilterValues = useMemo(
    () => defaultFilterValuesFromConfigs(filterConfigs),
    [filterConfigs]
  );
  const [searchTerm, setSearchTerm] = useState(() => filters.searchTerm || '');
  const deferredSearchTerm = useDeferredValue(searchTerm);
  const [filterValues, setFilterValues] = useState(() => defaultFilterValues);
  const [activeSavedViewKey, setActiveSavedViewKey] = useState('');
  const [selectedRowIds, setSelectedRowIds] = useState([]);
  const [activeRowId, setActiveRowId] = useState(null);
  const [batchFeedback, setBatchFeedback] = useState(null);
  const [currentPage, setCurrentPage] = useState(1);
  const [pageSize, setPageSize] = useState(DEFAULT_INVENTORY_PAGE_SIZES[0]);
  // 已处理的外部 focus-group 请求 ID（渲染期一次性应用的去重标记）
  const [handledExternalViewRequestId, setHandledExternalViewRequestId] = useState(null);

  const savedViews = useMemo(
    () => mergeSavedViews(inventory.savedViews || filters.savedViews),
    [filters.savedViews, inventory.savedViews]
  );
  const batchActions = useMemo(
    () => mergeBatchActions(inventory.batchActions),
    [inventory.batchActions]
  );

  const filteredRows = useMemo(() => {
    const query = compactText(deferredSearchTerm);

    const filtered = rows.filter((row) => {
      if (query && !buildSearchCorpus(row).includes(query)) {
        return false;
      }

      if (activeSavedViewKey && !matchesSavedView(row, activeSavedViewKey)) {
        return false;
      }

      return filterConfigs.every((config) => {
        if (config.key === 'sort') {
          return true;
        }

        return matchAliases(filterValues[config.key], fieldTokens(row, config.key));
      });
    });

    return sortRows(filtered, filterValues.sort);
  }, [activeSavedViewKey, deferredSearchTerm, filterConfigs, filterValues, rows]);

  const paginationState = useMemo(
    () => paginateInventoryRows(filteredRows, { currentPage, pageSize }),
    [currentPage, filteredRows, pageSize]
  );
  const visibleRows = paginationState.rows;

  // 渲染期状态校准（React 官方 adjust-state-during-render 模式，替代 effect 内同步 setState）：
  // 过滤/删行导致当前页越界时，立即钳制页码并清空选择，本次渲染即被丢弃重渲。
  if (paginationState.currentPage !== currentPage) {
    setCurrentPage(paginationState.currentPage);
    setSelectedRowIds([]);
    setBatchFeedback(null);
  }

  // 外部 focus-group 请求同样在渲染期处理：以 requestId 为一次性标记，
  // 同一请求只应用一次（旧 effect 会在 searchTerm/defaultFilterValues 变化时误重放）。
  if (
    externalViewRequest?.requestId
    && externalViewRequest?.type === 'focus-group'
    && handledExternalViewRequestId !== externalViewRequest.requestId
  ) {
    setHandledExternalViewRequestId(externalViewRequest.requestId);
    setSearchTerm(filters.searchTerm || '');
    setFilterValues({
      ...defaultFilterValues,
      group: externalViewRequest.groupKey || ALL_FILTER_VALUE
    });
    setActiveSavedViewKey('');
    setCurrentPage(1);
    setSelectedRowIds([]);
    setBatchFeedback(null);
  }

  const effectiveSelectedRowIds = useMemo(() => {
    const availableIds = new Set(rows.map((item) => String(item.id)));
    return selectedRowIds.filter((item) => availableIds.has(String(item)));
  }, [rows, selectedRowIds]);

  const selectedRows = useMemo(() => {
    const selectedIdSet = new Set(effectiveSelectedRowIds.map((item) => String(item)));
    return visibleRows.filter((row) => selectedIdSet.has(String(row.id)));
  }, [effectiveSelectedRowIds, visibleRows]);

  const activeRow = useMemo(
    () => rows.find((row) => String(row.id) === String(activeRowId)) || null,
    [activeRowId, rows]
  );

  const allVisibleSelected = visibleRows.length > 0
    && visibleRows.every((row) => effectiveSelectedRowIds.some((item) => String(item) === String(row.id)));

  const updateSearchTerm = useCallback((value) => {
    setSearchTerm(value);
    setCurrentPage(1);
    setSelectedRowIds([]);
    setBatchFeedback(null);
  }, []);

  const setFilterValue = useCallback((key, value) => {
    setFilterValues((current) => ({
      ...current,
      [key]: value || ALL_FILTER_VALUE
    }));
    setCurrentPage(1);
    setSelectedRowIds([]);
    setBatchFeedback(null);
  }, []);

  const resetFilters = useCallback(() => {
    setSearchTerm(filters.searchTerm || '');
    setFilterValues(defaultFilterValues);
    setActiveSavedViewKey('');
    setCurrentPage(1);
    setSelectedRowIds([]);
    setBatchFeedback(null);
  }, [defaultFilterValues, filters.searchTerm]);

  const toggleSavedView = useCallback((savedViewKey) => {
    setActiveSavedViewKey((current) => (current === savedViewKey ? '' : savedViewKey));
    setCurrentPage(1);
    setSelectedRowIds([]);
    setBatchFeedback(null);
  }, []);

  const toggleRowSelection = useCallback((rowId) => {
    setSelectedRowIds((current) => {
      const normalizedId = String(rowId);
      const exists = current.some((item) => String(item) === normalizedId);

      if (exists) {
        return current.filter((item) => String(item) !== normalizedId);
      }

      return [...current, rowId];
    });
  }, []);

  const toggleAllVisibleRows = useCallback(() => {
    setSelectedRowIds((current) => {
      const visibleIds = visibleRows.map((row) => String(row.id));
      const currentIdSet = new Set(current.map((item) => String(item)));
      const next = new Set(currentIdSet);
      const shouldSelect = visibleIds.some((id) => !currentIdSet.has(id));

      visibleIds.forEach((id) => {
        if (shouldSelect) {
          next.add(id);
        } else {
          next.delete(id);
        }
      });

      return Array.from(next);
    });
  }, [visibleRows]);

  const clearSelection = useCallback(() => {
    setSelectedRowIds([]);
  }, []);

  const openDetails = useCallback((row) => {
    setActiveRowId(row?.id ?? null);
  }, []);

  const closeDetails = useCallback(() => {
    setActiveRowId(null);
  }, []);

  const clearBatchFeedback = useCallback(() => {
    setBatchFeedback(null);
  }, []);

  const goToPage = useCallback((page) => {
    setCurrentPage(page);
    setSelectedRowIds([]);
    setBatchFeedback(null);
  }, []);

  const goToPreviousPage = useCallback(() => {
    setCurrentPage((current) => Math.max(1, current - 1));
    setSelectedRowIds([]);
    setBatchFeedback(null);
  }, []);

  const goToNextPage = useCallback(() => {
    setCurrentPage((current) => Math.min(paginationState.totalPages, current + 1));
    setSelectedRowIds([]);
    setBatchFeedback(null);
  }, [paginationState.totalPages]);

  const updatePageSize = useCallback((value) => {
    const nextPageSize = DEFAULT_INVENTORY_PAGE_SIZES.includes(Number(value))
      ? Number(value)
      : DEFAULT_INVENTORY_PAGE_SIZES[0];

    setPageSize(nextPageSize);
    setCurrentPage(1);
    setSelectedRowIds([]);
    setBatchFeedback(null);
  }, []);

  const runBatchAction = useCallback(async (action, variant) => {
    if (!selectedRows.length) {
      return null;
    }

    const actionLabel = variant?.label || action?.label || '批量操作';
    const payload = {
      actionKey: action?.key || '',
      actionLabel,
      variantKey: variant?.key || '',
      ids: selectedRows.map((row) => row.id),
      rows: selectedRows,
      count: selectedRows.length,
      meta: variant?.payload || {}
    };

    const intentMessage = buildBatchActionNotice(actionLabel, payload.count, { phase: 'intent' });
    setBatchFeedback({
      tone: 'info',
      phase: 'intent',
      message: intentMessage
    });

    const result = onBatchAction ? await onBatchAction(payload) : { success: true };

    if (result?.success === false) {
      setBatchFeedback({
        tone: 'danger',
        phase: 'failure',
        message: result.message || buildBatchActionNotice(actionLabel, payload.count, { phase: 'failure' })
      });
      return result;
    }

    const successMessage = result?.message || buildBatchActionNotice(actionLabel, payload.count, { phase: 'success' });
    setBatchFeedback({
      tone: 'success',
      phase: 'success',
      message: successMessage
    });

    if (!result?.keepSelection) {
      setSelectedRowIds([]);
    }

    return {
      success: true,
      ...result,
      message: successMessage
    };
  }, [onBatchAction, selectedRows]);

  return {
    rows,
    visibleRows,
    filterConfigs,
    toolbar: {
      searchTerm,
      setSearchTerm: updateSearchTerm,
      savedViews,
      activeSavedViewKey,
      toggleSavedView,
      filterValues,
      setFilterValue,
      resetFilters,
      resultCount: filteredRows.length
    },
    selection: {
      selectedRowIds: effectiveSelectedRowIds,
      selectedRows,
      selectedCount: selectedRows.length,
      allVisibleSelected,
      toggleRowSelection,
      toggleAllVisibleRows,
      clearSelection
    },
    batch: {
      actions: batchActions,
      feedback: batchFeedback,
      runBatchAction,
      clearBatchFeedback
    },
    drawer: {
      activeRow,
      open: Boolean(activeRow),
      openDetails,
      closeDetails
    },
    pagination: {
      currentPage: paginationState.currentPage,
      totalPages: paginationState.totalPages,
      totalCount: paginationState.totalCount,
      pageSize: paginationState.pageSize,
      pageSizeOptions: DEFAULT_INVENTORY_PAGE_SIZES.map((size) => ({
        value: String(size),
        label: `${size} / 页`
      })),
      hasPreviousPage: paginationState.currentPage > 1,
      hasNextPage: paginationState.currentPage < paginationState.totalPages,
      goToPage,
      goToPreviousPage,
      goToNextPage,
      setPageSize: updatePageSize
    }
  };
};

export default useAccountPoolDashboardState;
