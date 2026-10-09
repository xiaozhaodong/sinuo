// ============================================
// Requests 页面 - 请求追踪（重构版）
// 2025-12-06 10:40:38 v4.0: 简化端点切换（移除 Token 级联）
// 2026-10-07 桌面化：贴边表格 + 底部可拖动详情面板（替代双击弹窗）+ 状态栏（统计与分页）；
//            单击选中、↑↓ 切换、⌘C 复制请求 ID、右键菜单
// ============================================

import { useState, useEffect, useCallback, useMemo, useRef } from 'react';
import { ErrorMessage } from '@components/ui';
import { PageToolbar } from '@components/layout/AppToolbar.jsx';
import { StatusBar, StatusSpacer } from '@components/desktop/StatusBar.jsx';
import PaneSplitter from '@components/desktop/PaneSplitter.jsx';
import StatusPager from '@components/desktop/StatusPager.jsx';
import { readStorage, writeStorage } from '@components/desktop/storage.js';
import useResizablePane from '@components/desktop/useResizablePane.js';
import useContextMenu from '@components/desktop/useContextMenu.jsx';
import useListKeyboard from '@components/desktop/useListKeyboard.js';
import { formatShortcut, isMacPlatform } from '@utils/platform.js';
import { NoticeToast } from '@pages/account-pool/components';
import { useNotice } from '@pages/account-pool/hooks';
import {
  compareAccountsByManualPriority,
  isValidAccountId,
  resolveAccountId,
} from '@pages/account-pool/utils.js';
import {
  fetchRequests,
  fetchRequestUpstreamOptions,
  fetchModels,
  fetchUsageStats,
  fetchEndpoints,
  fetchClaudeRoutingState,
  fetchModelRoutingSettings,
  fetchModelRouteBindings,
  updateModelRouteBinding,
  fetchCodexModelRouteBindings,
  updateCodexModelRouteBinding,
  setClaudeRoutingOverride,
  clearClaudeRoutingOverride,
  clearNegativeHitCache,
  enableAutomaticAccountSelection,
  pinUpstreamAccountSelection,
  fetchUpstreamAccounts,
  fetchLatestAccountScheduleSnapshot
} from '@utils/api.js';
import { subscribeToEvent, isWailsEnvironment } from '@utils/wailsApi.js';
import { buildQueryParamsFromFilters, useFilters } from './hooks/useFilters.js';
import { useColumnConfig } from './hooks/useColumnConfig.js';
import { useTimeRange } from './hooks/useTimeRange.js';
import { useAutoRefresh } from './hooks/useAutoRefresh.js';
import { StatsOverview, RequestsTable, Toolbar, RequestDetailPane } from './components';
import FilterBar from './components/FilterBar.jsx';
import RequestRefreshStatus from './components/AutoRefreshControl.jsx';
import { copyTextToClipboard } from './components/clipboard.js';
import { PAGINATION_CONFIG } from './utils/constants.js';
import { buildColumnMenuItems } from './utils/columnMenu.js';
import { isRuntimeActiveSelection, isSamePinnedAccount } from './utils/accountSwitcherState.js';
import { bindingDisplayName, isWebToolsBinding, patchBindingPayload } from '../endpoints/utils/modelRouting.js';
import { buildTimeRangeSelectionState } from './utils/timeRangeSelection.js';
import { buildChipPatch, buildClearChipsPatch, countActiveChips, resolveStoredPageSize } from './utils/filterBar.js';
import { mergeRequestRows } from './utils/mergeRequestRows.js';
import { buildRowEnterAnimations, collectRequestIds, NO_ROW_ANIMATIONS } from './utils/rowEnterAnimation.js';
import { buildUpstreamFilterOptions, resolveUpstreamFilterOption } from './utils/requestSource.js';
import { prefersReducedMotion } from '@utils/motion.js';
import { consumePendingRequestsFilters } from '@utils/overviewHandoff.js';

// 每页条数与筛选条显隐记在本机
const PAGE_SIZE_STORAGE_KEY = 'requests.pageSize';
const FILTER_BAR_STORAGE_KEY = 'requests.filterBar.visible';

// 详情面板：默认 280px；拖动时面板至少 160px、上方列表至少留 120px
const DETAIL_PANE = { storageKey: 'requests.detailPane', defaultHeight: 280, minHeight: 160, minRemaining: 120 };

// ⌘C（Windows / Linux 为 Ctrl+C）
const isCopyShortcut = (event) => {
  const primary = isMacPlatform() ? event.metaKey : event.ctrlKey;
  return primary && !event.altKey && !event.shiftKey && event.key.toLowerCase() === 'c';
};

