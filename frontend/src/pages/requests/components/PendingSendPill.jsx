// ============================================
// PendingSendPill - 首响位「尚未发到上游」的等待点
// 2026-10-11
// 还没发到上游（本地处理、建连）时首响还没开始计，只放等待点、不给数字。
// 大多只停零点几秒，所以 1 秒内静止；超过 1 秒三个点从左到右依次变暗再亮回，
// 再久变黄 / 变红（阈值见 PENDING_SEND_THRESHOLDS_MS）。
// 这时状态列的轨道也在流动，同一行两处在动是有意的：只在真卡在发出前时才出现。
// ============================================

import { useLayoutEffect, useRef } from 'react';
import { parseTimestampOrNull } from '../utils/lifecycle.js';
import { subscribeElapsedTick } from '../utils/elapsedTicker.js';
import { getPendingSendPillState, TIMING_PILL_BASE } from '../utils/timing.js';

const PENDING_SEND_HINT = '尚未发到上游（本地处理 / 建连中）';

// 宽度钉成数字胶囊的 5 个等宽字符（border-box：内容 5ch + 左右 px-2 + 边框），换成数字时不抖。
// 居中点比句点在胶囊里不下沉。
const PILL_LAYOUT = 'w-[calc(5ch+1rem+2px)] justify-center pending-send-dots';

const buildClassName = ({ tone, animate }) => (
  `${TIMING_PILL_BASE} ${tone} ${PILL_LAYOUT}${animate ? ' pending-send-dots--waiting' : ''}`
);

// 首帧按刚进入算（静止、灰），由 layout effect 在绘制前换成真实阶段
const INITIAL_CLASS_NAME = buildClassName(getPendingSendPillState(0));

/**
 * @param {Object} props
 * @param {string} props.timestamp 请求进入时刻（固定微秒精度 UTC ISO）
 */
const PendingSendPill = ({ timestamp }) => {
  const nodeRef = useRef(null);
  const startedAt = parseTimestampOrNull(timestamp)?.getTime();
  const canTick = Number.isFinite(startedAt);

  useLayoutEffect(() => {
    if (!canTick) return undefined;

    // 与跑秒共用一个时钟、直接写 className；阶段只在跨阈值时变，没变就不碰 DOM，
    // 也不会把已经在跑的点动画打断重来。
    const paint = () => {
      const node = nodeRef.current;
      if (!node) return;
      const className = buildClassName(getPendingSendPillState(Date.now() - startedAt));
      if (node.className !== className) {
        node.className = className;
      }
    };

    paint();
    return subscribeElapsedTick(paint);
  }, [canTick, startedAt]);

  return (
    <span ref={nodeRef} className={INITIAL_CLASS_NAME} title={PENDING_SEND_HINT} aria-label={PENDING_SEND_HINT}>
      <i>·</i><i>·</i><i>·</i>
    </span>
  );
};

export default PendingSendPill;
