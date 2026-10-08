// ============================================
// StatsOverview - 统计概览组件
// 2025-12-01 11:11:13
// 2026-08-28: 指标数值改为平滑过渡，避免实时刷新时数字硬跳
// 2026-10-07: 桌面化，六张指标卡收进底部状态栏；总请求数由工具栏副标题承担，这里不再重复
// ============================================

import { StatusItem } from '@components/desktop/StatusBar.jsx';
import useCountUp from '@hooks/useCountUp.js';

/**
 * 格式化Token数量（转换为M单位）
 */
const formatTokens = (tokens) => {
  const numericTokens = Number(tokens) || 0;
  if (numericTokens === 0) return '0.00M';
  const inMillions = numericTokens / 1000000;
  return `${inMillions.toFixed(2)}M`;
};

/**
 * 格式化成本
 */
const formatCost = (cost) => {
  const num = parseFloat(cost);
  if (!cost || isNaN(num)) return '$0.00';
  return `$${num.toFixed(2)}`;
};

/**
 * 格式化持续时间
 */
const formatDuration = (duration) => {
  if (!duration || duration === 0) return '-';
  const ms = parseFloat(duration);
  if (isNaN(ms)) return '-';
  if (ms >= 1000) {
    return `${(ms / 1000).toFixed(1)}s`;
  } else {
    return `${Math.round(ms)}ms`;
  }
};

const formatCount = (value) => Math.round(value).toLocaleString();
const formatPercent = (value) => `${value.toFixed(1)}%`;

/**
 * CountUpItem - 状态栏里走 count-up 的单项指标
 * value 不是有限数值时显示 fallback。
 */
const CountUpItem = ({ label, value, format, fallback, valueClassName, title }) => {
  const animatedValue = useCountUp(value);
  const display = Number.isFinite(animatedValue) ? format(animatedValue) : fallback;
  return <StatusItem label={label} value={display} valueClassName={valueClassName} title={title} />;
};

/**
 * StatsOverview - 统计概览（渲染为状态栏条目，需放在 <StatusBar> 内）
 * 平均耗时会在 ms / s 之间切换单位，滚动过程中单位跳变很难看，
 * 改成值变化时用 key 重挂载触发一次短高亮。
 * @param {Object} props
 * @param {Object} props.stats - 统计数据
 */
const StatsOverview = ({ stats }) => {
  if (!stats) return null;

  const successRate = Number.isFinite(stats.success_rate) ? stats.success_rate : null;
  const avgDuration = formatDuration(stats.avg_duration_ms);  // 注意：字段是 avg_duration_ms
  const totalCost = parseFloat(stats.total_cost_usd);
  const totalTokens = Number(stats.total_tokens) || 0;
  const failedRequests = stats.failed_requests || 0;
  const interruptedRequests = stats.interrupted_requests;
  const failedHint = Number.isFinite(interruptedRequests)
    ? `另有 ${formatCount(interruptedRequests)} 次中断 · 不计为成功或失败`
    : undefined;

  return (
    <>
      <CountUpItem label="成功率" value={successRate} format={formatPercent} fallback="-%" />
      <span>
        平均耗时{' '}
        <b key={avgDuration} className="kpi-value-flash font-medium tabular-nums text-fg-body">{avgDuration}</b>
      </span>
      <CountUpItem label="Token" value={totalTokens} format={formatTokens} />
      <CountUpItem label="成本" value={Number.isFinite(totalCost) ? totalCost : 0} format={formatCost} />
      <CountUpItem
        label="失败"
        value={failedRequests}
        format={formatCount}
        valueClassName={failedRequests > 0 ? 'text-danger' : 'text-fg-body'}
        title={failedHint}
      />
    </>
  );
};

export default StatsOverview;
