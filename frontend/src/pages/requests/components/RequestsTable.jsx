// ============================================
// RequestsTable - 请求列表表格（支持动态列配置）
// 2025-12-01 10:31:09
// 2026-10-07 桌面化：贴边 desk-table；单击选中（详情在底部面板），双击打开面板，右键菜单由页面提供。
//            外框、标题行与分页已移出（分页与统计在页面状态栏）。
// ============================================

import React from 'react';
import { createPortal } from 'react-dom';
import { Waves, RefreshCw } from 'lucide-react';
import { LoadingSpinner } from '@components/ui';
import { formatCost } from '@utils/api.js';
import { useTimezone } from '@contexts/TimezoneContext.jsx';
import { RequestTraceCell } from './TraceRail.jsx';
import LiveElapsedPill from './LiveElapsedPill.jsx';
import PendingSendPill from './PendingSendPill.jsx';
import ModelTag from './ModelTag.jsx';
import { getRequestFamilyMeta } from '../utils/requestSource.js';
import { buildModelAlert } from '../utils/modelConsistency.js';
import { IN_FLIGHT_STATUSES, UNSETTLED_STATUSES } from '../utils/constants.js';
import { NO_ROW_ANIMATIONS } from '../utils/rowEnterAnimation.js';
import { resolveGenerationTickAnchor } from '../utils/elapsedTicker.js';
import { parseTimestampOrNull } from '../utils/lifecycle.js';
import {
  calculateTokensPerSecond,
  formatOptionalTimingBadge,
  formatTimingBadge,
  formatTpsBadge,
  getTimingPillClassName,
  resolveCompletionMs,
  resolveFirstResponseMs,
  TIMING_PILL_BASE
} from '../utils/timing.js';

// 整套入场里收尾最晚的一条动画（左缘亮线 620ms）。摘 class 必须等它 ——
// 单元格位移只有 200ms 且会从 td 冒泡上来，认 target 会提前 420ms 摘掉，
// 把亮线截断在半路。
const FINAL_ENTER_ANIMATION = 'request-row-edge-tick';
// 兜底时长：620ms 亮线 + 最大 stagger + 一点余量。伪元素的 animationend 一旦
// 没派发（或标签页在后台、动画根本不推进），class 与 --enter-delay 就会永久
// 留在 DOM 上，下次任何 animation 中断恢复都会重播一次入场。
const ENTER_TEARDOWN_MS = 900;

const RequestStreamIcon = ({ request }) => {
  const StreamIcon = request.isStreaming ? Waves : RefreshCw;
  const streamTitle = request.isStreaming ? '流式请求' : '常规请求';
  const iconColor = request.isStreaming ? 'text-info' : 'text-fg-subtle';

  return <StreamIcon className={`w-3 h-3 ${iconColor} flex-shrink-0`} title={streamTitle} />;
};

const TimingTooltip = ({ anchorRect, items }) => {
  if (!anchorRect) {
    return null;
  }

  const style = {
    left: `${anchorRect.left + anchorRect.width / 2}px`,
    top: `${anchorRect.bottom + 6}px`
  };

  return createPortal(
    <div
      className="pointer-events-none fixed z-[10001] -translate-x-1/2 rounded-lg border border-line bg-surface px-3 py-2 text-xs text-fg-body shadow-lg ring-1 ring-hairline"
      style={style}
    >
      <div className="grid grid-cols-[auto_auto] gap-x-3 gap-y-1">
        {items.map(item => (
          <React.Fragment key={item.label}>
            <span className="text-fg-subtle">{item.label}</span>
            <span className="text-right font-mono font-semibold text-fg-body">{item.value}</span>
          </React.Fragment>
        ))}
      </div>
    </div>,
    document.body
  );
};

// 首响位在跑的样子：从上游请求写完开始跑，与后端首响同一起点，定格时不跳。
// 还没发到上游时首响还没开始计，只放等待点、不给数字（见 PendingSendPill）。
const RunningFirstResponsePill = ({ request }) => (
  parseTimestampOrNull(request.firstTokenStartAt)
    ? <LiveElapsedPill timestamp={request.firstTokenStartAt} threshold="first" />
    : <PendingSendPill timestamp={request.timestamp} />
);

