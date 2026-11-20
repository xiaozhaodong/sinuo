// ============================================
// RequestsTable - 请求列表表格（支持动态列配置）
// 2025-12-01 10:31:09
// 2026-10-07 桌面化：贴边 desk-table；单击选中（详情在底部面板），双击打开面板，右键菜单由页面提供。
//            外框、标题行与分页已移出（分页与统计在页面状态栏）。
// ============================================

import React from 'react';
import { Waves, RefreshCw } from 'lucide-react';
import { LoadingSpinner } from '@components/ui';
import { useTimezone } from '@contexts/TimezoneContext.jsx';
import { RequestTraceCell } from './TraceRail.jsx';
import RequestTimingCell from './RequestTimingCell.jsx';
import ModelTag from './ModelTag.jsx';
import PrivacyWarningBadge from './PrivacyWarningBadge.jsx';
import { CODEX_SEARCH_USAGE_HINT, isCodexSearchRequest, resolveRequestTypeBadge } from '../utils/requestSource.js';
import { buildModelAlert } from '../utils/modelConsistency.js';
import { formatSessionRatio } from '../utils/sessionUsage.js';
import { formatExactRequestCost, formatRequestListCost, getSettledRequestCost } from '../utils/requestPresentation.js';
import { UNSETTLED_STATUSES } from '../utils/constants.js';
import { assignStableZebraParity, NO_ROW_ANIMATIONS } from '../utils/rowEnterAnimation.js';
import { prefersReducedMotion } from '@utils/motion.js';

// 新行入场动画名（已移除左缘 2px 亮线，只保留顺向轻落入槽）。
const FINAL_ENTER_ANIMATION = 'request-row-rise';
// animationend 兜底时长：220ms 轻落 + 错峰 / 让位前置余量。
// 若标签页在后台未推进动画，兜底摘掉入场 class 与 --enter-delay，避免后续重播。
const ENTER_TEARDOWN_MS = 500;
// 既有行让位下移的 FLIP 动画时长（仅在实时新增行插入顶部时触发）。
const ROW_SHIFT_FLIP_MS = 220;
// 下方存在既有行向下让位时，给新行 65ms 起跑前置：
// 旧行前 65ms 已滑出 20px/32px，新行再从 -8px 轻落入槽，全程零像素重叠、无重影。
const ROW_ENTER_SHIFT_LEAD_MS = 65;
const MAX_ROW_SHIFT_PX = 320;

// 自动表格布局下，为内容宽度容易变化的列保留最小宽度。
const COLUMN_MIN_WIDTH = {
  requestId: 'min-w-[116px]',
  model: 'w-[152px] min-w-[116px]',
  upstreamName: 'min-w-[88px]',
  duration: 'min-w-[132px]',
  cost: 'min-w-[68px]'
};

const RequestStreamIcon = ({ request }) => {
  const StreamIcon = request.isStreaming ? Waves : RefreshCw;
  const streamTitle = request.isStreaming ? '流式请求' : '常规请求';
  const iconColor = request.isStreaming ? 'text-info' : 'text-fg-subtle';

  return <StreamIcon className={`w-3 h-3 ${iconColor} flex-shrink-0`} title={streamTitle} />;
};

// 在途时 token 与成本都还没有值：后端只在终态写这几个字段
// （tracker.go 的 UpdateActiveRequestTokens 声称支持流式累积，但全项目零调用），
// 所以此刻的 0 是「还不知道」，不是「用了 0 个」。
//
// 渲染成占位符除了诚实，也是完成瞬间最强的那个信号：0 是个合法数字，
// 从 0 换成别的数字眼睛不会注意到，从 — 变成数字才是一次「到货」。
const PENDING_VALUE = '—';

// ValueCell - 右对齐的数值单元格，在途时显示占位符。
const ValueCell = ({ pending, tone, title, children }) => (
  <span className={`inline-block font-mono text-xs ${pending ? 'text-fg-subtle' : tone}`} title={title}>
    {pending ? PENDING_VALUE : children}
  </span>
);

