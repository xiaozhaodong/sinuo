// ============================================
// Pricing 页面 - 模型定价管理
// 2025-12-06 (v5.0 新增)
// ============================================

import { useState, useEffect, useCallback, useRef } from 'react';
import { createPortal } from 'react-dom';
import {
  Plus,
  Pencil,
  Trash2,
  AlertTriangle,
  Database,
  RefreshCw,
  Star,
  Calculator,
  TrendingUp,
  TrendingDown,
  Sparkles
} from 'lucide-react';
import {
  Button,
  LoadingSpinner,
  ErrorMessage
} from '@components/ui';
import useModalLifecycle from '@hooks/useModalLifecycle.js';
import {
  getModelPricingStorageStatus,
  getModelPricings,
  createModelPricing,
  updateModelPricing,
  deleteModelPricing,
  setDefaultModelPricing
} from '@utils/wailsApi.js';
import { useTimezone } from '@contexts/TimezoneContext.jsx';

const PRICE_INPUT_STEP = '0.001';
const CACHE_PRICE_DECIMALS = 3;

// ============================================
// 定价表单弹窗
// ============================================

const PricingForm = ({ pricing, onSave, onCancel, loading }) => {
  const isEdit = !!pricing;
  const dialogRef = useRef(null);

  // 挂载即打开（父组件条件渲染），卸载时由 hook 清理
  useModalLifecycle({
    open: true,
    onClose: () => {
      if (!loading) onCancel();
    },
    initialFocusRef: dialogRef
  });

  const [formData, setFormData] = useState({
    modelName: pricing?.modelName || '',
    displayName: pricing?.displayName || '',
    description: pricing?.description || '',
    inputPrice: pricing?.inputPrice || 3.0,
    outputPrice: pricing?.outputPrice || 15.0,
    cacheCreationPrice5m: pricing?.cacheCreationPrice5m || 3.75,
    cacheCreationPrice1h: pricing?.cacheCreationPrice1h || 6.0,
    cacheReadPrice: pricing?.cacheReadPrice || 0.30,
    isDefault: pricing?.isDefault || false
  });
  const [errors, setErrors] = useState({});

  const handleChange = (field, value) => {
    setFormData(prev => ({ ...prev, [field]: value }));
    // 清除对应字段的错误
    if (errors[field]) {
      setErrors(prev => ({ ...prev, [field]: null }));
    }
  };

  const validate = () => {
    const newErrors = {};
    if (!formData.modelName.trim()) {
      newErrors.modelName = '模型名称不能为空';
    }
    if (formData.inputPrice < 0) {
      newErrors.inputPrice = '输入价格不能为负数';
    }
    if (formData.outputPrice < 0) {
      newErrors.outputPrice = '输出价格不能为负数';
    }
    setErrors(newErrors);
    return Object.keys(newErrors).length === 0;
  };

  const handleSubmit = async (e) => {
    e.preventDefault();
    if (!validate()) return;

    try {
      await onSave(formData);
    } catch (err) {
      setErrors({ submit: err.message });
    }
  };

  // 自动计算缓存价格建议 (按 Anthropic 官方定价规则)
  // 5分钟缓存: input × 1.25, 1小时缓存: input × 2.0, 读取: input × 0.1
  const suggestCachePrices = () => {
    const input = parseFloat(formData.inputPrice) || 0;
    setFormData(prev => ({
      ...prev,
      cacheCreationPrice5m: parseFloat((input * 1.25).toFixed(CACHE_PRICE_DECIMALS)),
      cacheCreationPrice1h: parseFloat((input * 2.0).toFixed(CACHE_PRICE_DECIMALS)),
      cacheReadPrice: parseFloat((input * 0.1).toFixed(CACHE_PRICE_DECIMALS))
    }));
  };

  return createPortal(
    <div className="fixed inset-0 z-[60] flex items-start justify-center px-4 pt-[15vh]">
      <div className="absolute inset-0 bg-overlay" />
      <div
        ref={dialogRef}
        tabIndex={-1}
        role="dialog"
        aria-modal="true"
        aria-label={isEdit ? '编辑模型定价' : '添加模型定价'}
        className="relative flex max-h-[75vh] w-full max-w-lg flex-col overflow-hidden rounded-2xl border border-line bg-surface shadow-2xl focus:outline-none"
      >
        <form onSubmit={handleSubmit} className="flex min-h-0 flex-1 flex-col">
          {/* 头部 */}
          <div className="flex items-center justify-between border-b border-line-soft px-6 py-4">
            <div>
              <h3 className="text-lg font-semibold text-fg">
                {isEdit ? '编辑模型定价' : '添加模型定价'}
              </h3>
              <p className="mt-0.5 text-xs text-fg-subtle">USD per 1M tokens</p>
            </div>
            <button
              type="button"
              onClick={() => {
                if (!loading) onCancel();
              }}
              disabled={loading}
              className="text-sm text-fg-subtle hover:text-fg-body"
            >
              关闭
            </button>
          </div>

          {/* 表单内容 */}
          <div className="flex-1 space-y-4 overflow-y-auto px-6 py-5">
            {/* 模型名称 */}
            <div>
              <label className="block text-sm font-medium text-fg-body mb-1.5">
                模型名称 <span className="text-danger">*</span>
              </label>
              <input
                type="text"
                value={formData.modelName}
                onChange={(e) => handleChange('modelName', e.target.value)}
                placeholder="例如: claude-sonnet-4-20250514"
                disabled={isEdit}
                className={`w-full px-3 py-2 border rounded-lg text-sm focus:outline-none focus:ring-2 focus:ring-accent-ring ${
                  errors.modelName ? 'border-danger-line' : 'border-line'
                } ${isEdit ? 'bg-surface-sub text-fg-muted' : ''}`}
              />
              {errors.modelName && (
                <p className="text-xs text-danger mt-1">{errors.modelName}</p>
              )}
            </div>

            {/* 显示名称 */}
            <div>
              <label className="block text-sm font-medium text-fg-body mb-1.5">
                显示名称
              </label>
              <input
                type="text"
                value={formData.displayName}
                onChange={(e) => handleChange('displayName', e.target.value)}
                placeholder="例如: Claude Sonnet 4"
                className="w-full px-3 py-2 border border-line rounded-lg text-sm focus:outline-none focus:ring-2 focus:ring-accent-ring"
              />
            </div>

            {/* 描述 */}
            <div>
              <label className="block text-sm font-medium text-fg-body mb-1.5">
                描述
              </label>
              <input
                type="text"
                value={formData.description}
                onChange={(e) => handleChange('description', e.target.value)}
                placeholder="模型描述"
                className="w-full px-3 py-2 border border-line rounded-lg text-sm focus:outline-none focus:ring-2 focus:ring-accent-ring"
              />
            </div>

            {/* 价格设置 */}
            <div className="bg-surface-sub rounded-xl p-4 space-y-4">
              <div className="flex items-center justify-between">
                <h4 className="text-sm font-semibold text-fg-body">价格设置</h4>
                <button
                  type="button"
                  onClick={suggestCachePrices}
                  className="text-xs text-accent hover:text-accent-fg flex items-center gap-1"
                >
                  <Sparkles size={12} />
                  自动计算缓存价格
                </button>
              </div>

              <div className="grid grid-cols-2 gap-4">
                {/* 输入价格 */}
                <div>
                  <label className="block text-xs font-medium text-fg-body mb-1">
                    输入价格
                  </label>
                  <div className="relative">
                    <span className="absolute left-3 top-1/2 -translate-y-1/2 text-fg-subtle text-sm">$</span>
                    <input
                      type="number"
                      step={PRICE_INPUT_STEP}
                      min="0"
                      value={formData.inputPrice}
                      onChange={(e) => handleChange('inputPrice', parseFloat(e.target.value) || 0)}
                      className={`w-full pl-7 pr-3 py-2 border rounded-lg text-sm focus:outline-none focus:ring-2 focus:ring-accent-ring ${
                        errors.inputPrice ? 'border-danger-line' : 'border-line'
                      }`}
                    />
                  </div>
                </div>

                {/* 输出价格 */}
                <div>
                  <label className="block text-xs font-medium text-fg-body mb-1">
                    输出价格
                  </label>
                  <div className="relative">
                    <span className="absolute left-3 top-1/2 -translate-y-1/2 text-fg-subtle text-sm">$</span>
                    <input
                      type="number"
                      step={PRICE_INPUT_STEP}
                      min="0"
                      value={formData.outputPrice}
                      onChange={(e) => handleChange('outputPrice', parseFloat(e.target.value) || 0)}
                      className={`w-full pl-7 pr-3 py-2 border rounded-lg text-sm focus:outline-none focus:ring-2 focus:ring-accent-ring ${
                        errors.outputPrice ? 'border-danger-line' : 'border-line'
                      }`}
                    />
                  </div>
                </div>

                {/* 5分钟缓存创建价格 */}
                <div>
                  <label className="block text-xs font-medium text-fg-body mb-1">
                    缓存创建 (5m)
                  </label>
                  <div className="relative">
                    <span className="absolute left-3 top-1/2 -translate-y-1/2 text-fg-subtle text-sm">$</span>
                    <input
                      type="number"
                      step={PRICE_INPUT_STEP}
                      min="0"
                      value={formData.cacheCreationPrice5m}
                      onChange={(e) => handleChange('cacheCreationPrice5m', parseFloat(e.target.value) || 0)}
                      className="w-full pl-7 pr-3 py-2 border border-line rounded-lg text-sm focus:outline-none focus:ring-2 focus:ring-accent-ring"
                    />
                  </div>
                  <span className="text-[10px] text-fg-subtle">input × 1.25</span>
                </div>

                {/* 1小时缓存创建价格 */}
                <div>
                  <label className="block text-xs font-medium text-fg-body mb-1">
                    缓存创建 (1h)
                  </label>
                  <div className="relative">
                    <span className="absolute left-3 top-1/2 -translate-y-1/2 text-fg-subtle text-sm">$</span>
                    <input
                      type="number"
                      step={PRICE_INPUT_STEP}
                      min="0"
                      value={formData.cacheCreationPrice1h}
                      onChange={(e) => handleChange('cacheCreationPrice1h', parseFloat(e.target.value) || 0)}
                      className="w-full pl-7 pr-3 py-2 border border-line rounded-lg text-sm focus:outline-none focus:ring-2 focus:ring-accent-ring"
                    />
                  </div>
                  <span className="text-[10px] text-fg-subtle">input × 2.0</span>
                </div>

                {/* 缓存读取价格 */}
                <div className="col-span-2">
                  <label className="block text-xs font-medium text-fg-body mb-1">
                    缓存读取价格
                  </label>
                  <div className="relative">
                    <span className="absolute left-3 top-1/2 -translate-y-1/2 text-fg-subtle text-sm">$</span>
                    <input
                      type="number"
                      step={PRICE_INPUT_STEP}
                      min="0"
                      value={formData.cacheReadPrice}
                      onChange={(e) => handleChange('cacheReadPrice', parseFloat(e.target.value) || 0)}
                      className="w-full pl-7 pr-3 py-2 border border-line rounded-lg text-sm focus:outline-none focus:ring-2 focus:ring-accent-ring"
                    />
                  </div>
                  <span className="text-[10px] text-fg-subtle">input × 0.1</span>
                </div>
              </div>
            </div>

            {/* 设为默认 */}
            <label className="flex items-center gap-3 p-3 bg-warn-soft rounded-lg border border-warn-line cursor-pointer hover:bg-warn-soft transition-colors">
              <input
                type="checkbox"
                checked={formData.isDefault}
                onChange={(e) => handleChange('isDefault', e.target.checked)}
                className="app-checkbox"
              />
              <div className="flex items-center gap-2">
                <Star size={16} className="text-warn" />
                <span className="text-sm text-warn font-medium">设为默认定价</span>
              </div>
              <span className="text-xs text-warn ml-auto">未知模型将使用此定价</span>
            </label>

            {/* 错误提示 */}
            {errors.submit && (
              <div className="p-3 bg-danger-soft border border-danger-line rounded-lg text-sm text-danger">
                {errors.submit}
              </div>
            )}
          </div>

          {/* 底部按钮 */}
          <div className="flex justify-end gap-3 border-t border-line-soft bg-surface px-6 py-4">
            <Button variant="ghost" type="button" onClick={onCancel} disabled={loading}>
              取消
            </Button>
            <Button type="submit" loading={loading}>
              {isEdit ? '保存修改' : '创建定价'}
            </Button>
          </div>
        </form>
      </div>
    </div>,
    document.body
  );
};

