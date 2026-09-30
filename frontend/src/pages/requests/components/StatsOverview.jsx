// ============================================
// StatsOverview - 统计概览组件
// 2025-12-01 11:11:13
// 2026-08-28: 指标数值改为平滑过渡，避免实时刷新时数字硬跳
// ============================================

import {
  Zap,
  CheckCircle2,
  Clock,
  DollarSign,
  FileText,
  XCircle
} from 'lucide-react';
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
 * KpiCard - 单个指标卡
 * value 为有限数值时走 count-up；staticText 用于不适合滚动的指标
 * （平均耗时会在 ms / s 之间切换单位，滚动过程中单位跳变很难看，
 *  改成值变化时用 key 重挂载触发一次短高亮）。
 * hint 只走 hover title，不占卡片高度：六张卡同排等高，任何一张多出一行
 * 都会把整排撑高、其余卡片底部留白。
 */
const KpiCard = ({ label, value, format, fallback, staticText, hint, icon: Icon, tone }) => {
  const animatedValue = useCountUp(value);
  const isStatic = staticText !== undefined;
  const hasValue = Number.isFinite(animatedValue);
  const display = isStatic ? staticText : (hasValue ? format(animatedValue) : fallback);

  return (
    <div
      className="bg-surface p-4 rounded-xl shadow-sm border border-line/60 hover:border-accent-line transition-all group"
      title={hint || undefined}
    >
      <div className="flex items-center justify-between mb-3">
        <span className="text-xs font-medium text-fg-muted">{label}</span>
        <div className={`p-1.5 rounded-md ${tone}`}>
          <Icon className="w-3.5 h-3.5" />
        </div>
      </div>
      <div className="flex items-baseline gap-1">
        <span
          key={isStatic ? display : undefined}
          className={`text-xl font-bold text-fg tracking-tight tabular-nums ${isStatic ? 'kpi-value-flash' : ''}`}
        >
          {display}
        </span>
      </div>
    </div>
  );
};

/**
 * StatsOverview - 统计概览组件
 * @param {Object} props
 * @param {Object} props.stats - 统计数据
 * @param {number} props.total - 总记录数（来自分页）
 */
const StatsOverview = ({ stats, total = 0, isBlurred = false }) => {
  if (!stats) return null;

  // 数值与格式化分离：count-up 需要拿到原始数值，展示时再套格式。
  const totalRequests = stats.total_requests || total || 0;
  const successRate = Number.isFinite(stats.success_rate) ? stats.success_rate : null;
  const avgDuration = formatDuration(stats.avg_duration_ms);  // 注意：字段是 avg_duration_ms
  const totalCost = parseFloat(stats.total_cost_usd);
  const totalTokens = Number(stats.total_tokens) || 0;
  const failedRequests = stats.failed_requests || 0;
  const interruptedRequests = stats.interrupted_requests;

  const kpis = [
    { label: '总请求数', value: totalRequests, format: formatCount, icon: Zap, tone: 'tone-orange' },
    { label: '成功率', value: successRate, format: formatPercent, fallback: '-%', icon: CheckCircle2, tone: 'tone-emerald' },
    { label: '平均耗时', staticText: avgDuration, icon: Clock, tone: 'tone-amber' },
    { label: '总成本', value: Number.isFinite(totalCost) ? totalCost : 0, format: formatCost, icon: DollarSign, tone: 'tone-blue' },
    { label: '总Token数 (M)', value: totalTokens, format: formatTokens, icon: FileText, tone: 'tone-indigo' },
    {
      label: '失败请求', value: failedRequests, format: formatCount, icon: XCircle, tone: 'tone-rose',
      hint: Number.isFinite(interruptedRequests) ? `另有 ${formatCount(interruptedRequests)} 次中断 · 不计为成功或失败` : null
    }
  ];

  return (
    <div className={`grid grid-cols-2 md:grid-cols-3 lg:grid-cols-6 gap-4 transition-all duration-300 ${isBlurred ? 'opacity-40 pointer-events-none blur-[1px]' : ''}`}>
      {kpis.map((kpi) => (
        <KpiCard key={kpi.label} {...kpi} />
      ))}
    </div>
  );
};

export default StatsOverview;
