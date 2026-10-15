// ============================================
// elapsedTicker - 进行中请求的「跑秒」时钟
// 2026-08-30
// 单个模块级定时器，所有跑秒 pill 共享。
// 订阅者归零时定时器彻底停掉 —— 没有进行中的请求时，
// 整张表必须是零定时器、零动画，这条不能被跑秒破坏。
// ============================================

import { parseTimestampOrNull } from './lifecycle.js';

// 显示精度是 0.01s（formatTimingBadge），刷新必须比它细，否则数字会跳格。
// 浏览器里走 requestAnimationFrame（~16.7ms，且窗口不可见时自动停摆）；
// 这个常量只是 node 测试里没有 rAF 时的降级间隔。
export const TICK_INTERVAL_MS = 16;

/**
 * 计算当前段已耗时。
 *
 * @param {number} startedAt 请求开始的绝对毫秒时刻
 * @param {number} baseMs 当前段之前已经走完的毫秒数（首响位为 0）
 * @param {number} now 当前绝对毫秒时刻
 */
export const resolveElapsedMs = (startedAt, baseMs, now) => (
  // clamp >= 0：桌面端 start_time 与 Date.now() 同源于一个系统时钟，
  // 正常不会为负；但时间戳解析异常或系统时钟被改时不能显示负数。
  Math.max(now - startedAt - baseMs, 0)
);

/**
 * 生成位跑秒的起算锚点。
 * 优先用后端给的首字到达时刻：与 completion_ms（完成时刻 - 首字时刻）同一起点，定稿时不跳。
 * 拿不到时退回「请求开始 + 首响」：首响不含本地处理、连接和首字前的重试，
 * 这个近似会把这几段多算进生成位，定稿时往回跳。
 *
 * @param {{ timestamp: string, firstTokenAt?: string }} request
 * @param {number} firstResponseMs 后端写入的首响毫秒数
 * @returns {{ timestamp: string, baseMs: number }}
 */
export const resolveGenerationTickAnchor = (request, firstResponseMs) => (
  parseTimestampOrNull(request.firstTokenAt)
    ? { timestamp: request.firstTokenAt, baseMs: 0 }
    : { timestamp: request.timestamp, baseMs: firstResponseMs }
);

const subscribers = new Set();
let timerId = 0;
let usingRaf = false;

const tick = () => {
  // 订阅者在回调里只写 textContent / className，不 setState。
  // 逐个 try 隔离：一个订阅者抛错不能让整张表的时钟停摆。
  for (const notify of subscribers) {
    try {
      notify();
    } catch (error) {
      console.error('跑秒回调异常:', error);
    }
  }
};

const startTicking = () => {
  if (typeof requestAnimationFrame === 'function') {
    usingRaf = true;
    const frame = () => {
      // 先排下一帧再 tick：tick 里最后一个订阅者退订时，stopTicking
      // 才能把这一帧取消掉，否则它会漏网多跑一次。
      timerId = requestAnimationFrame(frame);
      tick();
    };
    timerId = requestAnimationFrame(frame);
    return;
  }
  // 裸 setInterval 而非 window.setInterval：这个模块要能在 node 测试里跑。
  usingRaf = false;
  timerId = setInterval(tick, TICK_INTERVAL_MS);
};

const stopTicking = () => {
  if (!timerId) return;
  if (usingRaf) {
    cancelAnimationFrame(timerId);
  } else {
    clearInterval(timerId);
  }
  timerId = 0;
};

/**
 * 订阅共享时钟。
 * @param {() => void} notify 每 tick 调用一次
 * @returns {() => void} 取消订阅；最后一个订阅者退订时定时器被清掉
 */
export const subscribeElapsedTick = (notify) => {
  subscribers.add(notify);
  if (!timerId) {
    startTicking();
  }

  return () => {
    subscribers.delete(notify);
    if (subscribers.size === 0) {
      stopTicking();
    }
  };
};

/** 仅供测试断言「没有订阅者时不留定时器」。 */
export const __getTickerState = () => ({ subscriberCount: subscribers.size, running: timerId !== 0 });
