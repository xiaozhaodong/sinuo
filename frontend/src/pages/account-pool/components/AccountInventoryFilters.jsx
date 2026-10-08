// ============================================
// 账号资产筛选条
// 2026-03-21
// 2026-10-07 桌面化：工具栏下方的范围条（24px 控件、浅灰底），批量操作条同样压紧；
//            筛选从「外置标签 + 下拉框」改为标签写在按钮里的弹出按钮，生效中的条件高亮
// ============================================

import { ChevronDown, RotateCcw, Search, Sparkles, TestTube2, UserRoundCheck, Rows3 } from 'lucide-react';

const BATCH_LABELS = {
  test: '批量测试',
  refresh: '批量刷新画像',
  enable: '批量启用',
  disable: '批量停用',
  primary: '移到主组',
  backup: '移到备组',
  cold: '移到冷备'
};

const findAction = (actions = [], ...keys) => actions.find((a) => keys.includes(a.key));
const findVariant = (action, key) => action?.variants?.find((v) => v.key === key);

const FILTER_SHORT_LABELS = {
  auth: '授权',
  plan: '计划',
  group: '组别',
  status: '状态',
  risk: '风险',
  sort: '排序'
};

// 默认值：排序取第一项，其余条件为「全部」
const defaultValueOf = (config) => (config.key === 'sort' ? config.options?.[0]?.value : 'all');

// 弹出按钮：默认值只显示标签「授权 ▾」，生效时显示取值并高亮「授权 OAuth ▾」，一眼看出哪些条件在生效；
// 选项走锚定菜单。排序不显示标签，始终显示当前排序方式
const FilterField = ({ config, value, onChange, onOpenMenu, showLabel = true }) => {
  const options = config.options || [];
  const current = options.find((option) => String(option.value) === String(value)) || options[0];
  const active = String(current?.value ?? '') !== String(defaultValueOf(config));
  const openMenu = (event) => onOpenMenu?.(event.currentTarget, options.map((option) => ({
    label: option.label,
    checked: String(option.value) === String(current?.value),
    onSelect: () => onChange?.(option.value)
  })));

  return (
    <button
      type="button"
      aria-haspopup="menu"
      onClick={openMenu}
      title={`${config.label}：${current?.label || '-'}`}
      className={`inline-flex h-6 shrink-0 items-center gap-1 rounded-md border px-2 text-xs shadow-xs transition-colors ${
        active ? 'border-accent-line bg-accent-soft text-accent-fg' : 'border-line bg-surface text-fg-body hover:bg-surface-sub'
      }`}
    >
      {showLabel && <span className={active ? 'opacity-70' : ''}>{FILTER_SHORT_LABELS[config.key] || config.label}</span>}
      {(active || !showLabel) && <span className="max-w-[96px] truncate">{current?.label || '-'}</span>}
      <ChevronDown size={12} className="shrink-0 opacity-60" />
    </button>
  );
};

const BatchActionButton = ({ icon: Icon, label, onClick, disabled, title, className = '' }) => (
  <button
    type="button"
    onClick={onClick}
    disabled={disabled}
    title={title}
    className={`inline-flex h-6 items-center rounded-md border border-line bg-surface px-2 text-xs text-fg-body shadow-xs transition-colors hover:bg-surface-sub disabled:opacity-40 ${className}`}
  >
    {Icon && <Icon size={12} className="mr-1" />}
    {label}
  </button>
);