const renderCell = (columnId, request, formatTime, enterAnimation) => {
  const pending = UNSETTLED_STATUSES.has(request.status);
  // 已落定但没有收到 usage 的记录与在途同理：0 是「还不知道」而不是「用了 0 个」。
  const usagePending = pending || request.usageKnown === false;
  const usageHint = isCodexSearchRequest(request) ? CODEX_SEARCH_USAGE_HINT : undefined;

  switch (columnId) {
    case 'requestId':
      return (
        <div className="flex items-center gap-1.5 text-accent font-mono text-xs group-hover:text-accent-fg transition-colors">
          <RequestStreamIcon request={request} />
          <span className="truncate">{request.requestId}</span>
        </div>
      );
    case 'timestamp':
      return <span className="inline-block text-xs tabular-nums text-fg-subtle">{formatTime(request.timestamp)}</span>;
    case 'sessionId':
      return <span className="inline-block font-mono text-xs text-fg-muted" title={request.sessionId}>{request.sessionId?.slice(0, 8) || '—'}</span>;
    case 'status':
      return <span className="inline-flex items-center gap-2"><RequestTraceCell request={request} enterAnimation={enterAnimation} /><PrivacyWarningBadge codes={request.privacyWarnings} /></span>;
    case 'model':
      // 模型列宽由表格分配，标签只在已分配的空间内收缩，保留完整名称供悬停查看。
      return <div className="flex [contain:inline-size]"><ModelTag model={request.model} alert={buildModelAlert(request)} compact /></div>;
    case 'requestFamily': {
      const badge = resolveRequestTypeBadge(request);
      // 文字包一层 span：grad-tone 的渐变字只作用于内层 span
      return <span className={`inline-flex items-center rounded-md border px-1.5 py-[3px] text-xs leading-4 font-semibold ${badge.className}`} title={badge.title}><span>{badge.label}</span></span>;
    }
    case 'upstreamName':
      return <span className="block max-w-[120px] truncate text-xs text-fg-body" title={request.upstreamName}>{request.upstreamName || '未知上游'}</span>;
    case 'duration':
      return <RequestTimingCell request={request} />;
    case 'inputTokens':
      return <ValueCell pending={usagePending || request.inputTokens == null} tone="text-fg-body" title={usageHint}>{request.inputTokens}</ValueCell>;
    case 'outputTokens':
      return <ValueCell pending={usagePending} tone="text-fg-body" title={usageHint}>{request.outputTokens}</ValueCell>;
    case 'cacheCreationTokens':
      return <ValueCell pending={usagePending || request.cacheCreationTokens == null} tone="text-fg-muted" title={usageHint}>{request.cacheCreationTokens}</ValueCell>;
    case 'cacheReadTokens':
      return <ValueCell pending={usagePending || request.cacheReadTokens == null} tone="text-fg-muted" title={usageHint}>{request.cacheReadTokens}</ValueCell>;
    case 'cacheRatio':
      return <ValueCell pending={usagePending || request.cacheRatio == null} tone="text-fg-body" title={usageHint || '按本行记录的 Token 计算：缓存读取 ÷ 完整输入'}>{formatSessionRatio(request.cacheRatio)}</ValueCell>;
    case 'cost': {
      const cost = getSettledRequestCost(request);
      return (
        <ValueCell pending={cost === null} tone="text-warn font-medium" title={cost === null ? usageHint : formatExactRequestCost(cost)}>
          {cost !== null && formatRequestListCost(cost)}
        </ValueCell>
      );
    }
    default:
      return null;
  }
};

/**
 * RequestRow - 单行请求数据（单击选中、双击打开详情面板、右键菜单）
 *
 * columns 收的是列配置对象数组（不是 id 数组），与表头同一个引用。
 *
 * enterAnimation 只在挂载那一刻取值并固化：React 按 requestId 做 key diff，
 * 「新的 requestId」等价于「新挂载的 RequestRow」。固化之后，
 * 后续任何 re-render 都不会重播入场动画。
 */
