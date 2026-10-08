// ============================================
// Toolbar - 页面工具栏
// 2025-12-06 10:40:38 v4.0: 简化端点切换（移除 Token 级联）
// 2026-10-07 桌面化：时间范围改分段控件；漏斗按钮只负责显示 / 隐藏筛选条（隐藏时有生效条件就带数字）；
//            「显示」改为列图标按钮
// ============================================

import { Columns3, Funnel, PanelBottom, RefreshCw } from 'lucide-react';
import { ToolbarButton, ToolbarSeparator } from '@components/layout/ToolbarButton.jsx';
import SegmentedControl from '@components/desktop/SegmentedControl.jsx';
import { TIME_RANGE_OPTIONS } from '../utils/constants.js';
import ClaudeAccessSwitcher from './ClaudeAccessSwitcher.jsx';
import CodexAccessSwitcher from './CodexAccessSwitcher.jsx';

/**
 * Toolbar - 请求追踪页面工具栏
 * @param {Object} props
 * @param {string} props.activeTimeRange - 当前激活的时间范围
 * @param {Function} props.onTimeRangeChange - 时间范围变更回调
 * @param {boolean} props.filterBarVisible - 筛选条是否显示
 * @param {Function} props.onFilterBarToggle - 显示 / 隐藏筛选条
 * @param {number} props.activeFilterCount - 生效中的筛选条件数（筛选条隐藏时显示在漏斗上）
 * @param {Function} props.onOpenColumnsMenu - 打开显示列菜单 (event) => void
 * @param {Function} props.onRefresh - 刷新回调
 * @param {boolean} props.refreshing - 是否正在刷新（手动刷新期间按钮转圈）
 * @param {Array} props.endpoints - Claude 端点列表
 * @param {Function} props.onClaudeEndpointSwitch - Claude 端点路由切换回调
 * @param {Array} props.accounts - 账号池账号列表
 * @param {Object|null} props.activeAccount - 当前手动固定账号
 * @param {string|number|null} props.recentSelectedAccountId - 最近一次调度命中的账号 ID
 * @param {Function} props.onAccountSwitch - 账号池切换回调 (account) => void
 * @param {Function} props.onEnableAutoSelection - 切回按编排自动调度
 * @param {boolean} props.accountSwitching - 是否正在切换账号
 * @param {boolean} props.detailPaneVisible - 底部详情面板是否显示
 * @param {Function} props.onDetailPaneToggle - 显示 / 隐藏详情面板
 */
const Toolbar = ({
  activeTimeRange = 'today',
  onTimeRangeChange,
  filterBarVisible = true,
  onFilterBarToggle,
  activeFilterCount = 0,
  onOpenColumnsMenu,
  onRefresh,
  refreshing = false,
  endpoints = [],
  claudeRoutingState = null,
  modelRoutingEnabled = false,
  codexModelRoutingEnabled = false,
  modelRouteBindings = [],
  onModelRoutePatch,
  modelRoutePatching = false,
  codexModelRouteBindings = [],
  onCodexModelRoutePatch,
  codexModelRoutePatching = false,
  onClaudeEndpointSwitch,
  onRestoreClaudeAuto,
  onClearRouteCache,
  onOpenClaudeModelRouting,
  onOpenCodexModelRouting,
  onNavigateToCodexAccess,
  routeSwitching = false,
  accounts = [],
  activeAccount = null,
  recentSelectedAccountId = null,
  onAccountSwitch,
  onEnableAutoSelection,
  accountSwitching = false,
  detailPaneVisible = true,
  onDetailPaneToggle
}) => {
  return (
    <div className="flex flex-nowrap items-center justify-end gap-1.5 min-w-0">
      {/* Claude 接入与端点策略快捷切换（入口未启用时即端点切换器） */}
      <ClaudeAccessSwitcher
        endpoints={endpoints}
        routingState={claudeRoutingState}
        modelRoutingEnabled={modelRoutingEnabled}
        modelRouteBindings={modelRouteBindings}
        onModelRoutePatch={onModelRoutePatch}
        modelRoutePatching={modelRoutePatching}
        onSwitch={onClaudeEndpointSwitch}
        onRestoreAuto={onRestoreClaudeAuto}
        onClearRouteCache={onClearRouteCache}
        onOpenModelRouting={onOpenClaudeModelRouting}
        loading={routeSwitching}
      />

      <CodexAccessSwitcher
        codexModelRoutingEnabled={codexModelRoutingEnabled}
        bindings={codexModelRouteBindings}
        accounts={accounts}
        onPatch={onCodexModelRoutePatch}
        modelRoutePatching={codexModelRoutePatching}
        activeAccount={activeAccount}
        recentSelectedAccountId={recentSelectedAccountId}
        onSwitch={onAccountSwitch}
        onSwitchAuto={onEnableAutoSelection}
        accountSwitching={accountSwitching}
        onOpenModelRouting={onOpenCodexModelRouting}
        onNavigateToCodexAccess={onNavigateToCodexAccess}
      />

      <ToolbarSeparator />

      {/* 快捷时间范围：自定义区间（筛选条的「时间」）时三个都不高亮 */}
      <SegmentedControl
        ariaLabel="时间范围"
        options={TIME_RANGE_OPTIONS}
        value={activeTimeRange}
        onChange={(value) => onTimeRangeChange?.(value)}
      />

      <ToolbarSeparator />

      <span className="relative inline-flex shrink-0">
        <ToolbarButton
          icon={Funnel}
          title={activeFilterCount > 0 && !filterBarVisible ? `显示筛选条（${activeFilterCount} 个条件生效中）` : '显示 / 隐藏筛选条'}
          pressed={filterBarVisible}
          onClick={onFilterBarToggle}
        />
        {activeFilterCount > 0 && !filterBarVisible && (
          <span className="pointer-events-none absolute -right-0.5 -top-0.5 grid h-[14px] min-w-[14px] place-items-center rounded-full bg-accent-ring px-0.5 text-[9.5px] font-semibold text-white">
            {activeFilterCount}
          </span>
        )}
      </span>

      {/* 列配置 */}
      <ToolbarButton icon={Columns3} title="显示列" onClick={onOpenColumnsMenu} />

      <ToolbarButton icon={RefreshCw} title="立即刷新" loading={refreshing} onClick={onRefresh} />

      {onDetailPaneToggle && (
        <ToolbarButton icon={PanelBottom} title="显示 / 隐藏详情面板" pressed={detailPaneVisible} onClick={onDetailPaneToggle} />
      )}
    </div>
  );
};

export default Toolbar;
