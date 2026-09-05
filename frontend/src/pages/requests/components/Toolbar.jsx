// ============================================
// Toolbar - 页面工具栏
// 2025-12-06 10:40:38 v4.0: 简化端点切换（移除 Token 级联）
// ============================================

import { Filter, Settings2, ChevronDown } from 'lucide-react';
import { TIME_RANGE_OPTIONS } from '../utils/constants.js';
import ViewConfigPanel from './ViewConfigPanel.jsx';
import AutoRefreshControl from './AutoRefreshControl.jsx';
import ClaudeEndpointSwitcher from './ClaudeEndpointSwitcher.jsx';
import AccountPoolSwitcher from './AccountPoolSwitcher.jsx';

/**
 * Toolbar - 请求追踪页面工具栏
 * @param {Object} props
 * @param {string} props.activeTimeRange - 当前激活的时间范围
 * @param {Function} props.onTimeRangeChange - 时间范围变更回调
 * @param {boolean} props.isFilterOpen - 筛选面板是否打开
 * @param {Function} props.onFilterToggle - 筛选面板切换回调
 * @param {boolean} props.isViewConfigOpen - 列配置面板是否打开
 * @param {Function} props.onViewConfigToggle - 列配置面板切换回调
 * @param {Function} props.onRefresh - 刷新回调
 * @param {boolean} props.refreshing - 是否正在刷新（手动刷新期间按钮转圈）
 * @param {Array} props.columns - 所有列配置
 * @param {Array} props.visibleColumns - 当前可见的列ID数组
 * @param {Function} props.onToggleColumn - 切换列显示回调
 * @param {Function} props.onResetColumns - 重置列配置回调
 * @param {Object} props.autoRefresh - 实时刷新状态
 * @param {Array} props.endpoints - Claude 端点列表
 * @param {Function} props.onClaudeEndpointSwitch - Claude 端点路由切换回调
 * @param {Array} props.accounts - 账号池账号列表
 * @param {Object|null} props.activeAccount - 当前手动固定账号
 * @param {string|number|null} props.recentSelectedAccountId - 最近一次调度命中的账号 ID
 * @param {Function} props.onAccountSwitch - 账号池切换回调 (account) => void
 * @param {Function} props.onEnableAutoSelection - 切回按编排自动调度
 * @param {boolean} props.accountSwitching - 是否正在切换账号
 */
const Toolbar = ({
  activeTimeRange = 'today',
  onTimeRangeChange,
  isFilterOpen = false,
  onFilterToggle,
  isViewConfigOpen = false,
  onViewConfigToggle,
  onRefresh,
  refreshing = false,
  columns = [],
  visibleColumns = [],
  onToggleColumn,
  onResetColumns,
  autoRefresh = null,
  endpoints = [],
  claudeRoutingState = null,
  modelRoutingEnabled = false,
  modelRouteBindings = [],
  onModelRoutePatch,
  modelRoutePatching = false,
  onClaudeEndpointSwitch,
  onRestoreClaudeAuto,
  onClearRouteCache,
  routeSwitching = false,
  accounts = [],
  activeAccount = null,
  recentSelectedAccountId = null,
  onAccountSwitch,
  onEnableAutoSelection,
  accountSwitching = false
}) => {
  return (
    <div className="flex flex-nowrap items-center justify-end gap-2 xl:gap-3 min-w-0">
      {/* 端点快捷切换器 */}
      <ClaudeEndpointSwitcher
        endpoints={endpoints}
        routingState={claudeRoutingState}
        modelRoutingEnabled={modelRoutingEnabled}
        modelRouteBindings={modelRouteBindings}
        onModelRoutePatch={onModelRoutePatch}
        modelRoutePatching={modelRoutePatching}
        onSwitch={onClaudeEndpointSwitch}
        onRestoreAuto={onRestoreClaudeAuto}
        onClearRouteCache={onClearRouteCache}
        loading={routeSwitching}
      />

      <AccountPoolSwitcher
        accounts={accounts}
        activeAccount={activeAccount}
        recentSelectedAccountId={recentSelectedAccountId}
        onSwitch={onAccountSwitch}
        onSwitchAuto={onEnableAutoSelection}
        loading={accountSwitching}
      />

      <div className="h-6 w-px bg-line-strong mx-1 hidden xl:block"></div>

      {/* 快捷时间范围选择 */}
      <div className="hidden sm:flex shrink-0 items-center bg-surface border border-line rounded-lg p-1 shadow-sm">
        {TIME_RANGE_OPTIONS.map((range) => (
          <button
            key={range.value}
            onClick={() => onTimeRangeChange?.(range.value)}
            className={`px-2 xl:px-3 py-1.5 text-xs font-medium rounded-md transition-all ${
              activeTimeRange === range.value
                ? 'bg-accent-soft text-accent-fg shadow-sm'
                : 'text-fg-muted hover:text-fg hover:bg-surface-sub'
            }`}
          >
            {range.label}
          </button>
        ))}
      </div>

      <div className="h-6 w-px bg-line-strong mx-1 hidden xl:block"></div>

      {/* 筛选按钮 */}
      <button
        onClick={onFilterToggle}
        className={`flex items-center gap-2 px-3 py-2 rounded-lg text-sm font-medium transition-all shadow-sm border ${
          isFilterOpen
            ? 'bg-accent-soft text-accent-fg border-accent-line ring-2 ring-accent-soft'
            : 'bg-surface text-fg-body border-line hover:border-accent-line hover:text-accent'
        }`}
      >
        <Filter className="w-4 h-4" /> <span className="hidden xl:inline">筛选</span><span className="xl:hidden">筛</span>
        <ChevronDown
          className={`w-3.5 h-3.5 transition-transform ${isFilterOpen ? 'rotate-180' : ''}`}
        />
      </button>

      <div className="h-6 w-px bg-line-strong mx-1 hidden xl:block"></div>

      {/* 列配置按钮 */}
      <div className="relative">
        <button
          onClick={onViewConfigToggle}
          className={`flex items-center gap-2 px-3 py-2 rounded-lg text-sm transition-all shadow-sm border ${
            isViewConfigOpen
              ? 'bg-accent-soft text-accent-fg border-accent-line ring-2 ring-accent-soft'
              : 'bg-surface text-fg-body border-line hover:bg-surface-sub hover:text-accent hover:border-accent-line'
          }`}
          title="自定义显示列"
        >
          <Settings2 className="w-4 h-4" />
          <span className="hidden xl:inline font-medium">显示</span><span className="xl:hidden font-medium">列</span>
        </button>

        {/* 列配置面板 */}
        <ViewConfigPanel
          isOpen={isViewConfigOpen}
          onClose={() => onViewConfigToggle?.(false)}
          columns={columns}
          visibleColumns={visibleColumns}
          onToggleColumn={onToggleColumn}
          onReset={onResetColumns}
        />
      </div>

      {/* 实时刷新状态（包含手动刷新） */}
      {autoRefresh && (
        <AutoRefreshControl
          mode={autoRefresh.mode}
          fallbackInterval={autoRefresh.fallbackInterval}
          onManualRefresh={onRefresh}
          refreshing={refreshing}
        />
      )}
    </div>
  );
};

export default Toolbar;