const RequestRow = ({ request, columns, selected, onSelect, onOpen, onContextMenu, formatTime, enterAnimation }) => {
  const [initialEnterAnimation] = React.useState(enterAnimation);
  const [entering, setEntering] = React.useState(() => Boolean(initialEnterAnimation));

  // 入场跑完就摘 class：它只在挂载那一次有意义，留在 DOM 上没有价值，
  // 且一旦将来有规则中断 animation（例如给 hover 加 animation:none），
  // 中断恢复时会把入场从头重播一次，看着像又来了一条新请求。
  React.useEffect(() => {
    if (!entering) return undefined;
    // animationend 的兜底，不是主路径 —— 主路径见 handleAnimationEnd。
    const timer = window.setTimeout(
      () => setEntering(false), ENTER_TEARDOWN_MS + (initialEnterAnimation?.delayMs || 0)
    );
    return () => window.clearTimeout(timer);
  }, [entering, initialEnterAnimation]);

  const handleAnimationEnd = React.useCallback((event) => {
    if (event.target === event.currentTarget && event.animationName === FINAL_ENTER_ANIMATION) {
      setEntering(false);
    }
  }, []);

  return (
    <tr
      data-row-id={request.requestId}
      aria-selected={selected}
      className={`group ${entering ? 'request-row-enter' : ''}`}
      style={entering && initialEnterAnimation?.delayMs ? { '--enter-delay': `${initialEnterAnimation.delayMs}ms` } : undefined}
      onClick={() => onSelect?.(request)}
      onDoubleClick={() => onOpen?.(request)}
      onContextMenu={(event) => onContextMenu?.(event, request)}
      onAnimationEnd={handleAnimationEnd}
    >
      {/* 与表头同源：都遍历 visibleColumnConfigs。曾经这里走的是 visibleColumns
          （state 数组），表头走 TABLE_COLUMNS，两者只靠「手抄的默认数组恰好同序」
          才对得上 —— 一旦 TABLE_COLUMNS 调整顺序，表头就会盖在错误的数据列上，
          且勾一下任意列就会自愈，只在首屏错。 */}
      {columns.map((col) => (
        <td key={col.id} className={col.align === 'right' ? 'text-right' : undefined}>
          {renderCell(col.id, request, formatTime, entering ? initialEnterAnimation : null)}
        </td>
      ))}
    </tr>
  );
};

// memo 生效的前提是 index.jsx 用 mergeRequestRows 保持了未变化行的对象引用。
// enterAnimation 已在挂载时固化，比较它没有意义，故排除在外。
// 选中态变化只重渲染新旧两行；三个回调由页面 useCallback 保持稳定。
const MemoizedRequestRow = React.memo(RequestRow, (prev, next) => (
  prev.request === next.request
  && prev.columns === next.columns
  && prev.selected === next.selected
  && prev.onSelect === next.onSelect
  && prev.onOpen === next.onOpen
  && prev.onContextMenu === next.onContextMenu
  && prev.formatTime === next.formatTime
));

/**
 * 实时插入后先维持滚动锚点，再给视口附近的既有行做 FLIP，并按行身份维护稳定斑马纹。
 * 斑马纹复用滚动锚点，避免视口上方有行增删时阅读区底色翻转。
 * 自然布局坐标用于锚定，视口坐标差用于 FLIP。行样式读取与写入按批次分开。
 */
