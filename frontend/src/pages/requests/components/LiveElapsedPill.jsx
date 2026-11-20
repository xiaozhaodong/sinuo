// ============================================
// LiveElapsedPill - 请求耗时胶囊（进行中跑秒与定稿共享同一 DOM 节点）
// 2026-08-30
// 跑秒始终落在「当前正在进行的那一段」上：
//   首字未到 -> 首响位在跑；首字已到 -> 生成位在跑。
// 落定时不卸载节点，让「中性灰 → 语义彩」的背景 / 描边 / 字色平滑过渡。
// ============================================

import { useLayoutEffect, useRef } from 'react';
import { parseTimestampOrNull } from '../utils/lifecycle.js';
import { resolveElapsedMs, subscribeElapsedTick } from '../utils/elapsedTicker.js';
import {
  formatOptionalTimingBadge,
  formatTimingBadge,
  getRunningPillClassName,
  getTimingPillClassName,
  TIMING_PILL_BASE
} from '../utils/timing.js';

// 首帧占位。渲染函数必须是纯的，读不了 Date.now，所以先渲染一个格式正确、
// 宽度接近真实值的字符串，由 layout effect 在绘制前替换掉 —— 用户看不到它。
const PLACEHOLDER_MS = 0;

/**
 * @param {Object} props
 * @param {boolean} [props.live=true] 是否仍在实时跑秒；false 时显示定稿值
 * @param {string} [props.timestamp] 请求开始时刻（固定微秒精度 UTC ISO）
 * @param {number} [props.baseMs=0] 当前段之前已走完的毫秒数
 * @param {number|null} [props.value] 定稿后的毫秒值（live=false 时展示）
 * @param {number|null} [props.fallback] value 缺省时用于判定色阶的兜底值
 * @param {'first'|'duration'} props.threshold 用哪套阈值上色
 */
const LiveElapsedPill = ({ live = true, timestamp, baseMs = 0, value, fallback, threshold }) => {
  const nodeRef = useRef(null);
  const startedAt = live ? parseTimestampOrNull(timestamp)?.getTime() : undefined;
  const canTick = live && Number.isFinite(startedAt);

  // useLayoutEffect 而非 useEffect：首次 paint 必须赶在浏览器绘制之前，
  // 否则占位值会先闪一帧再跳到真实值。
  useLayoutEffect(() => {
    const node = nodeRef.current;
    if (!node) return undefined;

    if (!live) {
      const settledClass = `${TIMING_PILL_BASE} ${getTimingPillClassName(threshold, value ?? fallback)}`;
      if (node.className !== settledClass) node.className = settledClass;
      const settledText = formatOptionalTimingBadge(value);
      if (node.textContent !== settledText) node.textContent = settledText;
      return undefined;
    }

    if (!canTick) {
      const fallbackClass = `${TIMING_PILL_BASE} tone-slate`;
      if (node.className !== fallbackClass) node.className = fallbackClass;
      if (node.textContent !== '-') node.textContent = '-';
      return undefined;
    }

    // 直接写 DOM 而不是 setState：跑秒是逐帧的，每帧推一次全表 diff
    // 恰好会撞上新行入场动画在跑的时刻 —— 那是主线程最不该被占的时候。
    const paint = () => {
      const el = nodeRef.current;
      if (!el) return;
      const elapsedMs = resolveElapsedMs(startedAt, baseMs, Date.now());
      el.textContent = formatTimingBadge(elapsedMs);
      // 正常区间保持中性灰（还在跑 = 未定稿），跨过阈值才变黄/红。
      // 这让跑秒既是预警 —— 卡住的请求会自己浮出来 —— 又给完成瞬间
      // 留出了「灰→彩」这个信号，否则秒表停下来根本看不出来。
      el.className = `${TIMING_PILL_BASE} ${getRunningPillClassName(threshold, elapsedMs)}`;
    };

    paint();
    return subscribeElapsedTick(paint);
  }, [live, canTick, startedAt, baseMs, value, fallback, threshold]);

  const toneClass = !live
    ? getTimingPillClassName(threshold, value ?? fallback)
    : (canTick ? getRunningPillClassName(threshold, PLACEHOLDER_MS) : 'tone-slate');
  const initialText = !live
    ? formatOptionalTimingBadge(value)
    : (canTick ? formatTimingBadge(PLACEHOLDER_MS) : '-');

  return (
    <span ref={nodeRef} className={`${TIMING_PILL_BASE} ${toneClass}`}>
      {initialText}
    </span>
  );
};

export default LiveElapsedPill;
