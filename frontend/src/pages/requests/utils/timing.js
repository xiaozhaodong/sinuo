export const TIMING_THRESHOLDS_MS = Object.freeze({
  first: {
    warning: 8000,
    critical: 15000
  },
  duration: {
    warning: 30000,
    critical: 60000
  }
});

// 耗时 pill（已定值与跑秒共用）的尺寸与字形，与模型标签对齐：24px 高、6px 圆角、13px 等宽；
// 配色由下方 getTimingPillClassName / getRunningPillClassName 给出
export const TIMING_PILL_BASE = 'inline-flex items-center px-2 py-[3px] rounded-md text-[13px] leading-4 font-mono font-medium border transition-all';

const toSafeMs = (ms) => (Number.isFinite(ms) ? Math.max(ms, 0) : 0);

/**
 * 耗时的唯一格式。跑秒中和定稿后共用同一把尺子 —— 精度在完成瞬间变化
 * 会让人以为数据源换了，而「完成」本来就由灰→彩负责表达，不需要数字帮腔。
 *
 * 1 秒内直接给毫秒整数：这一段大多是本地处理/连接，340ms 比 0.34s 好读。
 * 10 秒后收回一位小数：末位那时已经没人读了，跳动只剩噪声，也顺带压住了列宽。
 *
 * 中间那档两位小数是跑秒的主场 —— 0.1s 的刻度每秒只跳 10 次，看着像在等；
 * 0.01s 的末位滚起来才是秒表。三档宽度都是 5 个等宽字符（999ms / 1.00s / 10.0s）。
 */
export const formatTimingBadge = (ms) => {
  const value = toSafeMs(ms);
  if (value < 1000) {
    return `${Math.round(value)}ms`;
  }
  return `${(value / 1000).toFixed(value < 10000 ? 2 : 1)}s`;
};

export const formatOptionalTimingBadge = (ms) => (
  Number.isFinite(ms) ? formatTimingBadge(ms) : '-'
);

export const calculateGenerationMs = (durationMs, firstTokenMs) => {
  if (!Number.isFinite(durationMs) || !Number.isFinite(firstTokenMs)) {
    return null;
  }
  return Math.max(durationMs - firstTokenMs, 0);
};

export const resolveFirstResponseMs = (firstTokenMs, durationMs, isStreaming) => {
  if (Number.isFinite(firstTokenMs)) {
    return Math.max(firstTokenMs, 0);
  }
  if (!isStreaming && Number.isFinite(durationMs)) {
    return Math.max(durationMs, 0);
  }
  return null;
};

export const resolveCompletionMs = (completionMs, durationMs, firstTokenMs, isStreaming = true) => (
  Number.isFinite(completionMs)
    ? Math.max(completionMs, 0)
    : (!isStreaming && Number.isFinite(firstTokenMs)
      ? 0
      : calculateGenerationMs(durationMs, firstTokenMs))
);

export const calculateTokensPerSecond = (outputTokens, generationMs) => {
  if (!Number.isFinite(outputTokens) || !Number.isFinite(generationMs) || generationMs <= 0) {
    return null;
  }
  return Math.max(outputTokens, 0) / (generationMs / 1000);
};

export const formatTpsBadge = (tokensPerSecond) => {
  if (!Number.isFinite(tokensPerSecond)) {
    return '-';
  }
  return tokensPerSecond >= 100
    ? tokensPerSecond.toFixed(0)
    : tokensPerSecond.toFixed(1);
};

export const getTimingPillClassName = (type, ms) => {
  const value = toSafeMs(ms);
  const thresholds = type === 'first'
    ? TIMING_THRESHOLDS_MS.first
    : TIMING_THRESHOLDS_MS.duration;

  if (value > thresholds.critical) {
    return 'tone-rose';
  }
  if (value > thresholds.warning) {
    return type === 'first' ? 'tone-amber' : 'tone-orange';
  }
  return 'tone-emerald';
};

/**
 * 跑秒中的色阶。与 getTimingPillClassName 只差「正常区间」那一档：
 * 绿色表示「这个数字定了且健康」，还在跑的数字不该提前占用这个语义。
 *
 * 收敛成一条规则：灰 = 这一段还在跑，彩 = 这一段定了。
 * 完成瞬间的灰→彩就是耗时列最强的完成信号 —— 不这么分的话，
 * 进行中和已完成都是两枚绿 pill，秒表停下来根本看不出来。
 *
 * 示警区间照常变黄/橙/红：卡住的时候「它卡住了」比「还没定稿」更该被看见。
 */
export const getRunningPillClassName = (type, ms) => {
  const className = getTimingPillClassName(type, ms);
  return className === 'tone-emerald' ? 'tone-slate' : className;
};

/**
 * 首响位「尚未发到上游」等待点的阈值（从请求进入算起）。
 * 本地处理、建连大多零点几秒就过去：1 秒内保持静止 —— 一圈动画都走不完，动了反而像闪；
 * 超过 1 秒才开始动，说明真的卡在发出前；再久按跑秒同样的语义变黄 / 变红。
 */
export const PENDING_SEND_THRESHOLDS_MS = Object.freeze({
  animate: 1000,
  warning: 3000,
  critical: 10000
});

/**
 * @param {number} waitingMs 从请求进入到现在的毫秒数
 * @returns {{ tone: string, animate: boolean }}
 */
export const getPendingSendPillState = (waitingMs) => {
  const value = toSafeMs(waitingMs);
  const animate = value >= PENDING_SEND_THRESHOLDS_MS.animate;
  if (value > PENDING_SEND_THRESHOLDS_MS.critical) {
    return { tone: 'tone-rose', animate };
  }
  if (value > PENDING_SEND_THRESHOLDS_MS.warning) {
    return { tone: 'tone-amber', animate };
  }
  return { tone: 'tone-slate', animate };
};