const useRowShiftFlip = (tbodyRef, rowOrderKey, enterAnimations, animateArrivals) => {
  const prevRowsRef = React.useRef(new Map());
  const activeShiftsRef = React.useRef(new Map());
  const zebraMapRef = React.useRef(new Map());

  React.useLayoutEffect(() => {
    const tbody = tbodyRef.current;
    if (!tbody) {
      prevRowsRef.current.clear();
      zebraMapRef.current = new Map();
      return;
    }

    const rows = Array.from(tbody.querySelectorAll('tr[data-row-id]'));
    // offsetTop 排除入场和让位的视觉位移。首行作为 tbody 内偏移的原点。
    const offsetOrigin = rows[0]?.offsetTop ?? 0;
    const prevRows = prevRowsRef.current;
    const nextRows = new Map(rows.map((tr) => [tr.dataset.rowId, {
      top: tr.offsetTop - offsetOrigin,
      height: tr.offsetHeight
    }]));
    prevRowsRef.current = nextRows;

    const table = tbody.parentElement;
    const container = table.parentElement;
    const scrollContainer = container?.matches('.overflow-auto, .overflow-y-auto') ? container : null;
    const hasLiveArrivals = enterAnimations.size > 0;
    const scrollTop = prevRows.size > 0 ? (scrollContainer?.scrollTop ?? 0) : 0;
    const shouldFlip = prevRows.size > 0 && animateArrivals;
    const readViewport = scrollTop > 0 || shouldFlip;
    const tbodyTop = readViewport ? tbody.getBoundingClientRect().top : 0;
    const clipRect = readViewport ? scrollContainer?.getBoundingClientRect() : null;
    const clipTop = Math.max(0, clipRect?.top ?? 0);
    const clipBottom = Math.min(window.innerHeight, clipRect?.bottom ?? window.innerHeight);
    let anchorId;
    if (scrollTop > 0) {
      // 避开吸顶表头，优先选择首条从行首可见且仍在的旧行。
      const anchorTop = Math.max(clipTop, (clipRect?.top ?? 0) + (table.tHead?.offsetHeight ?? 0)) - tbodyTop;
      const anchorBottom = clipBottom - tbodyTop;
      if (anchorTop < anchorBottom) {
        // 实时插入时表格接管补偿，提交时的滚动位置对应旧布局。其余更新按原生滚动后的当前布局选行。
        const anchorRows = hasLiveArrivals ? prevRows : nextRows;
        const otherRows = hasLiveArrivals ? nextRows : prevRows;
        for (const [id, { top, height }] of anchorRows) {
          if (top >= anchorBottom) break;
          if (top + height <= anchorTop || !otherRows.has(id)) continue;
          anchorId = id;
          if (top >= anchorTop) break;
        }
      }
    }

    let scrollDelta = 0;
    if (anchorId && hasLiveArrivals) {
      const anchorShift = nextRows.get(anchorId).top - prevRows.get(anchorId).top;
      if (anchorShift !== 0) {
        // 以实际滚动量抵消位移，滚动范围夹紧时仍能补足剩余动画。
        scrollContainer.scrollTo({ top: scrollTop + anchorShift, behavior: 'instant' });
        scrollDelta = scrollContainer.scrollTop - scrollTop;
      }
    }

    const activeShifts = activeShiftsRef.current;
    const pendingShifts = [];
    if (shouldFlip) {
      rows.forEach((tr) => {
        const id = tr.dataset.rowId;
        const prevTop = prevRows.get(id)?.top;
        if (prevTop === undefined || enterAnimations.has(id)) return;
        const { top, height } = nextRows.get(id);
        const deltaY = prevTop - top + scrollDelta;
        if (Math.abs(deltaY) < 1 || Math.abs(deltaY) > MAX_ROW_SHIFT_PX) return;

        const nextViewportTop = tbodyTop + top - scrollDelta;
        const prevViewportTop = nextViewportTop + deltaY;
        if (Math.min(prevViewportTop, nextViewportTop) > clipBottom + 64
          || Math.max(prevViewportTop, nextViewportTop) + height < clipTop - 64) return;

        // transform 只承载让位，独立的 translate 入场动画可继续播放。
        let currentOffsetY = 0;
        if (activeShifts.has(tr)) {
          const transform = window.getComputedStyle(tr).transform;
          if (transform !== 'none') currentOffsetY = new DOMMatrixReadOnly(transform).m42;
        }
        pendingShifts.push({ tr, offsetY: deltaY + currentOffsetY });
      });
    }

    // 写阶段：所有样式读取均已结束，再同步写入新行斑马纹与让位初始值。
    const nextZebra = assignStableZebraParity(
      rows.map((tr) => ({ requestId: tr.dataset.rowId })), zebraMapRef.current, anchorId
    );
    zebraMapRef.current = nextZebra;
    rows.forEach((tr) => {
      const nextAttr = nextZebra.get(tr.dataset.rowId) === 1 ? '1' : '0';
      if (tr.dataset.zebra !== nextAttr) {
        tr.dataset.zebra = nextAttr;
      }
    });

    if (pendingShifts.length === 0) return;

    // 只写入 <tr>。让位使用独立属性，React 移除入场 class 时不会打断让位。
    pendingShifts.forEach(({ tr, offsetY }) => {
      activeShifts.get(tr)?.();
      tr.style.setProperty('--row-shift-y', `${offsetY}px`);
      tr.dataset.rowShift = 'prep';
    });

    // 整表仅一次同步 flush，提交全部可见行的初始位移。
    void tbody.offsetHeight;

    pendingShifts.forEach(({ tr }) => {
      tr.dataset.rowShift = 'run';
      const cleanup = () => {
        window.clearTimeout(timer);
        activeShifts.delete(tr);
        delete tr.dataset.rowShift;
        tr.style.removeProperty('--row-shift-y');
      };
      const timer = window.setTimeout(cleanup, ROW_SHIFT_FLIP_MS + 40);
      activeShifts.set(tr, cleanup);
    });
  }, [tbodyRef, rowOrderKey, enterAnimations, animateArrivals]);

  React.useEffect(() => {
    const activeShifts = activeShiftsRef.current;
    return () => activeShifts.forEach((cleanup) => cleanup());
  }, []);
};