// ============================================
// Requests 页面
// ============================================

const RequestsPage = ({ onOpenClaudeModelRouting, onOpenCodexModelRouting, onNavigateToCodexAccess }) => {
  // ==================== 状态管理 ====================
  const { notice, showNotice, closeNotice } = useNotice();

  // 数据状态
  const [requests, setRequests] = useState([]);
  const [stats, setStats] = useState(null);
  const [models, setModels] = useState([]);
  // 上游筛选候选：范围内实际出现过的上游（后端查询）；null 表示不可用，只按当前页生成
  const [upstreamCatalog, setUpstreamCatalog] = useState(null);
  const [endpoints, setEndpoints] = useState([]);
  const [claudeRoutingState, setClaudeRoutingState] = useState({ mode: 'auto', endpointName: '', fallbackEnabled: true });
  const [modelRoutingSettings, setModelRoutingSettings] = useState({ claude_enabled: false });
  const [modelRouteBindings, setModelRouteBindings] = useState([]);
  const [modelRoutePatching, setModelRoutePatching] = useState(false);
  const [codexModelRouteBindings, setCodexModelRouteBindings] = useState([]);
  const [codexModelRoutePatching, setCodexModelRoutePatching] = useState(false);
  const [accounts, setAccounts] = useState([]);
  const [latestScheduleSnapshot, setLatestScheduleSnapshot] = useState({ hasSnapshot: false, has_snapshot: false, candidates: [] });
  const [routeSwitching, setRouteSwitching] = useState(false);
  const [accountSwitching, setAccountSwitching] = useState(false);
  const [loading, setLoading] = useState(true);
  // refreshing 与 loading 分离：首次加载才允许整表换成骨架，
  // 之后的刷新保持表格在位，只在顶部走一条进度条，避免整表闪一下。
  const [refreshing, setRefreshing] = useState(false);
  // 本批次新增行的入场编排（stagger 延迟 + 是否位移）。
  // 只有实时事件驱动的刷新才非空：首次加载、翻页、改筛选、手动刷新都会整页换数据，
  // 那时播动画会变成满屏闪。
  const [rowEnterAnimations, setRowEnterAnimations] = useState(NO_ROW_ANIMATIONS);
  const [error, setError] = useState(null);
  const loadRequestIdRef = useRef(0);
  const loadDataIdRef = useRef(0);
  const hasLoadedOnceRef = useRef(false);
  // 上一批的 requestId 集合，用于 diff 出「哪些行是新到达的、各自排第几」。
  // 只在 refreshRequestData 这个 callback 里读写，不碰 render 期。
  const prevRequestIdsRef = useRef(null);

  // 面板状态：筛选条默认显示
  const [filterBarVisible, setFilterBarVisibleState] = useState(() => readStorage(FILTER_BAR_STORAGE_KEY) !== 'false');

  // 选中行与底部详情面板。selection 存行对象：行被实时刷新挤出当前页后，面板仍按它显示（详情接口按 ID 拉权威数据）
  const [selection, setSelection] = useState(null);
  const listRef = useRef(null);
  const detailPane = useResizablePane(DETAIL_PANE);
  const { contextMenu, openContextMenu, openMenuAt } = useContextMenu();

  // 筛选器 Hook
  // 概览页模型表跳转过来时会带一次性筛选条件（模型 / 请求族 / 滚动时间区间），
  // 只在本页挂载时消费一次。概览的滚动区间不等于「今天 / 7天 / 30天」预设，
  // 所以带日期时不让任何预设按钮保持高亮，避免列表窗口和按钮说明打架。
  const handoffRef = useRef();
  if (handoffRef.current === undefined) {
    const pending = consumePendingRequestsFilters();
    handoffRef.current = {
      filters: pending || undefined,
      timeRangeKey: pending && (pending.startDate || pending.endDate) ? 'custom' : 'today'
    };
  }
  const {
    filters,
    updateFilters,
    buildQueryParams
  } = useFilters(handoffRef.current.filters);
  const [appliedQueryParams, setAppliedQueryParams] = useState(() => buildQueryParams());

  // 列配置 Hook
  const {
    visibleColumns,
    toggleColumn,
    resetColumns,
    allColumns: columnConfigs
  } = useColumnConfig();
  const columnMenuItems = buildColumnMenuItems(columnConfigs, visibleColumns, toggleColumn, resetColumns);

  // 时间范围 Hook
  const { activeRange, selectRange, markCustomRange } = useTimeRange((timeRange, filterPatch) => {
    const nextState = buildTimeRangeSelectionState(filters, timeRange, filterPatch);
    updateFilters(nextState.filters);
    setAppliedQueryParams(nextState.appliedQueryParams);
    setPagination(prev => ({ ...prev, page: 1 }));
    setSelection(null);
  }, handoffRef.current.timeRangeKey);

  // 分页状态
  const [pagination, setPagination] = useState({
    page: 1,
    pageSize: resolveStoredPageSize(readStorage(PAGE_SIZE_STORAGE_KEY)),
    total: 0,
    totalPages: 1
  });

  const sortedAccounts = useMemo(() => {
    return [...accounts].sort(compareAccountsByManualPriority);
  }, [accounts]);

  const upstreamOptions = useMemo(() => buildUpstreamFilterOptions({
    catalog: upstreamCatalog,
    requests,
    family: filters.requestFamily,
    selected: filters.upstreamName
  }), [filters.requestFamily, filters.upstreamName, requests, upstreamCatalog]);

  const recentSelectedAccountId = useMemo(() => {
    const hasSnapshot = latestScheduleSnapshot?.hasSnapshot === true || latestScheduleSnapshot?.has_snapshot === true;
    if (!hasSnapshot) {
      return null;
    }

    const finalOutcome = String(
      latestScheduleSnapshot?.finalOutcome
      ?? latestScheduleSnapshot?.final_outcome
      ?? ''
    ).trim().toLowerCase();
    if (finalOutcome === 'transient_failure' || finalOutcome === 'auth_failed' || finalOutcome === 'no_schedulable_accounts') {
      return null;
    }

    return latestScheduleSnapshot?.selectedAccountId
      ?? latestScheduleSnapshot?.selected_account_id
      ?? null;
  }, [latestScheduleSnapshot]);

  const activeAccount = useMemo(() => {
    return sortedAccounts.find(isRuntimeActiveSelection) || null;
  }, [sortedAccounts]);

  // ==================== 数据加载 ====================

  // live=true 表示本次刷新由 request:update 事件触发，此时新增的行才播入场动画。
  const refreshRequestData = useCallback(async ({ live = false } = {}) => {
    const requestId = ++loadRequestIdRef.current;
    const requestsQueryParams = {
      ...appliedQueryParams,
      source_view: appliedQueryParams.source_view || 'all'
    };
    const statsParams = {
      ...requestsQueryParams,
      period: '30d',
      // 顶部汇总必须与列表同口径：显式下传（缺省才是后端按指标混合的概览口径）。
      include_compaction: requestsQueryParams.include_compaction !== false
    };

    const [requestsData, statsData] = await Promise.all([
      fetchRequests({
        ...requestsQueryParams,
        page: pagination.page,
        pageSize: pagination.pageSize
      }),
      fetchUsageStats(statsParams)
    ]);

    if (loadRequestIdRef.current !== requestId) {
      return;
    }

    const nextRequests = requestsData.requests;
    // 新旧两批在这里都在手上，diff 一次算出谁是新到达的、各自延迟多少。
    // 放在 callback 里而不是 RequestsTable 的 render 期，是因为 render 期读 ref
    // 在 StrictMode 下会被跑两次，第二次 diff 必然落空。
    //
    // 入场动画只在第 1 页成立：停在第 2 页时，本页「新出现的 requestId」多半是
    // 被上一页挤下来的旧请求 —— 每来一条新请求就误报一次，动效会持续说谎。
    // reduced-motion 也在这里拦掉，而不是只靠 CSS 的 animation:none：
    // 后者会让 animationend 永不触发，行上的 class 与 --enter-delay 内联变量
    // 就再也摘不掉了。
    const canAnimateEnter = live && pagination.page === 1 && !prefersReducedMotion();
    setRowEnterAnimations(buildRowEnterAnimations(nextRequests, prevRequestIdsRef.current, canAnimateEnter));
    prevRequestIdsRef.current = collectRequestIds(nextRequests);
    // 未变化的行复用旧引用，让 RequestRow 的 memo 生效。
    setRequests(prev => mergeRequestRows(prev, nextRequests));
    setPagination(prev => ({
      ...prev,
      total: requestsData.total,
      totalPages: requestsData.totalPages
    }));
    setStats(statsData?.data || statsData);
  }, [appliedQueryParams, pagination.page, pagination.pageSize]);

  const loadData = useCallback(async (silent = false) => {
    const loadId = ++loadDataIdRef.current;
    const isFirstLoad = !hasLoadedOnceRef.current;
    try {
      // 只有首次加载才把整表换成骨架；后续刷新走顶部进度条，表格保持在位。
      if (isFirstLoad) {
        setLoading(true);
      } else if (!silent) {
        setRefreshing(true);
      }
      setError(null);

      // 请求明细与统计单独封装，实时事件只刷新这两组动态数据。
      const [, upstreamCatalogData, modelsData, endpointsData, routeStateData, modelRoutingData, modelBindingsData, codexModelBindingsData, accountsData, latestSnapshotData] = await Promise.all([
        refreshRequestData(),
        fetchRequestUpstreamOptions(appliedQueryParams).catch((err) => {
          console.error('❌ 加载上游筛选候选失败:', err);
          return null;
        }),
        fetchModels(),
        fetchEndpoints(),
        fetchClaudeRoutingState().catch((err) => {
          console.error('❌ 加载 Claude 路由状态失败:', err);
          return { mode: 'auto', endpointName: '', fallbackEnabled: true };
        }),
        fetchModelRoutingSettings().catch((err) => {
          console.error('❌ 加载模型路由状态失败:', err);
          return { claude_enabled: false };
        }),
        fetchModelRouteBindings().catch((err) => {
          console.error('❌ 加载模型路由映射失败:', err);
          return [];
        }),
        fetchCodexModelRouteBindings().catch((err) => {
          console.error('❌ 加载 Codex 模型路由绑定失败:', err);
          return [];
        }),
        fetchUpstreamAccounts().catch((err) => {
          console.error('❌ 加载账号池账号失败:', err);
          showNotice('error', err?.message || '加载账号池账号失败，账号切换器暂不可用');
          return [];
        }),
        fetchLatestAccountScheduleSnapshot().catch((err) => {
          console.error('❌ 加载最近一次账号调度结果失败:', err);
          return {
            unsupported: true,
            hasSnapshot: false,
            has_snapshot: false,
            candidates: []
          };
        })
      ]);

      if (loadDataIdRef.current !== loadId) {
        return;
      }

      setUpstreamCatalog(Array.isArray(upstreamCatalogData) ? upstreamCatalogData : null);
      setModels(Array.isArray(modelsData) ? modelsData : []);

      const endpointsList = endpointsData.endpoints || endpointsData || [];
      setEndpoints(Array.isArray(endpointsList) ? endpointsList : []);

      setClaudeRoutingState(routeStateData && typeof routeStateData === 'object'
        ? routeStateData
        : { mode: 'auto', endpointName: '', fallbackEnabled: true });
      setModelRoutingSettings(modelRoutingData && typeof modelRoutingData === 'object' ? modelRoutingData : { claude_enabled: false });
      setModelRouteBindings(Array.isArray(modelBindingsData) ? modelBindingsData : []);
      setCodexModelRouteBindings(Array.isArray(codexModelBindingsData) ? codexModelBindingsData : []);

      setAccounts(Array.isArray(accountsData) ? accountsData : []);
      setLatestScheduleSnapshot(latestSnapshotData && typeof latestSnapshotData === 'object'
        ? latestSnapshotData
        : { hasSnapshot: false, has_snapshot: false, candidates: [] });

    } catch (err) {
      if (loadDataIdRef.current !== loadId) {
        return;
      }
      setError(err.message);
    } finally {
      if (loadDataIdRef.current === loadId) {
        hasLoadedOnceRef.current = true;
        setLoading(false);
        setRefreshing(false);
      }
    }
  }, [appliedQueryParams, refreshRequestData, showNotice]);

  // 请求变化由 Wails 事件触发，只刷新明细与统计；事件不可用时低频降级。
  // 包一层标记 live，使事件驱动的新行才播入场动画。
  const refreshLiveRequestData = useCallback(() => refreshRequestData({ live: true }), [refreshRequestData]);
  const autoRefresh = useAutoRefresh(refreshLiveRequestData);

  // 手动刷新：显式不传参，避免 onClick 的 event 对象被当成 silent 实参。
  const handleManualRefresh = useCallback(() => loadData(), [loadData]);

  // ==================== 事件处理 ====================

  const setFilterBarVisible = useCallback((visible) => {
    setFilterBarVisibleState(visible);
    writeStorage(FILTER_BAR_STORAGE_KEY, visible);
  }, []);

  // 换筛选、翻页都是换一批数据，选中项随之清掉（与切换文件夹时清空选择同理）

  // 筛选条与右键「只看此模型 / 上游」共用：在当前筛选上叠加并立即生效
  const applyQuickFilter = useCallback((patch) => {
    updateFilters(patch);
    setAppliedQueryParams(buildQueryParamsFromFilters({ ...filters, ...patch }));
    setPagination(prev => ({ ...prev, page: 1 }));
    setSelection(null);
  }, [filters, updateFilters]);

  const handleChipChange = (key, value) => applyQuickFilter(buildChipPatch(key, value));
  const handleClearChips = () => applyQuickFilter(buildClearChipsPatch());

  // 自定义时间段：工具栏三个预设随之都不高亮
  const handleCustomRange = ({ startDate, endDate }) => {
    applyQuickFilter({ startDate, endDate });
    markCustomRange();
  };

  // 右键「只看此…」后把筛选条亮出来，生效的条件看得见
  const applyContextFilter = useCallback((patch) => {
    if (patch.sessionId) {
      // 会话与近 30 天一次性生效，避免先筛选再切时间触发两次查询。
      selectRange('30days', patch);
    } else {
      applyQuickFilter(patch);
    }
    setFilterBarVisible(true);
  }, [applyQuickFilter, selectRange, setFilterBarVisible]);

  const handleSessionFilter = useCallback((sessionId) => applyContextFilter({ sessionId }), [applyContextFilter]);

  // 页码变更
  const handlePageChange = (newPage) => {
    setPagination(prev => ({ ...prev, page: newPage }));
    setSelection(null);
  };

  // 每页条数变更
  const handlePageSizeChange = (newPageSize) => {
    writeStorage(PAGE_SIZE_STORAGE_KEY, newPageSize);
    setPagination(prev => ({
      ...prev,
      pageSize: newPageSize,
      page: 1 // 重置到第一页
    }));
    setSelection(null);
  };

  // 行回调必须 useCallback：作为 prop 传给 memo 化的 RequestRow，
  // 每次渲染换新引用会让整页行的 memo 全部失效。
  const handleRowSelect = useCallback((request) => setSelection(request), []);
  const { show: showDetailPane, toggle: toggleDetailPane } = detailPane;
  const handleRowOpen = useCallback(() => showDetailPane(), [showDetailPane]);

  const handleRowContextMenu = useCallback((event, request) => {
    setSelection(request);
    const model = request.model || '';
    const upstream = resolveUpstreamFilterOption(request);
    const filterItems = [
      request.sessionId && { label: '只看此会话', onSelect: () => handleSessionFilter(request.sessionId) },
      model && { label: `只看模型「${model}」`, onSelect: () => applyContextFilter({ model }) },
      upstream && { label: `只看上游「${upstream.label}」`, onSelect: () => applyContextFilter({ upstreamName: upstream.value }) }
    ].filter(Boolean);
    openContextMenu(event, [
      { label: '复制请求 ID', shortcut: formatShortcut('C'), onSelect: () => copyTextToClipboard(request.requestId, '请求 ID') },
      request.sessionId && { label: '复制会话 ID', onSelect: () => copyTextToClipboard(request.sessionId, '会话 ID') },
      model && { label: '复制模型名', onSelect: () => copyTextToClipboard(model, '模型名') },
      ...(filterItems.length > 0 ? ['-', ...filterItems] : []),
      '-',
      { label: detailPane.visible ? '隐藏详情面板' : '显示详情面板', onSelect: toggleDetailPane }
    ]);
  }, [applyContextFilter, handleSessionFilter, detailPane.visible, openContextMenu, toggleDetailPane]);

  // Claude 端点路由切换回调
  const handleClaudeEndpointSwitch = async (endpointName, mode = 'manual_preferred') => {
    try {
      setRouteSwitching(true);
      console.log('🔄 切换 Claude 路由:', endpointName, mode);
      const nextState = await setClaudeRoutingOverride({
        mode,
        endpointName,
        fallbackEnabled: mode !== 'manual_fixed'
      });

      if (!nextState?.unsupported) {
        setClaudeRoutingState(nextState);
        showNotice(
          'success',
          mode === 'manual_fixed'
            ? `已严格固定 Claude 端点「${endpointName}」`
            : `已优先使用 Claude 端点「${endpointName}」`
        );
      } else showNotice('info', nextState.message || '当前后端暂不支持保存 Claude 路由模式');

      // 切换后刷新数据
      await loadData(true);
    } catch (err) {
      console.error('❌ 切换失败:', err);
      throw err;
    } finally {
      setRouteSwitching(false);
    }
  };

  const handleModelRoutePatch = useCallback(async (binding, patch) => {
    setModelRoutePatching(true);
    try {
      await updateModelRouteBinding(binding.assignment_id, patchBindingPayload(binding, patch));
      const list = await fetchModelRouteBindings();
      setModelRouteBindings(Array.isArray(list) ? list : []);
      const mode = patch.route_mode === 'auto' ? '自动调度' : `${patch.route_mode === 'manual_fixed' ? '固定' : '优选'} ${patch.route_target}`;
      const subject = isWebToolsBinding(binding) ? '联网工具映射' : `模型「${bindingDisplayName(binding)}」`;
      showNotice('success', `${subject}已切换为${mode}`);
    } catch (err) {
      showNotice('error', err?.message || '切换模型路由策略失败');
    } finally {
      setModelRoutePatching(false);
    }
  }, [showNotice]);

  const handleCodexModelRoutePatch = useCallback(async (binding, patch) => {
    setCodexModelRoutePatching(true);
    try {
      const next = { ...binding, ...patch };
      const routeMode = next.route_mode || 'auto';
      const accountIDs = (next.account_ids || []).map(Number);
      const targetID = routeMode === 'auto' ? 0 : (Number(next.route_target_account_id) || accountIDs[0] || 0);
      await updateCodexModelRouteBinding(binding.assignment_id, {
        model: next.model,
        account_ids: accountIDs,
        member_enabled: next.member_enabled || {},
        route_mode: routeMode,
        route_target_account_id: targetID,
        enabled: next.enabled !== false,
        note: next.note || ''
      });
      const list = await fetchCodexModelRouteBindings();
      setCodexModelRouteBindings(Array.isArray(list) ? list : []);
      const targetAccount = accounts.find((account) => resolveAccountId(account) === targetID);
      const targetName = targetAccount?.account_name || targetAccount?.accountName || `#${targetID}`;
      const mode = routeMode === 'auto' ? '自动调度' : `${routeMode === 'manual_fixed' ? '固定' : '优选'}「${targetName}」`;
      showNotice('success', `Codex 模型「${binding.model}」已切换为${mode}`);
    } catch (err) {
      showNotice('error', err?.message || '切换 Codex 模型路由策略失败');
    } finally {
      setCodexModelRoutePatching(false);
    }
  }, [accounts, showNotice]);

  const handleRestoreClaudeAuto = useCallback(async () => {
    setRouteSwitching(true);
    try {
      const nextState = await clearClaudeRoutingOverride();
      if (nextState?.unsupported) {
        showNotice('info', nextState.message || '当前后端暂不支持恢复自动路由');
        return;
      }
      setClaudeRoutingState(nextState);
      showNotice('success', 'Claude 路由已恢复自动调度');
      await loadData(true);
    } catch (err) {
      showNotice('error', err.message || '恢复自动路由失败');
    } finally {
      setRouteSwitching(false);
    }
  }, [loadData, showNotice]);

  const handleClearRouteCache = useCallback(async (endpointName = '') => {
    setRouteSwitching(true);
    try {
      const result = await clearNegativeHitCache(endpointName);
      if (result?.unsupported) {
        showNotice('info', result.message || '当前后端暂不支持清理路由缓存');
        return;
      }
      showNotice('success', endpointName ? `已清理「${endpointName}」的路由缓存` : '已清理 Claude 路由缓存');
      await loadData(true);
    } catch (err) {
      showNotice('error', err.message || '清理路由缓存失败');
    } finally {
      setRouteSwitching(false);
    }
  }, [loadData, showNotice]);

  const handleAccountSwitch = useCallback(async (account) => {
    const accountId = resolveAccountId(account);
    const accountName = account?.account_name || account?.accountName || '目标账号';

    if (!isValidAccountId(accountId)) {
      const err = new Error('账号缺少 ID，无法切换');
      showNotice('error', err.message);
      return;
    }

    if (isSamePinnedAccount({ displayedActiveAccount: activeAccount, targetAccount: account })) {
      showNotice('info', `「${accountName}」已经固定为当前账号`);
      return;
    }

    setAccountSwitching(true);
    try {
      const result = await pinUpstreamAccountSelection(accountId);

      if (result?.unsupported) {
        showNotice('info', result.message || '当前后端版本暂不支持固定账号');
        return;
      }

      if (result?.changed !== true) {
        showNotice('info', `「${accountName}」已经固定为当前账号`);
        return;
      }

      showNotice('success', result?.message || `已固定使用「${accountName}」，除非该账号严格不可用才会自动切走`);
      await loadData(true);
    } catch (err) {
      showNotice('error', err.message || '固定账号失败');
      return;
    } finally {
      setAccountSwitching(false);
    }
  }, [activeAccount, loadData, showNotice]);

  const handleEnableAutoSelection = useCallback(async () => {
    if (activeAccount === null) {
      showNotice('info', '当前已按编排自动调度');
      return;
    }

    setAccountSwitching(true);
    try {
      const result = await enableAutomaticAccountSelection();

      if (result?.unsupported) {
        showNotice('info', result.message || '当前后端版本暂不支持启用编排');
        return;
      }

      if (result?.changed !== true) {
        showNotice('info', result?.message || '当前已按编排自动调度');
        return;
      }

      showNotice('success', result?.message || '已启用编排自动调度');
      await loadData(true);
    } catch (err) {
      showNotice('error', err.message || '启用编排失败');
    } finally {
      setAccountSwitching(false);
    }
  }, [activeAccount, loadData, showNotice]);

  // ==================== 生命周期 ====================

  useEffect(() => {
    loadData();
  }, [loadData]);

  // v8:订阅 Claude 路由事件,切换/请求生效后即时同步页头状态(不依赖下一次 loadData)
  useEffect(() => {
    if (!isWailsEnvironment()) return undefined;
    const unsubscribe = subscribeToEvent('claude-routing:update', (state) => {
      if (!state || typeof state !== 'object') return;
      setClaudeRoutingState(state);
    });
    const unsubscribeModelRouting = subscribeToEvent('model-routing:update', (state) => {
      if (state && typeof state === 'object') setModelRoutingSettings(state);
      // 端点页改了映射或本页切换策略后，同步刷新切换器里的映射列表。
      fetchModelRouteBindings().then((list) => setModelRouteBindings(Array.isArray(list) ? list : [])).catch((err) => {
        console.error('❌ 刷新模型路由映射失败:', err);
      });
      fetchCodexModelRouteBindings().then((list) => setCodexModelRouteBindings(Array.isArray(list) ? list : [])).catch((err) => {
        console.error('❌ 刷新 Codex 模型路由绑定失败:', err);
      });
    });
    return () => {
      if (typeof unsubscribe === 'function') {
        unsubscribe();
      }
      if (typeof unsubscribeModelRouting === 'function') unsubscribeModelRouting();
    };
  }, []);

  // ==================== 选中与键盘 ====================

  // 选中行优先取当前列表里的同 ID 行（随实时刷新更新），不在当前页时退回选中那一刻的快照
  const selectedRequest = useMemo(() => {
    if (!selection) return null;
    return requests.find((request) => request.requestId === selection.requestId) || selection;
  }, [requests, selection]);
  const requestIds = useMemo(() => requests.map((request) => request.requestId), [requests]);
  const selectById = useCallback((requestId) => {
    const row = requests.find((request) => request.requestId === requestId);
    if (row) setSelection(row);
  }, [requests]);
  const handleListNavigation = useListKeyboard({ ids: requestIds, selectedId: selectedRequest?.requestId ?? null, onSelect: selectById, containerRef: listRef });

  // ⌘C：列表里有选中文字时按系统默认复制文字，否则复制选中行的请求 ID
  const handleListKeyDown = (event) => {
    if (isCopyShortcut(event)) {
      if (selectedRequest && !window.getSelection()?.toString()) {
        event.preventDefault();
        copyTextToClipboard(selectedRequest.requestId, '请求 ID');
      }
      return;
    }
    handleListNavigation(event);
  };

  // ==================== 渲染 ====================

  if (error) {
    return (
      <div className="p-5">
        <ErrorMessage
          title="请求数据加载失败"
          message={error}
          onRetry={handleManualRefresh}
        />
      </div>
    );
  }

  return (
    <div className="motion-page relative flex min-h-0 flex-1 flex-col bg-surface">
      <NoticeToast notice={notice} onClose={closeNotice} />

      {/* 工具栏：投送到窗口统一工具栏 */}
      <PageToolbar subtitle={`共 ${Number(pagination.total || 0).toLocaleString()} 条`}>
        <div className="flex min-w-0 flex-1 justify-end">
          <Toolbar
            activeTimeRange={activeRange}
            onTimeRangeChange={selectRange}
            filterBarVisible={filterBarVisible}
            onFilterBarToggle={() => setFilterBarVisible(!filterBarVisible)}
            activeFilterCount={countActiveChips(filters)}
            onOpenColumnsMenu={(event) => openMenuAt(event.currentTarget, columnMenuItems)}
            onRefresh={handleManualRefresh}
            refreshing={refreshing}
            endpoints={endpoints}
            claudeRoutingState={claudeRoutingState}
            modelRoutingEnabled={modelRoutingSettings?.claude_enabled === true}
            codexModelRoutingEnabled={modelRoutingSettings?.codex_enabled === true}
            modelRouteBindings={modelRouteBindings}
            onModelRoutePatch={handleModelRoutePatch}
            modelRoutePatching={modelRoutePatching}
            codexModelRouteBindings={codexModelRouteBindings}
            onCodexModelRoutePatch={handleCodexModelRoutePatch}
            codexModelRoutePatching={codexModelRoutePatching}
            onClaudeEndpointSwitch={handleClaudeEndpointSwitch}
            onRestoreClaudeAuto={handleRestoreClaudeAuto}
            onClearRouteCache={handleClearRouteCache}
            onOpenClaudeModelRouting={onOpenClaudeModelRouting}
            onOpenCodexModelRouting={onOpenCodexModelRouting}
            onNavigateToCodexAccess={onNavigateToCodexAccess}
            routeSwitching={routeSwitching}
            accounts={accounts}
            activeAccount={activeAccount}
            recentSelectedAccountId={recentSelectedAccountId}
            onAccountSwitch={handleAccountSwitch}
            onEnableAutoSelection={handleEnableAutoSelection}
            accountSwitching={accountSwitching}
            detailPaneVisible={detailPane.visible}
            onDetailPaneToggle={toggleDetailPane}
          />
        </div>
      </PageToolbar>

      {/* 筛选条：工具栏下方，选了立即生效 */}
      {filterBarVisible && (
        <FilterBar
          filters={filters}
          activeRange={activeRange}
          models={models}
          upstreams={upstreamOptions}
          onChange={handleChipChange}
          onClear={handleClearChips}
          onApplyCustomRange={handleCustomRange}
        />
      )}

      {/* 列表 + 详情面板 */}
      <div
        ref={detailPane.containerRef}
        className="flex min-h-0 flex-1 flex-col"
      >
        <div className="relative min-h-0 flex-1">
          {/* 刷新进度条：盖在表头上沿，不占布局高度 */}
          {refreshing && (
            <div className="absolute inset-x-0 top-0 z-[4] h-0.5 overflow-hidden">
              <div className="indeterminate-bar h-full w-1/3 bg-accent-ring/70" />
            </div>
          )}
          <div
            ref={listRef}
            tabIndex={0}
            onKeyDown={handleListKeyDown}
            aria-label="请求列表"
            className="absolute inset-0 overflow-auto focus:outline-none"
          >
            <RequestsTable
              requests={requests}
              loading={loading}
              enterAnimations={rowEnterAnimations}
              visibleColumns={visibleColumns}
              columnConfigs={columnConfigs}
              selectedId={selectedRequest?.requestId ?? null}
              onSelect={handleRowSelect}
              onOpen={handleRowOpen}
              onRowContextMenu={handleRowContextMenu}
              onHeaderContextMenu={(event) => openContextMenu(event, columnMenuItems)}
            />
          </div>
        </div>

        {detailPane.visible && (
          <>
            <PaneSplitter label="调整详情面板高度" {...detailPane.splitterProps} />
            {/* max-h 兜住窗口变矮：按视口算（52 工具栏 + 26 状态栏 + 列表至少 120），
                主内容区只有 min-height，百分比在这里解析不出来 */}
            <div
              className="flex max-h-[calc(100vh-198px)] shrink-0 flex-col bg-inspector"
              style={{ height: detailPane.height }}
            >
              <RequestDetailPane request={selectedRequest} onClose={detailPane.hide} onFilterSession={handleSessionFilter} />
            </div>
          </>
        )}
      </div>

      <StatusBar>
        <StatsOverview stats={stats} />
        <StatusSpacer />
        <RequestRefreshStatus mode={autoRefresh.mode} fallbackInterval={autoRefresh.fallbackInterval} />
        <StatusPager
          currentPage={pagination.page}
          pageSize={pagination.pageSize}
          pageSizeOptions={PAGINATION_CONFIG.PAGE_SIZE_OPTIONS}
          total={pagination.total}
          onPageChange={handlePageChange}
          onPageSizeChange={handlePageSizeChange}
          onOpenMenu={openMenuAt}
        />
      </StatusBar>

      {contextMenu}
    </div>
  );
};

export default RequestsPage;