const RequestTimingCell = ({ request }) => {
  const [tooltipAnchor, setTooltipAnchor] = React.useState(null);
  const running = IN_FLIGHT_STATUSES.has(request.status);
  // 计时可以比状态先定格：生成耗时随计时事件先到，状态与 token 要等下一次查询。
  const timingRunning = running && !Number.isFinite(request.completionMs);
  // 进行中只认后端真实写入的 firstTokenMs。
  // resolveFirstResponseMs 对非流式请求会拿 duration 兜底首响，而那条降级的前提是
  // 「请求已结束」—— 进行中 duration 还是 0（tracker 只在终态写 duration_ms），
  // 兜底会让每条非流式请求都先显示一个假的 0.0s 首响。
  const firstResponseMs = running
    ? (Number.isFinite(request.firstTokenMs) ? Math.max(request.firstTokenMs, 0) : null)
    : resolveFirstResponseMs(request.firstTokenMs, request.duration, request.isStreaming);
  const hasFirstResponse = Number.isFinite(firstResponseMs);
  const completionMs = hasFirstResponse
    ? resolveCompletionMs(request.completionMs, request.duration, firstResponseMs, request.isStreaming)
    : null;
  const tokensPerSecond = calculateTokensPerSecond(request.outputTokens, completionMs);
  const timingTooltipItems = [
    { label: '总耗', value: formatTimingBadge(request.duration) },
    { label: 'TPS', value: formatTpsBadge(tokensPerSecond) }
  ];

  const showTooltip = (event) => {
    setTooltipAnchor(event.currentTarget.getBoundingClientRect());
  };

  const hideTooltip = () => {
    setTooltipAnchor(null);
  };

  // 进行中不挂 tooltip：那两项此刻都还没有值（duration 为 0、TPS 为 null），
  // 而 pill 自己就在实时显示数字，tooltip 没有任何增量信息。
  const hoverHandlers = running ? undefined : {
    onMouseEnter: showTooltip,
    onMouseMove: showTooltip,
    onMouseLeave: hideTooltip,
    onFocus: showTooltip,
    onBlur: hideTooltip
  };

  return (
    <div className="inline-flex items-center gap-1.5" {...hoverHandlers}>
      {/* 首响位。已定值就静态显示；进行中且首字未到时，这一格自己在跑秒。 */}
      {hasFirstResponse ? (
        <span className={`${TIMING_PILL_BASE} ${getTimingPillClassName('first', firstResponseMs)}`}>
          {formatTimingBadge(firstResponseMs)}
        </span>
      ) : timingRunning ? (
        <RunningFirstResponsePill request={request} />
      ) : null}

      {/* 生成位。进行中且首字已到时，这一格接过跑秒 —— 交接的那一刻正是首字到达。
          首字未到时不出这一格：生成还没开始，画一个 '-' 只是噪音。
          起算点见 resolveGenerationTickAnchor：优先从首字到达时刻算，与定稿值同一起点。 */}
      {timingRunning ? (
        hasFirstResponse && (
          <LiveElapsedPill
            {...resolveGenerationTickAnchor(request, firstResponseMs)}
            threshold="duration"
          />
        )
      ) : (
        <span className={`${TIMING_PILL_BASE} ${getTimingPillClassName('duration', completionMs ?? request.duration)}`}>
          {formatOptionalTimingBadge(completionMs)}
        </span>
      )}
      {!running && <TimingTooltip anchorRect={tooltipAnchor} items={timingTooltipItems} />}
    </div>
  );
};

// 在途时 token 与成本都还没有值：后端只在终态写这几个字段
// （tracker.go 的 UpdateActiveRequestTokens 声称支持流式累积，但全项目零调用），
// 所以此刻的 0 是「还不知道」，不是「用了 0 个」。
//
// 渲染成占位符除了诚实，也是完成瞬间最强的那个信号：0 是个合法数字，
// 从 0 换成别的数字眼睛不会注意到，从 — 变成数字才是一次「到货」。
const PENDING_VALUE = '—';

/**
 * ValueCell - 右对齐的数值单元格，在途时显示占位符
 *
 * inline-block 不能省：入场位移挂在 `> td > *` 上，而 transform 对非替换的
 * 行内元素无效 —— 写成裸 <span> 这一列就只会淡入、不会跟着上浮。
 */
const ValueCell = ({ pending, tone, children }) => (
  <span className={`inline-block font-mono text-xs ${pending ? 'text-fg-subtle' : tone}`}>
    {pending ? PENDING_VALUE : children}
  </span>
);

/**
 * 渲染单元格内容
 *
 * ⚠️ 每个分支必须返回单一根元素，且该元素不能是非替换的行内盒
 * （见 ValueCell 的注释）—— 由静态扫描用例锁定。
 */
