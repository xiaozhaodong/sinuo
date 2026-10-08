// ============================================
// Pricing 页面 - 模型定价管理
// 2025-12-06 (v5.0 新增)
// 2026-10-07：桌面化——卡片网格改为表格 + 右侧检查器就地编辑，统计卡收进状态栏；
//             错误不再走 window.alert（WKWebView 不弹窗），改为页内提示
// ============================================

import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { AlertCircle, Calculator, Database, Plus, RefreshCw, Tag, X } from 'lucide-react';
import { ErrorMessage, LoadingSpinner } from '@components/ui';
import { PageToolbar } from '@components/layout/AppToolbar.jsx';
import { ToolbarButton, ToolbarSpacer } from '@components/layout/ToolbarButton.jsx';
import ToolbarSearch from '@components/layout/ToolbarSearch.jsx';
import { Inspector, InspectorEmpty } from '@components/desktop/Inspector.jsx';
import { StatusBar, StatusItem, StatusSpacer } from '@components/desktop/StatusBar.jsx';
import useContextMenu from '@components/desktop/useContextMenu.jsx';
import useListKeyboard from '@components/desktop/useListKeyboard.js';
import ListEmpty from '@components/desktop/ListEmpty.jsx';
import useConfirm from '@hooks/useConfirm.js';
import { resolveErrorMessage } from '@utils/errors.js';
import { copyTextToClipboard } from '@pages/requests/components/clipboard.js';
import {
  getModelPricingStorageStatus,
  getModelPricings,
  createModelPricing,
  updateModelPricing,
  deleteModelPricing,
  setDefaultModelPricing
} from '@utils/wailsApi.js';
import PricingTable from './components/PricingTable.jsx';
import PricingInspector from './components/PricingInspector.jsx';
import { sortAndFilterPricings } from './utils/pricingForm.js';

const NEW_SELECTION = '__new__';

const DISCARD_CONFIRM = {
  title: '放弃未保存的修改？',
  message: '当前定价有未保存的修改，继续操作后将丢失。',
  confirmText: '放弃修改',
  danger: true
};

const averageOf = (pricings, key) => (pricings.length > 0
  ? (pricings.reduce((sum, item) => sum + (Number(item[key]) || 0), 0) / pricings.length).toFixed(2)
  : '0.00');

