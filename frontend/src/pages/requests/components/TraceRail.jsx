// ============================================
// TraceRail - 请求轨道（列表用的生命周期缩略图）
// 2026-08-30
// 表格负责「扫一眼知道状态」，详情面板负责「展开后理解耗时」。
// 两者使用同一分段引擎；74px轨道保留既有投影，详情额外拆分内容阶段。
// ============================================

import { useEffect, useRef } from 'react';
import { Check, X, Ban, Minus } from 'lucide-react';
import { getStatusConfig } from '@utils/constants.js';
import { IN_FLIGHT_STATUSES } from '../utils/constants.js';
import { buildRailLifecycleSegments as buildLifecycleSegments, railSegmentColors } from '../utils/lifecycle.js';
import { formatTimingBadge } from '../utils/timing.js';
import { getRequestPresentation } from '../utils/requestPresentation.js';

// 轨道视觉只有 7 档，比状态机少：error 与 failed 同形、timeout 归入 failed
// （都是「没跑完」）。这两组的差异交给状态文字与详情面板，轨道不承担。
const RAIL_VARIANT = {
  pending: 'pending',
  forwarding: 'forwarding',
  processing: 'processing',
  retry: 'retry',
  suspended: 'suspended',
  completed: 'completed',
  failed: 'failed',
  error: 'failed',
  timeout: 'failed',
  cancelled: 'cancelled',
  interrupted: 'cancelled'
};

// 轨道末端符号。刻意用裸符号而非 RequestStatusBadge 的 CheckCircle2 / XCircle：
// 11px 下外圈会糊成一个实心点，反而看不出是对勾还是叉。
// 进行中不给符号 —— 轨道右端空着本身就表示「还没到终点」。
const CAP_ICON = {
  completed: Check,
  failed: X,
  cancelled: Ban,
  suspended: Minus
};

// 状态文字的语义色。去掉了徽章的底色与描边：轨道已经在表达状态，
// 再套一层带底色的徽章是重复编码，也是整张表「吵」的主要来源。
const LABEL_CLASS = {
  forwarding: 'text-accent',
  processing: 'text-accent',
  retry: 'text-warn',
  completed: 'text-success',
  failed: 'text-danger',
  error: 'text-danger',
  timeout: 'text-danger'
};

const segmentLabel = (segment) => `${segment.label} ${formatTimingBadge(segment.ms)}`;

// 落位动画的总时长：fill 360ms、flash 460ms、cap 170+300ms，取最长再留点余量。
const SETTLE_MS = 600;

/**
 * useSettleOnFinish - 「亲眼看着它跑完」时给轨道挂一次落位动画
 *
 * 首屏与翻页拿到的终态行是历史数据，全表一起闪一遍毫无意义，
 * 所以判据不是「现在是终态」而是「上一帧还在跑、这一帧跑完了」。
 * cancelled 档位包括取消和客户端中断，使用静态中性样式，不播放失败提醒动效。
 *
 * 走 ref 直接改 class 而不是 setState：落位是纯视觉的一次性效果，
 * 让它进渲染状态会为每条完成的请求多推两次重渲染（一次点亮一次熄灭），
 * 而这些重渲染发生的时刻正是新行入场动画在跑的时刻。
 * 返回的 ref 必须挂到轨道根节点上。
 */
const useSettleOnFinish = (status, variant) => {
  const railRef = useRef(null);
  const prevStatusRef = useRef(status);

  useEffect(() => {
    const prev = prevStatusRef.current;
    prevStatusRef.current = status;

    const justFinished = prev !== status
      && IN_FLIGHT_STATUSES.has(prev)
      && !IN_FLIGHT_STATUSES.has(status);
    if (!justFinished || variant === 'cancelled') return undefined;

    const node = railRef.current;
    if (!node) return undefined;

    // status 变化时 React 已经用新的 className 覆盖过一次，旧的 is-settling
    // 自然被抹掉；effect 在 DOM 更新之后跑，这里加的是新一轮的。
    node.classList.add('is-settling');
    const timer = window.setTimeout(() => node.classList.remove('is-settling'), SETTLE_MS);
    return () => {
      clearTimeout(timer);
      node.classList.remove('is-settling');
    };
  }, [status, variant]);

  return railRef;
};