// ============================================
// 删除确认对话框
// ============================================

const DeleteConfirmDialog = ({ pricing, onConfirm, onCancel, loading }) => {
  // 挂载即打开（父组件条件渲染），卸载时由 hook 清理
  const dialogRef = useRef(null);
  useModalLifecycle({
    open: true,
    onClose: () => {
      if (!loading) onCancel();
    },
    initialFocusRef: dialogRef
  });

  return (
    <div className="fixed inset-0 bg-overlay flex items-start justify-center z-50 animate-fade-in pt-[20vh]">
      <div
        ref={dialogRef}
        tabIndex={-1}
        role="dialog"
        aria-modal="true"
        aria-label="确认删除模型定价"
        className="bg-surface rounded-2xl shadow-xl w-full max-w-md p-6 focus:outline-none"
      >
        <div className="flex items-center gap-3 mb-4">
          <div className="p-3 bg-danger-soft rounded-full">
            <AlertTriangle className="text-danger" size={24} />
          </div>
          <div>
            <h3 className="text-lg font-semibold text-fg">确认删除</h3>
            <p className="text-sm text-fg-muted">此操作不可撤销</p>
          </div>
        </div>

        <p className="text-fg-body mb-6">
          确定要删除模型定价 <span className="font-semibold">&ldquo;{pricing?.displayName || pricing?.modelName}&rdquo;</span> 吗？
          删除后将无法恢复。
        </p>

        <div className="flex justify-end gap-3">
          <Button variant="ghost" onClick={onCancel} disabled={loading}>
            取消
          </Button>
          <Button
            variant="danger"
            icon={Trash2}
            onClick={onConfirm}
            loading={loading}
          >
            确认删除
          </Button>
        </div>
      </div>
    </div>
  );
};

