// ============================================
// Requests 页面 - 请求追踪（重构版）
// 2025-12-06 10:40:38 v4.0: 简化端点切换（移除 Token 级联）
// ============================================

import { useState, useEffect, useCallback, useMemo, useRef } from 'react';
import { BarChart3 } from 'lucide-react';
import { ErrorMessage } from '@components/ui';
import { NoticeToast } from '@pages/account-pool/components';
import { useNotice } from '@pages/account-pool/hooks';
import {
  compareAccountsByManualPriority,
  isValidAccountId,
  resolveAccountId,
} from '@pages/account-pool/utils.js';
import {
  fetchRequests,
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
import { FiltersPanel, StatsOverview, RequestsTable, Toolbar, RequestDetailModal } from './components';
import { PAGINATION_CONFIG } from './utils/constants.js';
import { isRuntimeActiveSelection, isSamePinnedAccount } from './utils/accountSwitcherState.js';
import { patchBindingPayload } from '../endpoints/utils/modelRouting.js';
import { buildTimeRangeSelectionState } from './utils/timeRangeSelection.js';
import { mergeRequestRows } from './utils/mergeRequestRows.js';
import { buildRowEnterAnimations, collectRequestIds, NO_ROW_ANIMATIONS } from './utils/rowEnterAnimation.js';
import { filterUpstreamOptionsByFamily } from './utils/requestSource.js';
import { prefersReducedMotion } from '@utils/motion.js';
import { consumePendingRequestsFilters } from '@utils/overviewHandoff.js';

// ============================================
// Requests 页面
// ============================================

const RequestsPage = () => {
  // ==================== 状态管理 ====================
  const { notice, showNotice, closeNotice } = useNotice();

  // 数据状态
  const [requests, setRequests] = useState([]);
  const [stats, setStats] = useState(null);
  const [models, setModels] = useState([]);
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

  // 面板状态
  const [isFilterOpen, setIsFilterOpen] = useState(false);
  const [isViewConfigOpen, setIsViewConfigOpen] = useState(false);

  // 详情模态框状态
  const [selectedRequest, setSelectedRequest] = useState(null);
  const [isDetailModalOpen, setIsDetailModalOpen] = useState(false);

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
    updateFilter,
    updateFilters,
    resetFilters,
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

  // 时间范围 Hook
  const { activeRange, selectRange } = useTimeRange((timeRange) => {
    const nextState = buildTimeRangeSelectionState(filters, timeRange);
    updateFilters(nextState.filters);
    setAppliedQueryParams(nextState.appliedQueryParams);
    setPagination(prev => ({ ...prev, page: 1 }));
  }, handoffRef.current.timeRangeKey);

  // 分页状态
  const [pagination, setPagination] = useState({
    page: 1,
    pageSize: PAGINATION_CONFIG.DEFAULT_PAGE_SIZE,
    total: 0,
    totalPages: 1
  });

  const sortedAccounts = useMemo(() => {
    return [...accounts].sort(compareAccountsByManualPriority);
  }, [accounts]);

  const upstreamOptions = useMemo(() => filterUpstreamOptionsByFamily(requests, filters.requestFamily), [filters.requestFamily, requests]);

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
      period: '30d'
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
      const [, modelsData, endpointsData, routeStateData, modelRoutingData, modelBindingsData, codexModelBindingsData, accountsData, latestSnapshotData] = await Promise.all([
        refreshRequestData(),
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
  }, [refreshRequestData, showNotice]);

  // 请求变化由 Wails 事件触发，只刷新明细与统计；事件不可用时低频降级。
  // 包一层标记 live，使事件驱动的新行才播入场动画。
  const refreshLiveRequestData = useCallback(() => refreshRequestData({ live: true }), [refreshRequestData]);
  const autoRefresh = useAutoRefresh(refreshLiveRequestData);

  // 手动刷新：显式不传参，避免 onClick 的 event 对象被当成 silent 实参。
  const handleManualRefresh = useCallback(() => loadData(), [loadData]);

  // ==================== 事件处理 ====================

  // 筛选面板切换
  const handleFilterToggle = () => {
    setIsFilterOpen(!isFilterOpen);
    setIsViewConfigOpen(false); // 关闭列配置
  };

  // 列配置面板切换
  const handleViewConfigToggle = () => {
    setIsViewConfigOpen(!isViewConfigOpen);
    setIsFilterOpen(false); // 关闭筛选面板
  };

  // 应用筛选
  const handleApplyFilters = () => {
    setAppliedQueryParams(buildQueryParams());
    setPagination(prev => ({ ...prev, page: 1 }));
  };

  // 重置筛选
  const handleResetFilters = () => {
    const nextFilters = resetFilters();
    setAppliedQueryParams(buildQueryParamsFromFilters(nextFilters));
    setPagination(prev => ({ ...prev, page: 1 }));
  };

  // 页码变更
  const handlePageChange = (newPage) => {
    setPagination(prev => ({ ...prev, page: newPage }));
  };

  // 每页条数变更
  const handlePageSizeChange = (newPageSize) => {
    setPagination(prev => ({
      ...prev,
      pageSize: newPageSize,
      page: 1 // 重置到第一页
    }));
  };

  // 双击行打开详情
  // 必须 useCallback：作为 prop 传给 memo 化的 RequestRow，
  // 每次渲染换新引用会让整页行的 memo 全部失效。
  const handleRowDoubleClick = useCallback((request) => {
    setSelectedRequest(request);
    setIsDetailModalOpen(true);
  }, []);

  // 关闭详情模态框
  const handleCloseDetailModal = () => {
    setIsDetailModalOpen(false);
    setSelectedRequest(null);
  };

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
      showNotice('success', `模型「${binding.model}」已切换为${mode}`);
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

  // ==================== 渲染 ====================

  if (error) {
    return (
      <ErrorMessage
        title="请求数据加载失败"
        message={error}
        onRetry={handleManualRefresh}
      />
    );
  }

  return (
    <div className="space-y-6 animate-fade-in relative">
      <NoticeToast notice={notice} onClose={closeNotice} />

      {/* 页面标题 & 工具栏（单行） */}
      <div className="flex items-center gap-2 xl:gap-3 relative z-30">
        {/* 页面标题 */}
        <div className="flex items-center gap-2 shrink-0">
          <div className="p-1.5 bg-indigo-600 rounded-lg text-white shadow-lg">
            <BarChart3 className="w-5 h-5" />
          </div>
          <h1 className="text-lg xl:text-xl font-bold text-fg tracking-tight whitespace-nowrap">请求追踪</h1>
        </div>

        {/* 工具栏 */}
        <div className="flex-1 min-w-0 flex justify-end">
          <Toolbar
            activeTimeRange={activeRange}
            onTimeRangeChange={selectRange}
            isFilterOpen={isFilterOpen}
            onFilterToggle={handleFilterToggle}
            isViewConfigOpen={isViewConfigOpen}
            onViewConfigToggle={handleViewConfigToggle}
            onRefresh={handleManualRefresh}
            refreshing={refreshing}
            columns={columnConfigs}
            visibleColumns={visibleColumns}
            onToggleColumn={toggleColumn}
            onResetColumns={resetColumns}
            autoRefresh={autoRefresh}
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
            routeSwitching={routeSwitching}
            accounts={accounts}
            activeAccount={activeAccount}
            recentSelectedAccountId={recentSelectedAccountId}
            onAccountSwitch={handleAccountSwitch}
            onEnableAutoSelection={handleEnableAutoSelection}
            accountSwitching={accountSwitching}
          />
        </div>

        {/* 筛选面板（弹出式） */}
        <div className="absolute top-full left-0 right-0 z-10">
          <FiltersPanel
            isOpen={isFilterOpen}
            onClose={() => setIsFilterOpen(false)}
            filters={filters}
            updateFilter={updateFilter}
            onApply={handleApplyFilters}
            onReset={handleResetFilters}
            models={models}
            upstreams={upstreamOptions}
          />
        </div>
      </div>

      {/* 统计概览 - 面板打开时blur */}
      <StatsOverview
        stats={stats}
        total={pagination.total}
        isBlurred={isFilterOpen || isViewConfigOpen}
      />

      {/* 请求列表表格 - 面板打开时blur */}
      <div className={`transition-all duration-300 ${isFilterOpen || isViewConfigOpen ? 'opacity-40 pointer-events-none blur-[1px]' : ''}`}>
        <RequestsTable
          requests={requests}
          loading={loading}
          refreshing={refreshing}
          enterAnimations={rowEnterAnimations}
          pagination={pagination}
          onPageChange={handlePageChange}
          onPageSizeChange={handlePageSizeChange}
          visibleColumns={visibleColumns}
          columnConfigs={columnConfigs}
          onRowDoubleClick={handleRowDoubleClick}
        />
      </div>

      {/* 请求详情模态框 */}
      <RequestDetailModal
        isOpen={isDetailModalOpen}
        onClose={handleCloseDetailModal}
        request={selectedRequest}
      />
    </div>
  );
};

export default RequestsPage;
