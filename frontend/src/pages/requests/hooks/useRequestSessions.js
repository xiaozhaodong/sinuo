import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { fetchRequestSessions, fetchRequestSessionTrend, fetchRequests } from '@utils/api.js';
import { isWailsEnvironment } from '@utils/wailsApi.js';
import { buildQueryParamsFromFilters } from '../utils/filterTime.js';
import { createSessionQueryController, requireLocatedRequest, sessionDrillQuery, sessionKey } from '../utils/sessionUsage.js';

// 独立保存会话工作台状态，正式请求页只接入视图切换与现有数据回调。
export default function useRequestSessions({ filters, query, pageSize, activeRange, restoreTimeRange, updateFilters, setQuery, setPagination, setRequests, setSelection, showDetail, showNotice }) {
  const supported = isWailsEnvironment();
  const [mode, setMode] = useState('requests');
  const [returnState, setReturnState] = useState(null);
  const scope = returnState?.query || query;
  const scopeKey = JSON.stringify(scope);
  const [table, setTable] = useState({ scope: '', page: 1, size: 30, sort: 'recent', order: 'desc' });
  const page = table.scope === scopeKey ? table.page : 1;
  const rankingKey = JSON.stringify([scopeKey, page, table.size, table.sort, table.order]);
  const [ranking, setRanking] = useState({ key: null, data: null, revision: 0, error: null });
  const data = ranking.key === rankingKey ? ranking.data : null;
  const [chosen, setChosen] = useState({ scope: null, key: null });
  // 当前页是选中项的唯一来源；等待新排行时不可下钻旧页的会话。
  const selected = data?.rows?.find(row => chosen.scope === scopeKey && sessionKey(row) === chosen.key) || data?.rows?.[0] || null;
  const selectedKey = sessionKey(selected);
  const selectedFamily = selected?.request_family;
  const selectedSessionID = selected?.session_id;
  const [trendState, setTrendState] = useState({ key: null, data: null, error: null });
  const trendKey = JSON.stringify([scopeKey, selectedKey]);
  const trend = trendState.key === trendKey ? trendState.data : null;
  const trendError = trendState.key === trendKey ? trendState.error : null;
  const selectedIDs = useRef(new Map());
  const trendNavigation = useRef(null);
  const [pointFocusRevision, setPointFocusRevision] = useState(0);
  const [pointChoice, setPointChoice] = useState({ key: null, id: '' });
  const selectedID = pointChoice.key === trendKey ? pointChoice.id : trend?.selected_id;
  const point = trend?.points?.find((item) => item.request_id === selectedID) || null;
  const [controller] = useState(createSessionQueryController);
  const locationKey = JSON.stringify([rankingKey, selectedKey, selectedID, mode]);
  const [locationPending, setLocationPending] = useState(null);
  const locating = locationPending === locationKey;
  const cancelLocation = useCallback(() => {
    controller.cancel('location');
    setLocationPending(null);
  }, [controller, setLocationPending]);

  // 用户选择在事件回调里立即使定位失效；自动刷新引起的选择变化也必须失效。
  useEffect(() => () => {
    controller.cancel('location');
    // 失效键不能在筛选往返后复活，也不能清掉另一定位新写入的等待状态。
    setLocationPending(pending => pending === locationKey ? null : pending);
  }, [controller, locationKey, setLocationPending]);

  const refresh = useCallback(() => controller.run('ranking',
    () => fetchRequestSessions({ ...scope, page, pageSize: table.size, sort_by: table.sort, sort_order: table.order }),
    (result) => {
      setRanking((prev) => ({ key: rankingKey, data: result, revision: prev.revision + 1, error: null }));
      setChosen(prev => {
        const key = result.rows.some(row => prev.scope === scopeKey && sessionKey(row) === prev.key) ? prev.key : sessionKey(result.rows[0]);
        return prev.scope === scopeKey && prev.key === key ? prev : {scope: scopeKey, key};
      });
    },
    (error) => { setRanking((prev) => ({ ...prev, key: rankingKey, error: error.message })); throw error; }
  ), [controller, scope, scopeKey, rankingKey, page, table.size, table.sort, table.order, setRanking, setChosen]);

  const loadTrend = useCallback((nextPage = 0) => {
    if (!selectedFamily || !selectedSessionID) return Promise.resolve();
    return controller.run('trend',
      () => fetchRequestSessionTrend({ ...scope, request_family: selectedFamily, session_id: selectedSessionID, session_missing: false }, selectedIDs.current.get(trendKey) || '', nextPage),
      (result) => {
        setTrendState({ key: trendKey, data: result, error: null });
        setPointChoice({ key: trendKey, id: result.selected_id });
        if (result.selected_id) selectedIDs.current.set(trendKey, result.selected_id);
      },
      (error) => setTrendState({ key: trendKey, data: null, error: error.message })
    );
  }, [controller, selectedFamily, selectedSessionID, scope, trendKey, setTrendState, setPointChoice]);

  const changeTrendPage = useCallback(async (nextPage = 0) => {
    if (nextPage <= 0) return loadTrend(nextPage);
    cancelLocation();
    const navigation = {key: trendKey};
    trendNavigation.current = navigation;
    try { return await loadTrend(nextPage); }
    finally { if (trendNavigation.current === navigation) trendNavigation.current = null; }
  }, [cancelLocation, loadTrend, trendKey]);

  // 只有会话、范围或视图失效时取消走势；排行刷新不能取消用户正在进行的翻页。
  useEffect(() => () => {
    controller.cancel('trend');
    if (trendNavigation.current?.key === trendKey) trendNavigation.current = null;
  }, [controller, trendKey, mode]);

  useEffect(() => {
    if (mode !== 'sessions' || trendNavigation.current?.key === trendKey) return;
    void loadTrend();
  }, [loadTrend, mode, trendKey, ranking.revision]);

  useEffect(() => () => {
    controller.cancel('ranking'); controller.cancel('trend');
  }, [controller, scopeKey]);

  const choosePoint = useCallback((next) => {
    cancelLocation();
    controller.cancel('trend');
    trendNavigation.current = null;
    selectedIDs.current.set(trendKey, next.request_id);
    setPointChoice({ key: trendKey, id: next.request_id });
    setPointFocusRevision(prev => prev + 1);
  }, [controller, trendKey, cancelLocation, setPointChoice, setPointFocusRevision]);

  const resetPoint = useCallback(() => {
    cancelLocation();
    trendNavigation.current = null;
    selectedIDs.current.delete(trendKey);
    return loadTrend();
  }, [trendKey, loadTrend, cancelLocation]);

  const changeMode = useCallback((next) => {
    if (next === 'sessions' && returnState) {
      restoreTimeRange(returnState.activeRange);
      updateFilters(returnState.filters);
      setQuery(returnState.query);
      setReturnState(null);
    }
    cancelLocation();
    setMode(next);
  }, [cancelLocation, returnState, restoreTimeRange, updateFilters, setQuery, setReturnState, setMode]);

  const navigate = useCallback(async (ungrouped = false) => {
    if (!ungrouped && (!selected || !point)) return;
    setLocationPending(locationKey);
    const patch = ungrouped
      ? { sessionId: '', sessionMissing: true }
      : { requestFamily: selected.request_family, sessionId: selected.session_id, sessionMissing: false };
    const nextFilters = { ...filters, ...patch };
    const navigationQuery = ungrouped ? buildQueryParamsFromFilters(nextFilters) : sessionDrillQuery(scope, selected, point.request_id);
    const nextQuery = { ...navigationQuery };
    // 目标 ID 只用于本次定位，常规刷新按已定位页码读取，不追随旧目标。
    delete nextQuery.target_request_id;
    return controller.run('location', () => fetchRequests({ ...navigationQuery, page: 1, pageSize }), (result) => {
      const row = ungrouped ? null : requireLocatedRequest(result, point.request_id);
      setReturnState({ filters: { ...filters }, query: scope, activeRange });
      updateFilters(nextFilters);
      setQuery(nextQuery);
      setRequests(result.requests);
      setPagination({ page: result.page, pageSize: result.pageSize, total: result.total, totalPages: result.totalPages });
      setSelection(row);
      showDetail();
      setMode('requests');
      setLocationPending(null);
    }, (error) => { showNotice('error', error.message); setLocationPending(null); });
  }, [controller, selected, point, filters, scope, pageSize, activeRange, locationKey, updateFilters, setQuery, setRequests, setPagination, setSelection, showDetail, showNotice, setReturnState, setMode, setLocationPending]);

  const select = useCallback((row) => { cancelLocation(); setChosen({ scope: scopeKey, key: sessionKey(row) }); }, [scopeKey, cancelLocation, setChosen]);
  const changePage = useCallback((next) => {
    cancelLocation();
    setTable((prev) => ({ ...prev, scope: scopeKey, page: next }));
    setChosen({ scope: null, key: null });
  }, [scopeKey, cancelLocation, setTable, setChosen]);
  const changeSize = useCallback((size) => {
    cancelLocation();
    setTable((prev) => ({ ...prev, scope: scopeKey, page: 1, size }));
    setChosen({ scope: null, key: null });
  }, [scopeKey, cancelLocation, setTable, setChosen]);
  const sort = useCallback((key) => {
    cancelLocation();
    setTable((prev) => ({ ...prev, scope: scopeKey, page: 1, sort: key, order: prev.sort === key && prev.order === 'desc' ? 'asc' : 'desc' }));
  }, [scopeKey, cancelLocation, setTable]);
  const rows = useMemo(() => data?.rows || [], [data]);
  const returnDescription = query.session_missing ? '正在查看无会话 ID 的请求'
    : query.session_id ? query.request_family ? '已按请求族与完整会话 ID 筛选' : '已按完整会话 ID 筛选'
      : query.request_family ? '已按请求族筛选' : null;
  return { supported, mode, changeMode, refresh, data, rows, page, size: table.size, sortBy: table.sort, sortOrder: table.order,
    select, selected, selectedKey, changePage, changeSize, sort, trend, trendError, point, pointFocusRevision, choosePoint, resetPoint, loadTrend: changeTrendPage, navigate, locating,
    returnAvailable: Boolean(returnState), returnDescription, loading: !data, error: ranking.key === rankingKey ? ranking.error : null };
}