// ============================================
// 定价卡片组件
// ============================================

const PricingCard = ({ pricing, onEdit, onDelete, onSetDefault }) => {
  const { formatTimestamp } = useTimezone();
  const isDefault = pricing.isDefault;

  // 计算价格等级颜色
  const getPriceLevel = (price) => {
    if (price >= 10) return 'text-danger';
    if (price >= 3) return 'text-warn';
    return 'text-success';
  };

  return (
    <div className={`
      bg-surface rounded-xl border shadow-sm hover:shadow-md transition-all
      ${isDefault ? 'border-warn-line ring-2 ring-warn-line' : 'border-line'}
    `}>
      {/* 头部 */}
      <div className="px-5 py-4 border-b border-line-soft">
        <div className="flex items-start justify-between">
          <div className="flex-1 min-w-0">
            <div className="flex items-center gap-2">
              <h3 className="font-bold text-fg truncate">
                {pricing.displayName || pricing.modelName}
              </h3>
              {isDefault && (
                <span className="inline-flex items-center px-2 py-0.5 rounded-full text-[10px] font-medium bg-warn-soft text-warn border border-warn-line">
                  <Star size={10} className="mr-1" />
                  默认
                </span>
              )}
            </div>
            <p className="text-xs text-fg-subtle font-mono mt-1 truncate">
              {pricing.modelName}
            </p>
          </div>
          <div className="flex items-center gap-1 ml-2">
            {!isDefault && (
              <button
                onClick={() => onSetDefault(pricing.modelName)}
                className="p-1.5 text-fg-subtle hover:bg-warn-soft hover:text-warn rounded-md transition-colors"
                title="设为默认"
              >
                <Star size={14} />
              </button>
            )}
            <button
              onClick={() => onEdit(pricing)}
              className="p-1.5 text-fg-subtle hover:bg-surface-mut hover:text-accent rounded-md transition-colors"
              title="编辑"
            >
              <Pencil size={14} />
            </button>
            {!isDefault && (
              <button
                onClick={() => onDelete(pricing)}
                className="p-1.5 text-fg-subtle hover:bg-danger-soft hover:text-danger rounded-md transition-colors"
                title="删除"
              >
                <Trash2 size={14} />
              </button>
            )}
          </div>
        </div>
        {pricing.description && (
          <p className="text-xs text-fg-muted mt-2 line-clamp-2">{pricing.description}</p>
        )}
      </div>

      {/* 价格信息 */}
      <div className="p-4">
        <div className="grid grid-cols-2 gap-3">
          {/* 输入价格 */}
          <div className="bg-surface-sub rounded-lg p-3">
            <div className="flex items-center gap-1.5 text-xs text-fg-muted mb-1">
              <TrendingDown size={12} />
              输入
            </div>
            <div className={`text-lg font-bold ${getPriceLevel(pricing.inputPrice)}`}>
              ${pricing.inputPrice}
            </div>
          </div>

          {/* 输出价格 */}
          <div className="bg-surface-sub rounded-lg p-3">
            <div className="flex items-center gap-1.5 text-xs text-fg-muted mb-1">
              <TrendingUp size={12} />
              输出
            </div>
            <div className={`text-lg font-bold ${getPriceLevel(pricing.outputPrice)}`}>
              ${pricing.outputPrice}
            </div>
          </div>

          {/* 缓存创建 5m */}
          <div className="bg-accent-soft rounded-lg p-3">
            <div className="text-xs text-fg-muted mb-1">缓存创建 (5m)</div>
            <div className="text-sm font-semibold text-accent">
              ${pricing.cacheCreationPrice5m}
            </div>
          </div>

          {/* 缓存创建 1h */}
          <div className="bg-accent-soft rounded-lg p-3">
            <div className="text-xs text-fg-muted mb-1">缓存创建 (1h)</div>
            <div className="text-sm font-semibold text-accent-fg">
              ${pricing.cacheCreationPrice1h}
            </div>
          </div>

          {/* 缓存读取 */}
          <div className="col-span-2 bg-success-soft rounded-lg p-3">
            <div className="text-xs text-fg-muted mb-1">缓存读取</div>
            <div className="text-sm font-semibold text-success">
              ${pricing.cacheReadPrice}
            </div>
          </div>
        </div>
      </div>

      {/* 底部信息 */}
      <div className="px-4 py-2 border-t border-line-soft text-xs text-fg-subtle">
        更新于 {pricing.updatedAt ? formatTimestamp(pricing.updatedAt) : '-'}
      </div>
    </div>
  );
};