const renderCell = (columnId, request, formatTime) => {
  const pending = UNSETTLED_STATUSES.has(request.status);
  // 已落定但没有收到 usage 的记录与在途同理：0 是「还不知道」而不是「用了 0 个」。
  const usagePending = pending || request.usageKnown === false;
  const costPending = pending || request.costKnown === false;

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
      return <RequestTraceCell request={request} />;
    case 'model':
      return <ModelTag model={request.model} alert={buildModelAlert(request)} compact />;
    case 'requestFamily': {
      const family = getRequestFamilyMeta(request.requestFamily);
      // 文字包一层 span：grad-tone 的渐变字只作用于内层 span
      return <span className={`inline-flex items-center rounded-md border px-1.5 py-[3px] text-xs leading-4 font-semibold ${family.className}`}><span>{family.label}</span></span>;
    }
    case 'upstreamName':
      return <span className="block max-w-[120px] truncate text-xs text-fg-body" title={request.upstreamName}>{request.upstreamName || '未知上游'}</span>;
    case 'duration':
      return <RequestTimingCell request={request} />;
    case 'inputTokens':
      return <ValueCell pending={usagePending} tone="text-fg-body">{request.inputTokens}</ValueCell>;
    case 'outputTokens':
      return <ValueCell pending={usagePending} tone="text-fg-body">{request.outputTokens}</ValueCell>;
    case 'cacheCreationTokens':
      return <ValueCell pending={usagePending} tone="text-fg-muted">{request.cacheCreationTokens}</ValueCell>;
    case 'cacheReadTokens':
      return <ValueCell pending={usagePending} tone="text-fg-muted">{request.cacheReadTokens}</ValueCell>;
    case 'cost':
      return <ValueCell pending={costPending} tone="text-warn font-medium">{formatCost(request.cost)}</ValueCell>;
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
  const enterTimerRef = React.useRef(null);
  const [entering, setEntering] = React.useState(() => Boolean(enterAnimation));

  // 入场跑完就摘 class：它只在挂载那一次有意义，留在 DOM 上没有价值，
  // 且一旦将来有规则中断 animation（例如给 hover 加 animation:none），
  // 中断恢复时会把入场从头重播一次，看着像又来了一条新请求。
  React.useEffect(() => {
    if (!entering) return undefined;
    // animationend 的兜底，不是主路径 —— 主路径见 handleAnimationEnd。
    enterTimerRef.current = window.setTimeout(() => {
      enterTimerRef.current = null;
      setEntering(false);
    }, ENTER_TEARDOWN_MS + (enterAnimation?.delayMs || 0));
    return () => {
      if (enterTimerRef.current) {
        clearTimeout(enterTimerRef.current);
        enterTimerRef.current = null;
      }
    };
  }, [entering, enterAnimation]);

  // 只认收尾最晚的那条动画。单元格位移（200ms）会从 td 冒泡上来，
  // 认 event.target 会在亮线还剩 420ms 时就把 class 摘掉。
  const handleAnimationEnd = React.useCallback((event) => {
    if (event.animationName === FINAL_ENTER_ANIMATION) {
      setEntering(false);
    }
  }, []);

  return (
    <tr
      data-row-id={request.requestId}
      aria-selected={selected}
      className={`group ${entering ? 'request-row-enter' : ''}`}
      // 走 CSS 变量而非行内 animation-delay：入场由两条动画组成
      // （单元格位移 + 左缘亮线），行内 delay 只能套到其中一条上。
      style={entering && enterAnimation?.delayMs ? { '--enter-delay': `${enterAnimation.delayMs}ms` } : undefined}
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
          {renderCell(col.id, request, formatTime)}
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
 *        本批次新增行的入场编排，由 index.jsx 在拿到新旧两批数据时算好传入
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

  const visibleColumnConfigs = React.useMemo(
    () => columnConfigs.filter(col => visibleColumns.includes(col.id)),
    [columnConfigs, visibleColumns]
  );

  if (loading) {
    return <LoadingSpinner text="加载请求数据..." />;
  }

  return (
    <table className="desk-table desk-dense">
      <thead onContextMenu={onHeaderContextMenu}>
        <tr>
          {visibleColumnConfigs.map(col => (
            <th key={col.id} className={col.align === 'right' ? 'text-right' : undefined}>
              {col.label}
            </th>
          ))}
        </tr>
      </thead>
      <tbody>
        {requests.length === 0 ? (
          <tr>
            <td colSpan={visibleColumnConfigs.length} className="h-24 text-center text-fg-subtle">
              暂无请求数据
            </td>
          </tr>
        ) : (
          requests.map((req) => (
            <MemoizedRequestRow
              key={req.requestId}
              request={req}
              columns={visibleColumnConfigs}
              selected={selectedId === req.requestId}
              onSelect={onSelect}
              onOpen={onOpen}
              onContextMenu={onRowContextMenu}
              formatTime={formatListTimestamp}
              enterAnimation={enterAnimations.get(req.requestId) || null}
            />
          ))
        )}
      </tbody>
    </table>
  );
};

export default RequestsTable;
