// 路由目标行（端点 / 账号），方案 A：当前目标打勾并显示「优选 / 固定」标签，
// 悬停或键盘聚焦时换成两个按钮，点一次写一次。
// 2026-10-07 桌面化：浮层 26px 行；按钮区与标签叠在同一网格格子里，宽度由按钮撑开（每行都渲染按钮，各行右侧对齐），悬停时不挤动左侧文字
import { LockKeyhole, Route } from 'lucide-react';
import { CheckSlot } from '@components/desktop/PopoverParts.jsx';
import RouteModeTag from './RouteModeTag.jsx';

const ACTIONS = {
  manual_preferred: { label: '优选', Icon: Route, hover: 'hover:border-accent-line hover:text-accent', active: 'border-accent-line bg-accent-soft text-accent' },
  manual_fixed: { label: '固定', Icon: LockKeyhole, hover: 'hover:border-danger-line hover:text-danger', active: 'border-danger-line bg-danger-soft text-danger' }
};

const RouteTargetRow = ({
  target,
  mode = 'auto',
  isCurrent = false,
  flash = false,
  onCommit,
  loading = false,
  indent = false,
  preferredDisabled = false,
  preferredTitle = '优先用此目标，不可用时回落',
  fixedTitle = '只用此目标，不可用时直接失败'
}) => {
  const currentMode = isCurrent ? mode : '';
  const showPriority = target.priority !== undefined && target.priority !== null && !target.statusLabel;
  return (
    <div className={`group flex h-[26px] items-center gap-1.5 rounded-[5px] pr-1.5 hover:bg-surface-mut has-[:focus-visible]:bg-surface-mut ${indent ? 'pl-[22px]' : 'pl-1.5'}`}>
      <CheckSlot checked={isCurrent} />
      <span className={`h-[7px] w-[7px] shrink-0 rounded-full ${target.indicatorClass || 'bg-fg-subtle/60'}`} />
      <span className={`min-w-0 flex-1 truncate transition-colors duration-700 ${isCurrent ? 'font-medium' : ''} ${flash ? 'text-accent' : isCurrent ? 'text-fg' : 'text-fg-body'}`} title={target.title || (typeof target.name === 'string' ? target.name : undefined)}>{target.name}</span>
      {target.lastHit && <span className="shrink-0 text-[11px] text-fg-subtle">最近命中</span>}
      {target.statusLabel && <span className={`shrink-0 text-[11px] ${target.statusClass || 'text-fg-subtle'}`}>{target.statusLabel}</span>}
      {showPriority && <span className="shrink-0 font-mono text-[11px] text-fg-subtle">P{target.priority}</span>}
      <div className="grid h-5 shrink-0 items-center justify-items-end">
        {currentMode && <span className="pointer-events-none col-start-1 row-start-1 transition-opacity group-hover:opacity-0 group-has-[:focus-visible]:opacity-0"><RouteModeTag mode={currentMode} /></span>}
        <div className="col-start-1 row-start-1 whitespace-nowrap flex gap-1 opacity-0 transition-opacity group-hover:opacity-100 group-has-[:focus-visible]:opacity-100">
          {Object.entries(ACTIONS).map(([actionMode, action]) => {
            const { Icon } = action;
            const disabled = loading || (actionMode === 'manual_preferred' && preferredDisabled);
            return (
              <button
                key={actionMode}
                type="button"
                disabled={disabled}
                title={actionMode === 'manual_preferred' ? preferredTitle : fixedTitle}
                onClick={() => onCommit?.({ route_mode: actionMode, route_target: target.value ?? target.name })}
                className={`inline-flex h-5 shrink-0 whitespace-nowrap items-center gap-0.5 rounded border px-1.5 text-[11px] disabled:opacity-50 ${actionMode === currentMode ? action.active : `border-line bg-surface text-fg-muted ${action.hover}`}`}
              >
                <Icon size={10} />{action.label}
              </button>
            );
          })}
        </div>
      </div>
    </div>
  );
};

export default RouteTargetRow;