/**
 * TraceRail - 单条请求的轨道
 *
 * ⚠️ 内部 DOM 结构对所有状态恒定（dot / track > flow + fill / cap），
 * 状态之间只有 className 与 fill 的子元素在变。这不是风格问题：
 * pending → forwarding → processing 会在几百毫秒内迁移两次，只要结构变了
 * React 就会重建节点，流光动画随之跳回起点，看着像卡顿。
 */
const TraceRail = ({ request }) => {
  const status = getRequestPresentation(request).status;
  const variant = RAIL_VARIANT[status] || 'pending';
  const inFlight = IN_FLIGHT_STATUSES.has(status);
  const CapIcon = CAP_ICON[variant];
  const railRef = useSettleOnFinish(status, variant);

  // 分段只画给「真正跑完了」的请求。
  // suspended 不算：它虽然不在 IN_FLIGHT（等人工干预，不该有流光），但请求
  // 并没有结束 —— duration 还不是最终值，照着画分段等于伪造一条不存在的时间线。
  // 它的半程灰条是纯静态装饰，不承载比例语义。
  const settled = !inFlight && variant !== 'suspended';
  const segments = settled ? buildLifecycleSegments(request) : null;
  const description = segments
    ? segments.map(segmentLabel).join(' · ')
    : getStatusConfig(status).label;

  return (
    <span
      ref={railRef}
      className={`trace-rail trace-rail--${variant}`}
      // aria-hidden 而不是 role="img"+aria-label：右边紧挨着的就是同一个状态的
      // 可见文字，给轨道再挂一个可访问名会让读屏把状态念两遍。轨道是那行文字的
      // 图形化重复，按惯例应该退出可访问性树，由文字承担名称。
      // 终态的分段明细也没丢：耗时列的首响/生成两枚 pill 是文字，详情面板里
      // 还有完整的 LifecyclePanel。title 保留 —— 悬停看分段是鼠标用户的额外收益。
      aria-hidden="true"
      title={description}
    >
      <span className="trace-rail__dot" />
      <span className="trace-rail__track">
        <span className="trace-rail__flow" />
        <span className="trace-rail__fill">
          {segments?.map((segment) => (
            <i
              key={segment.key}
              className={railSegmentColors[segment.key] || railSegmentColors.total}
              // flexBasis:0 + flexGrow 才是按比例分配；只给 flexGrow 会被
              // 各段的内容宽度（这里是 0）之外的 basis:auto 干扰。
              style={{ flexGrow: Math.max(segment.ms, 1), flexBasis: 0 }}
            />
          ))}
        </span>
      </span>
      <span className="trace-rail__cap">
        {CapIcon && <CapIcon className="w-2.5 h-2.5" strokeWidth={3} />}
      </span>
    </span>
  );
};

/**
 * RequestTraceCell - 状态列单元格：轨道 + 纯文字标签
 *
 * 文字不可省。轨道是颜色编码，全色盲下紫/绿会退化成两段明度差；
 * 状态本身必须有一条不依赖颜色的读法。
 *
 * 宽度写死（74 轨道 + 10 间距 + 3 字标签）：表格是 table-layout:auto，
 * 列宽取当页内容的最大值。状态列现在排在首位，它一抖后面 11 列全跟着右移。
 * 更硬的理由是 getStatusConfig 的兜底会把未知状态的 label 退化成后端原始串，
 * 那种时候没有上限就会把整列撑开 —— truncate 负责把它截住。
 */
export const RequestTraceCell = ({ request }) => {
  const { status, description } = getRequestPresentation(request);
  return (
    <span className="inline-flex w-[124px] items-center" title={description || undefined}>
      <TraceRail request={request} />
      <span className={`ml-2.5 min-w-0 truncate text-xs font-medium ${LABEL_CLASS[status] || 'text-fg-muted'}`}>
        {getStatusConfig(status).label}
      </span>
    </span>
  );
};

export default TraceRail;