/**
 * RequestsTable - 请求列表表格组件（支持动态列）
 * 滚动容器、键盘导航与右键菜单由页面负责；这里只渲染表格本身。
 * @param {Object} props
 * @param {Array} props.requests - 请求列表
 * @param {boolean} props.loading - 首次加载（整表换成加载指示）
 * @param {Array} props.visibleColumns - 可见列ID数组（与 columnConfigs 求交后决定渲染哪些列）
 * @param {Array} props.columnConfigs - 列配置数组，其顺序即渲染顺序
 * @param {string|null} props.selectedId - 选中行的 requestId
 * @param {Function} props.onSelect - 单击行 (request) => void
 * @param {Function} props.onOpen - 双击行 (request) => void
 * @param {Function} props.onRowContextMenu - 右键行 (event, request) => void
 * @param {Function} props.onHeaderContextMenu - 右键表头 (event) => void
 * @param {Map<string, {delayMs:number}>} props.enterAnimations
 *        本批实时新增行及其入场编排，减少动态效果时仍用于滚动锚定
 */
const RequestsTable = ({
  requests = [],
  loading = false,
  enterAnimations = NO_ROW_ANIMATIONS,
  visibleColumns = [],
  columnConfigs = [],
  selectedId = null,
  onSelect,
  onOpen,
  onRowContextMenu,
  onHeaderContextMenu
}) => {
  // 时间列：当天只显示时分秒（完整日期在详情面板标题行）
  const { formatListTimestamp } = useTimezone();
  const tbodyRef = React.useRef(null);

  const visibleColumnConfigs = React.useMemo(
    () => columnConfigs.filter(col => visibleColumns.includes(col.id)),
    [columnConfigs, visibleColumns]
  );

  const rowOrderKey = loading ? '' : requests.map((req) => req.requestId).join('|');
  const hasLiveArrivals = enterAnimations.size > 0;
  const animateArrivals = hasLiveArrivals && !prefersReducedMotion();
  useRowShiftFlip(tbodyRef, rowOrderKey, enterAnimations, animateArrivals);
  // 下方有未入场的既有行向下让位时，统一给新行追加 65ms 让位前置（空表首条保持 0ms）。
  const hasShiftingRows = animateArrivals
    && requests.some((req) => !enterAnimations.has(req.requestId));

  if (loading) {
    return <LoadingSpinner text="加载请求数据..." />;
  }

  return (
    <table className="desk-table desk-dense requests-table" data-live-arrivals={hasLiveArrivals ? '' : undefined}>
      <thead onContextMenu={onHeaderContextMenu}>
        <tr>
          {visibleColumnConfigs.map(col => {
            const thClass = [col.align === 'right' ? 'text-right' : '', COLUMN_MIN_WIDTH[col.id] || ''].filter(Boolean).join(' ') || undefined;
            return (
              <th key={col.id} className={thClass}>
                {col.label}
              </th>
            );
          })}
        </tr>
      </thead>
      <tbody ref={tbodyRef}>
        {requests.length === 0 ? (
          <tr>
            <td colSpan={visibleColumnConfigs.length} className="h-24 text-center text-fg-subtle">
              暂无请求数据
            </td>
          </tr>
        ) : (
          requests.map((req) => {
            const rawEnter = animateArrivals ? enterAnimations.get(req.requestId) : null;
            const rowEnterAnimation = rawEnter
              ? (hasShiftingRows ? { delayMs: (rawEnter.delayMs || 0) + ROW_ENTER_SHIFT_LEAD_MS } : rawEnter)
              : null;
            return (
              <MemoizedRequestRow
                key={req.requestId}
                request={req}
                columns={visibleColumnConfigs}
                selected={selectedId === req.requestId}
                onSelect={onSelect}
                onOpen={onOpen}
                onContextMenu={onRowContextMenu}
                formatTime={formatListTimestamp}
                enterAnimation={rowEnterAnimation}
              />
            );
          })
        )}
      </tbody>
    </table>
  );
};

export default RequestsTable;