// ============================================
// Pricing 页面
// ============================================

const PricingPage = () => {
  // 数据状态
  const [storageStatus, setStorageStatus] = useState(null);
  const [pricings, setPricings] = useState([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState(null);

  // 表单状态
  const [showForm, setShowForm] = useState(false);
  const [editingPricing, setEditingPricing] = useState(null);
  const [formLoading, setFormLoading] = useState(false);

  // 删除确认状态
  const [deleteTarget, setDeleteTarget] = useState(null);
  const [deleteLoading, setDeleteLoading] = useState(false);

  // 加载数据
  const loadData = useCallback(async () => {
    setLoading(true);
    setError(null);
    try {
      const [status, records] = await Promise.all([
        getModelPricingStorageStatus(),
        getModelPricings()
      ]);
      setStorageStatus(status);
      setPricings(records);
    } catch (err) {
      console.error('加载模型定价失败:', err);
      setError(err.message);
    } finally {
      setLoading(false);
    }
  }, []);

  // 初始化加载
  useEffect(() => {
    loadData();
  }, [loadData]);

  // 新建定价
  const handleCreate = () => {
    setEditingPricing(null);
    setShowForm(true);
  };

  // 编辑定价
  const handleEdit = (pricing) => {
    setEditingPricing(pricing);
    setShowForm(true);
  };

  // 删除定价
  const handleDelete = (pricing) => {
    setDeleteTarget(pricing);
  };

  // 设为默认
  const handleSetDefault = async (modelName) => {
    try {
      await setDefaultModelPricing(modelName);
      await loadData();
    } catch (err) {
      console.error('设置默认定价失败:', err);
      alert(`操作失败: ${err.message}`);
    }
  };

  // 保存定价
  const handleSave = async (formData) => {
    setFormLoading(true);
    try {
      if (editingPricing) {
        await updateModelPricing(editingPricing.modelName, formData);
      } else {
        await createModelPricing(formData);
      }
      setShowForm(false);
      setEditingPricing(null);
      await loadData();
    } catch (err) {
      console.error('保存失败:', err);
      throw err;
    } finally {
      setFormLoading(false);
    }
  };

  // 确认删除
  const handleConfirmDelete = async () => {
    if (!deleteTarget) return;

    setDeleteLoading(true);
    try {
      await deleteModelPricing(deleteTarget.modelName);
      setDeleteTarget(null);
      await loadData();
    } catch (err) {
      console.error('删除失败:', err);
      alert(`删除失败: ${err.message}`);
    } finally {
      setDeleteLoading(false);
    }
  };

  // 存储未启用
  if (!loading && storageStatus && !storageStatus.enabled) {
    return (
      <div className="motion-page">
        <div className="flex flex-col items-center justify-center py-20">
          <div className="p-4 bg-surface-mut rounded-full mb-4">
            <Database size={40} className="text-fg-subtle" />
          </div>
          <h3 className="text-lg font-semibold text-fg-body mb-2">模型定价服务未启用</h3>
          <p className="text-sm text-fg-muted max-w-md text-center mb-4">
            请在配置文件中启用 <code className="bg-surface-mut px-1.5 py-0.5 rounded text-xs">usage_tracking.enabled: true</code>
          </p>
        </div>
      </div>
    );
  }

  // 错误状态
  if (error) {
    return (
      <ErrorMessage
        title="数据加载失败"
        message={error}
        onRetry={loadData}
      />
    );
  }

  // 加载状态
  if (loading && pricings.length === 0) {
    return <LoadingSpinner text="加载模型定价..." />;
  }

  // 统计数据
  const defaultPricing = pricings.find(p => p.isDefault);
  const avgInputPrice = pricings.length > 0
    ? (pricings.reduce((sum, p) => sum + p.inputPrice, 0) / pricings.length).toFixed(2)
    : 0;
  const avgOutputPrice = pricings.length > 0
    ? (pricings.reduce((sum, p) => sum + p.outputPrice, 0) / pricings.length).toFixed(2)
    : 0;

  return (
    <div className="motion-page">
      {/* 页面标题 */}
      <div className="flex justify-between items-end mb-8">
        <div>
          <h1 className="text-2xl font-bold text-fg">Model Pricing</h1>
          <p className="text-fg-muted text-sm mt-1">
            管理 Claude 模型的基础定价配置 (USD per 1M tokens)
          </p>
        </div>
        <div className="flex items-center gap-3">
          {/* 存储状态 - 已隐藏 */}
          {/* <div className="flex items-center gap-2 px-3 py-1.5 rounded-lg text-xs font-medium bg-accent-soft text-accent-fg border border-accent-line">
            <Database size={14} />
            SQLite 存储
            <span className="text-accent">({storageStatus?.totalCount || 0} 条)</span>
          </div> */}

          {/* 刷新按钮 */}
          <Button
            variant="ghost"
            size="sm"
            icon={RefreshCw}
            onClick={loadData}
            loading={loading}
          >
            刷新
          </Button>

          {/* 添加按钮 */}
          <Button icon={Plus} onClick={handleCreate}>
            添加定价
          </Button>
        </div>
      </div>

      {/* 统计卡片 */}
      <div className="grid grid-cols-4 gap-4 mb-6">
        <div className="bg-surface rounded-xl border border-line p-4 shadow-sm">
          <div className="text-2xl font-bold text-fg">{pricings.length}</div>
          <div className="text-sm text-fg-muted">定价配置数</div>
        </div>
        <div className="bg-surface rounded-xl border border-warn-line p-4 shadow-sm">
          <div className="text-2xl font-bold text-warn truncate">
            {defaultPricing?.displayName || defaultPricing?.modelName || '-'}
          </div>
          <div className="text-sm text-fg-muted">默认定价模型</div>
        </div>
        <div className="bg-surface rounded-xl border border-accent-line p-4 shadow-sm">
          <div className="text-2xl font-bold text-accent">${avgInputPrice}</div>
          <div className="text-sm text-fg-muted">平均输入价格</div>
        </div>
        <div className="bg-surface rounded-xl border border-success-line p-4 shadow-sm">
          <div className="text-2xl font-bold text-success">${avgOutputPrice}</div>
          <div className="text-sm text-fg-muted">平均输出价格</div>
        </div>
      </div>

      {/* 定价卡片网格 */}
      {pricings.length === 0 ? (
        <div className="bg-surface rounded-2xl border border-line shadow-sm p-12 text-center">
          <div className="flex flex-col items-center gap-3">
            <Calculator size={40} className="text-fg-subtle" />
            <p className="text-fg-muted">暂无定价配置</p>
            <Button icon={Plus} onClick={handleCreate}>
              添加第一个定价
            </Button>
          </div>
        </div>
      ) : (
        <div className="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-3 gap-4">
          {pricings.map((pricing) => (
            <PricingCard
              key={pricing.modelName}
              pricing={pricing}
              onEdit={handleEdit}
              onDelete={handleDelete}
              onSetDefault={handleSetDefault}
            />
          ))}
        </div>
      )}

      {/* 表单弹窗 */}
      {showForm && (
        <PricingForm
          pricing={editingPricing}
          onSave={handleSave}
          onCancel={() => {
            setShowForm(false);
            setEditingPricing(null);
          }}
          loading={formLoading}
        />
      )}

      {/* 删除确认弹窗 */}
      {deleteTarget && (
        <DeleteConfirmDialog
          pricing={deleteTarget}
          onConfirm={handleConfirmDelete}
          onCancel={() => setDeleteTarget(null)}
          loading={deleteLoading}
        />
      )}
    </div>
  );
};

export default PricingPage;
