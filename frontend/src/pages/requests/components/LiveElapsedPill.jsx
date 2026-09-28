// ============================================
// LiveElapsedPill - 进行中请求的跑秒徽章
// 2026-08-30
// 跑秒始终落在「当前正在进行的那一段」上：
//   首字未到 -> 首响位在跑；首字已到 -> 生成位在跑。
// 位置交接的那一刻正是首字到达的瞬间，与轨道的流光语义一致
// （都在表达「现在走到哪一步了」）。
// ============================================

import { useLayoutEffect, useRef } from 'react';
import { parseTimestampOrNull } from '../utils/lifecycle.js';
import { resolveElapsedMs, subscribeElapsedTick } from '../utils/elapsedTicker.js';
import { formatTimingBadge, getRunningPillClassName, TIMING_PILL_BASE } from '../utils/timing.js';


// 首帧占位。渲染函数必须是纯的，读不了 Date.now，所以先渲染一个格式正确、
// 宽度接近真实值的字符串，由 layout effect 在绘制前替换掉 —— 用户看不到它。
const PLACEHOLDER_MS = 0;

/**
 * @param {Object} props
 * @param {string} props.timestamp 请求开始时刻（固定微秒精度 UTC ISO）
 * @param {number} props.baseMs 当前段之前已走完的毫秒数
 * @param {'first'|'duration'} props.threshold 用哪套阈值上色
 */
const LiveElapsedPill = ({ timestamp, baseMs = 0, threshold }) => {
  const nodeRef = useRef(null);
  const startedAt = parseTimestampOrNull(timestamp)?.getTime();
  const canTick = Number.isFinite(startedAt);

  // useLayoutEffect 而非 useEffect：首次 paint 必须赶在浏览器绘制之前，
  // 否则占位值会先闪一帧再跳到真实值。
  useLayoutEffect(() => {
    if (!canTick) return undefined;

    // 直接写 DOM 而不是 setState：跑秒是逐帧的，每帧推一次全表 diff
    // 恰好会撞上新行入场动画在跑的时刻 —— 那是主线程最不该被占的时候。
    const paint = () => {
      const node = nodeRef.current;
      if (!node) return;
      const elapsedMs = resolveElapsedMs(startedAt, baseMs, Date.now());
      node.textContent = formatTimingBadge(elapsedMs);
      // 正常区间保持中性灰（还在跑 = 未定稿），跨过阈值才变黄/红。
      // 这让跑秒既是预警 —— 卡住的请求会自己浮出来 —— 又给完成瞬间
      // 留出了「灰→彩」这个信号，否则秒表停下来根本看不出来。
      node.className = `${TIMING_PILL_BASE} ${getRunningPillClassName(threshold, elapsedMs)}`;
    };

    paint();
    return subscribeElapsedTick(paint);
  }, [canTick, startedAt, baseMs, threshold]);

  return (
    <span
      ref={nodeRef}
      // 时间戳解析失败时同样是中性色 —— 按 elapsed=0 上色会涂成「健康」的绿，
      // 把一个读不出来的值伪装成一次很快的响应。
      className={`${TIMING_PILL_BASE} ${canTick ? getRunningPillClassName(threshold, PLACEHOLDER_MS) : 'tone-slate'}`}
    >
      {canTick ? formatTimingBadge(PLACEHOLDER_MS) : '-'}
    </span>
  );
};

export default LiveElapsedPill;