const PricingPage = () => {
  const [storageStatus, setStorageStatus] = useState(null);
  const [pricings, setPricings] = useState([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState(null);
  // revision 每次加载完成后自增，用作检查器的 key：保存 / 刷新后表单按最新数据重建
  const [revision, setRevision] = useState(0);
  const [query, setQuery] = useState('');
  const [selectedId, setSelectedId] = useState(null);
  const [saving, setSaving] = useState(false);
  const [actionError, setActionError] = useState(null);
  const dirtyRef = useRef(false);
  const listRef = useRef(null);
  const { confirm, confirmDialog } = useConfirm();
  const { contextMenu, openContextMenu } = useContextMenu();

  const loadData = useCallback(async () => {
    setLoading(true);
    setError(null);
    try {
      const [status, records] = await Promise.all([
        getModelPricingStorageStatus(),
        getModelPricings()
      ]);
      setStorageStatus(status);
      setPricings(records || []);
      setRevision((value) => value + 1);
    } catch (err) {
      console.error('加载模型定价失败:', err);
      setError(resolveErrorMessage(err, '加载模型定价失败'));
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    loadData();
  }, [loadData]);

  const rows = useMemo(() => sortAndFilterPricings(pricings, query), [pricings, query]);
  const rowIds = useMemo(() => rows.map((item) => item.modelName), [rows]);

  // 未选或选中项已被删除时落到第一行
  const isNew = selectedId === NEW_SELECTION;
  const effectiveId = isNew || pricings.some((item) => item.modelName === selectedId)
    ? selectedId
    : rowIds[0] ?? null;
  const selected = isNew ? null : pricings.find((item) => item.modelName === effectiveId) || null;

  const handleDirtyChange = useCallback((value) => {
    dirtyRef.current = value;
  }, []);

  const confirmDiscard = useCallback(async () => {
    if (!dirtyRef.current) return true;
    const discard = await confirm(DISCARD_CONFIRM);
    if (discard) dirtyRef.current = false;
    return discard;
  }, [confirm]);

  const requestSelect = useCallback(async (nextId) => {
    if (nextId === effectiveId || !(await confirmDiscard())) return;
    setSelectedId(nextId);
  }, [effectiveId, confirmDiscard]);

  const requestRefresh = async () => {
    if (await confirmDiscard()) loadData();
  };

  const handleListKeyDown = useListKeyboard({ ids: rowIds, selectedId: effectiveId, onSelect: requestSelect, containerRef: listRef });

  const handleSave = async (payload) => {
    setSaving(true);
    try {
      if (isNew) {
        await createModelPricing(payload);
      } else {
        await updateModelPricing(selected.modelName, payload);
      }
      dirtyRef.current = false;
      setSelectedId(isNew ? payload.modelName : selected.modelName);
      await loadData();
    } finally {
      setSaving(false);
    }
  };

  const handleDelete = async (pricing) => {
    const confirmed = await confirm({
      title: '删除模型定价',
      message: `确定要删除「${pricing.displayName || pricing.modelName}」的定价吗？删除后无法恢复。`,
      confirmText: '删除',
      danger: true
    });
    if (!confirmed) return;
    setActionError(null);
    try {
      await deleteModelPricing(pricing.modelName);
      const index = rowIds.indexOf(pricing.modelName);
      dirtyRef.current = false;
      setSelectedId(rowIds[index + 1] ?? rowIds[index - 1] ?? null);
      await loadData();
    } catch (err) {
      setActionError(resolveErrorMessage(err, '删除失败'));
    }
  };

  const handleSetDefault = async (pricing) => {
    if (!(await confirmDiscard())) return;
    setActionError(null);
    try {
      await setDefaultModelPricing(pricing.modelName);
      await loadData();
    } catch (err) {
      setActionError(resolveErrorMessage(err, '设置默认定价失败'));
    }
  };

  const handleRowContextMenu = (event, pricing) => {
    if (!dirtyRef.current) setSelectedId(pricing.modelName);
    openContextMenu(event, [
      { label: '复制模型 ID', onSelect: () => copyTextToClipboard(pricing.modelName, '模型 ID') },
      { label: '设为默认定价', disabled: pricing.isDefault, onSelect: () => handleSetDefault(pricing) },
      '-',
      { label: '删除…', danger: true, disabled: pricing.isDefault, onSelect: () => handleDelete(pricing) }
    ]);
  };

  const toolbar = (
    <PageToolbar subtitle={`${pricings.length} 个模型 · USD / 1M tokens`}>
      <ToolbarSpacer />
      <ToolbarSearch value={query} onChange={setQuery} placeholder="筛选模型" />
      <ToolbarButton icon={RefreshCw} title="重新载入" onClick={requestRefresh} loading={loading && pricings.length > 0} />
      <ToolbarButton icon={Plus} title="添加定价" onClick={() => requestSelect(NEW_SELECTION)} />
    </PageToolbar>
  );

  if (!loading && storageStatus && !storageStatus.enabled) {
    return (
      <div className="motion-page flex flex-1 flex-col items-center justify-center gap-3 p-5 text-center">
        {toolbar}
        <Database size={36} strokeWidth={1.25} className="text-fg-subtle" />
        <h3 className="text-[15px] font-semibold text-fg-body">模型定价服务未启用</h3>
        <p className="max-w-md text-[13px] text-fg-muted">
          请在配置文件中启用 <code className="rounded bg-surface-mut px-1.5 py-0.5 text-xs">usage_tracking.enabled: true</code>
        </p>
      </div>
    );
  }

  if (error && pricings.length === 0) {
    return (
      <div className="p-5">
        {toolbar}
        <ErrorMessage title="数据加载失败" message={error} onRetry={loadData} />
      </div>
    );
  }

  if (loading && pricings.length === 0) {
    return <LoadingSpinner text="加载模型定价..." />;
  }

  const defaultPricing = pricings.find((item) => item.isDefault);

  return (
    <div className="motion-page flex min-h-0 flex-1 bg-surface">
      {toolbar}

      <div className="flex min-w-0 flex-1 flex-col">
        {(actionError || error) && (
          <div role="alert" className="selectable flex shrink-0 items-center gap-2 border-b border-danger-line bg-danger-soft px-4 py-2 text-xs text-danger">
            <AlertCircle size={14} className="shrink-0" />
            <span className="min-w-0 flex-1">{actionError || error}</span>
            <button type="button" onClick={() => { setActionError(null); setError(null); }} aria-label="关闭提示" className="shrink-0 rounded p-0.5 hover:bg-danger-soft">
              <X size={13} />
            </button>
          </div>
        )}

        <div
          ref={listRef}
          tabIndex={0}
          onKeyDown={handleListKeyDown}
          aria-label="模型定价列表"
          className="min-h-0 flex-1 overflow-auto focus:outline-none"
        >
          {rows.length > 0 ? (
            <PricingTable
              pricings={rows}
              selectedId={effectiveId}
              onSelect={requestSelect}
              onRowContextMenu={handleRowContextMenu}
            />
          ) : (
            <ListEmpty icon={Calculator} title={query ? '没有匹配的模型' : '暂无定价配置，点击工具栏的 + 添加'} />
          )}
        </div>

        <StatusBar>
          <StatusItem label="模型" value={pricings.length} />
          {query && <StatusItem label="筛选后" value={rows.length} />}
          <StatusItem label="平均输入" value={`$${averageOf(pricings, 'inputPrice')}`} />
          <StatusItem label="平均输出" value={`$${averageOf(pricings, 'outputPrice')}`} />
          <StatusSpacer />
          <StatusItem label="默认定价" value={defaultPricing?.displayName || defaultPricing?.modelName || '—'} />
        </StatusBar>
      </div>

      {isNew || selected ? (
        <PricingInspector
          key={isNew ? NEW_SELECTION : `${selected.modelName}:${revision}`}
          pricing={selected}
          isNew={isNew}
          saving={saving}
          onSave={handleSave}
          onCancelNew={() => {
            dirtyRef.current = false;
            setSelectedId(null);
          }}
          onDelete={handleDelete}
          onDirtyChange={handleDirtyChange}
        />
      ) : (
        <Inspector label="定价详情">
          <InspectorEmpty icon={Tag}>选择一个模型查看和编辑定价</InspectorEmpty>
        </Inspector>
      )}

      {contextMenu}
      {confirmDialog}
    </div>
  );
};

export default PricingPage;