const AccountInventoryFilters = ({
  searchTerm = '',
  onSearchTermChange,
  filterConfigs = [],
  filterValues = {},
  onFilterChange,
  onResetFilters,
  resultCount = 0,
  selectedCount = 0,
  batchActions = [],
  batchFeedback,
  onBatchAction,
  onOpenMenu,
  viewSwitcher = null
}) => {
  const testAction = findAction(batchActions, 'batch-test', 'test');
  const refreshAction = findAction(batchActions, 'batch-refresh-profile', 'refresh-profile');
  const toggleAction = findAction(batchActions, 'batch-toggle', 'toggle-enabled');
  const moveAction = findAction(batchActions, 'batch-move-tier', 'move-backup', 'move-cold-standby');
  const enableVariant = findVariant(toggleAction, 'enable');
  const disableVariant = findVariant(toggleAction, 'disable');
  const primaryVariant = findVariant(moveAction, 'primary');
  const backupVariant = findVariant(moveAction, 'backup');
  const coldVariant = findVariant(moveAction, 'cold');
  const sortConfig = filterConfigs.find((config) => config.key === 'sort');
  const queryConfigs = filterConfigs.filter((config) => config.key !== 'sort');
  const hasActiveFilters = Boolean(String(searchTerm || '').trim()) || filterConfigs.some((config) => {
    const defaultValue = defaultValueOf(config);
    return String(filterValues[config.key] ?? defaultValue) !== String(defaultValue);
  });

  return (
    <div className="shrink-0 border-b border-line bg-inspector px-3 py-1.5">
      {/* 单行工具栏：查询条件与结果工具共用一条基线；窄屏时按组换行 */}
      <div className="flex flex-wrap items-center gap-x-3 gap-y-2">
        <div className="relative w-40 shrink-0">
          <Search size={13} className="pointer-events-none absolute left-2 top-1/2 -translate-y-1/2 text-fg-subtle" />
          <input
            id="account-inventory-search"
            type="search"
            aria-label="搜索账号"
            value={searchTerm}
            placeholder="搜索账号名 / 备注 / 标签"
            onChange={(event) => onSearchTermChange?.(event.target.value)}
            className="desk-field w-full pl-7 pr-10 text-xs placeholder:text-fg-subtle"
          />
          <span
            className="pointer-events-none absolute right-1.5 top-1/2 -translate-y-1/2 rounded-full bg-surface-mut px-1.5 text-[10px] font-medium leading-4 tabular-nums text-fg-muted"
            title={`当前筛选结果：${resultCount} 个账号`}
          >
            {resultCount}
          </span>
        </div>

        <div className="flex shrink-0 items-center gap-1">
          {queryConfigs.map((config) => (
            <FilterField
              key={config.key}
              config={config}
              value={filterValues[config.key]}
              onChange={(value) => onFilterChange?.(config.key, value)}
              onOpenMenu={onOpenMenu}
            />
          ))}
        </div>

        <div className="ml-auto flex shrink-0 items-center gap-1.5 text-xs text-fg-subtle">
          <span className="hidden h-4 w-px bg-line lg:block" />
          {sortConfig ? (
            <FilterField
              config={sortConfig}
              value={filterValues[sortConfig.key]}
              onChange={(value) => onFilterChange?.(sortConfig.key, value)}
              onOpenMenu={onOpenMenu}
              showLabel={false}
            />
          ) : null}
          <span className="hidden h-4 w-px bg-line sm:block" />
          {viewSwitcher}
          <button
            type="button"
            onClick={onResetFilters}
            disabled={!hasActiveFilters}
            className={`inline-flex h-6 items-center gap-1 rounded-md px-1.5 text-xs transition-colors ${
              hasActiveFilters
                ? 'text-fg-muted hover:bg-surface-mut hover:text-fg-body'
                : 'text-fg-subtle'
            }`}
            title={hasActiveFilters ? '重置搜索、筛选与排序' : '当前没有需要重置的条件'}
            aria-label="重置搜索、筛选与排序"
          >
            <RotateCcw size={12} />
          </button>
        </div>
      </div>

      {/* 批量操作 - 选中时显示 */}
      {selectedCount > 0 && (
        <div className="mt-1.5 flex items-center gap-2 rounded-md border border-accent-line bg-accent-soft/40 px-2 py-1">
          <span className="shrink-0 text-xs font-medium text-accent">
            {selectedCount} 个已选
          </span>
          <span className="h-4 w-px bg-accent-line" />
          <div className="flex flex-wrap items-center gap-1.5">
            <BatchActionButton
              icon={TestTube2}
              label={testAction?.label || BATCH_LABELS.test}
              onClick={() => onBatchAction?.(testAction || { key: 'batch-test', label: BATCH_LABELS.test })}
            />
            <BatchActionButton
              icon={Sparkles}
              label={refreshAction?.label || BATCH_LABELS.refresh}
              onClick={() => onBatchAction?.(refreshAction || { key: 'batch-refresh-profile', label: BATCH_LABELS.refresh })}
            />
            <BatchActionButton
              icon={UserRoundCheck}
              label={enableVariant?.label || BATCH_LABELS.enable}
              onClick={() => onBatchAction?.(toggleAction || { key: 'batch-toggle' }, enableVariant || { key: 'enable', label: BATCH_LABELS.enable })}
            />
            <BatchActionButton
              icon={Rows3}
              label={disableVariant?.label || BATCH_LABELS.disable}
              onClick={() => onBatchAction?.(toggleAction || { key: 'batch-toggle' }, disableVariant || { key: 'disable', label: BATCH_LABELS.disable })}
            />
            <span className="h-4 w-px bg-surface-emph" />
            <BatchActionButton
              label={primaryVariant?.label || BATCH_LABELS.primary}
              onClick={() => onBatchAction?.(moveAction || { key: 'batch-move-tier' }, primaryVariant || { key: 'primary', label: BATCH_LABELS.primary })}
            />
            <BatchActionButton
              label={backupVariant?.label || BATCH_LABELS.backup}
              onClick={() => onBatchAction?.(moveAction || { key: 'batch-move-tier' }, backupVariant || { key: 'backup', label: BATCH_LABELS.backup })}
            />
            <BatchActionButton
              label={coldVariant?.label || BATCH_LABELS.cold}
              onClick={() => onBatchAction?.(moveAction || { key: 'batch-move-tier' }, coldVariant || { key: 'cold', label: BATCH_LABELS.cold })}
              disabled={Boolean(coldVariant?.disabled)}
              title={coldVariant?.soon ? '即将支持' : ''}
            />
          </div>
          {batchFeedback?.message && (
            <span className="ml-auto shrink-0 text-xs text-fg-muted">{batchFeedback.message}</span>
          )}
        </div>
      )}
    </div>
  );
};

export default AccountInventoryFilters;
