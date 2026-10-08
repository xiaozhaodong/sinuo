// 订阅额度：请求页 Claude 浮层「接入模式」里的额度区，状态由 useClaudeSubscriptionRateLimit 提供。
// 5h / d7 取响应头与主动查询中较新的一份；按模型周额度与额外用量只有主动查询才有。与账号池一样展示剩余。
// 2026-10-07 桌面化：去掉外框，改为浮层的一组（小标题 + 「刷新」文字按钮 + 细条行 + 灰字说明）

import { Fragment } from 'react';
import { Loader2, RefreshCw } from 'lucide-react';
import { PopoverAlert, PopoverNote, PopoverSection } from '@components/desktop/PopoverParts.jsx';
import { useTimezone } from '@contexts/TimezoneContext.jsx';
import {
  describeSubscriptionExtraUsage,
  describeSubscriptionQuotaWindow,
  describeSubscriptionRateLimitAlert,
  describeSubscriptionRefresh,
  describeSubscriptionSource,
  hasSubscriptionQuotaData,
  resolveSubscriptionWindows,
  SUBSCRIPTION_QUOTA_WINDOWS
} from './subscriptionRateLimit.js';

const describeWindowText = (view, formatMonthDayTime) => {
  if (view.state === 'reset') return '已重置，等待新数据';
  if (view.state === 'unknown') return '官方未返回';
  const resetText = view.resetAt ? ` · ${formatMonthDayTime(view.resetAt)} 重置` : '';
  return `${view.remainingPercent.toFixed(0)}%${resetText}`;
};

const describeRefreshTitle = (refreshState, formatTimeOnly) => {
  if (refreshState.reason === 'refreshing') return '正在查询官方额度';
  if (refreshState.reason === 'no_credential') return '没有可用的登录凭据：凭据只在最近一次订阅直通请求后保留 30 分钟';
  if (refreshState.reason === 'cooldown') return `为避免频繁请求，${formatTimeOnly(refreshState.availableAt)} 后可再次刷新`;
  return '查询官方最新额度（含 claude.ai 网页等其他客户端的用量）';
};

// 三列网格让各行标签等宽对齐，模型名比 5h / d7 长；左缩进与浮层行文字对齐
const QuotaRows = ({ rows, nowMs, formatMonthDayTime }) => (
  <div className="grid grid-cols-[auto_minmax(0,1fr)_auto] items-center gap-x-2 gap-y-1.5 py-1 pl-[26px] pr-2">
    {rows.map(({ key, label, window }) => {
      const view = describeSubscriptionQuotaWindow(window, nowMs);
      return (
        <Fragment key={key}>
          <span className="text-[11px] text-fg-subtle">{label}</span>
          <div className="h-1.5 overflow-hidden rounded-full bg-surface-mut">
            <div className={`h-full rounded-full transition-all duration-500 ${view.progressClass}`} style={{ width: `${view.remainingPercent ?? 0}%` }} />
          </div>
          <span className="whitespace-nowrap text-right text-[11px] font-medium text-fg-body">{describeWindowText(view, formatMonthDayTime)}</span>
        </Fragment>
      );
    })}
  </div>
);

const RateLimitAlert = ({ alert, formatMonthDayTime }) => {
  const details = [alert.claimLabel, alert.resetAt && `${formatMonthDayTime(alert.resetAt)} 重置`].filter(Boolean);
  return (
    <PopoverAlert tone={alert.tone === 'danger' ? 'danger' : 'warn'}>
      {alert.title}{details.length > 0 && `（${details.join('，')}）`}
    </PopoverAlert>
  );
};

const SubscriptionRateLimitPanel = ({ quota, error, refreshing, nowMs, onRefresh }) => {
  const { formatMonthDayTime, formatTimeOnly } = useTimezone();
  const windows = resolveSubscriptionWindows(quota);
  const alert = describeSubscriptionRateLimitAlert(quota?.headers, nowMs);
  const refreshState = describeSubscriptionRefresh(quota, nowMs, refreshing);
  const extraUsageText = describeSubscriptionExtraUsage(quota?.usage?.extraUsage);
  const rows = [
    ...SUBSCRIPTION_QUOTA_WINDOWS.map(({ key, label }) => ({ key, label, window: windows[key] })),
    ...(quota?.usage?.modelWeekly || []).map((item) => ({ key: `model-${item.label}`, label: `${item.label} d7`, window: item }))
  ];
  const errorText = error || quota?.lastError;

  const refreshButton = (
    <button
      type="button"
      onClick={onRefresh}
      disabled={refreshState.disabled}
      title={describeRefreshTitle(refreshState, formatTimeOnly)}
      className="flex items-center gap-1 text-[11.5px] text-accent hover:text-accent-fg disabled:text-fg-subtle"
    >
      {refreshing ? <Loader2 size={11} className="animate-spin" /> : <RefreshCw size={11} />}刷新
    </button>
  );

  return (
    <div>
      <PopoverSection title="订阅额度" aside={refreshButton} />
      {hasSubscriptionQuotaData(quota) ? (
        <>
          {alert && <RateLimitAlert alert={alert} formatMonthDayTime={formatMonthDayTime} />}
          <QuotaRows rows={rows} nowMs={nowMs} formatMonthDayTime={formatMonthDayTime} />
          {extraUsageText && <PopoverNote tone="muted">额外用量：{extraUsageText}</PopoverNote>}
          <PopoverNote>
            显示剩余比例，更新于 {formatMonthDayTime(windows.updatedAt)}（{describeSubscriptionSource(windows.source)}）。订阅请求结束约 3 分钟后自动同步一次官方额度，网页等其他客户端的用量届时补上。
          </PopoverNote>
        </>
      ) : (
        <PopoverNote>发起一次订阅直通请求后显示。额度只保存在内存里，重启应用后需要重新请求。</PopoverNote>
      )}
      {errorText && <PopoverNote tone="danger"><span role="alert">{errorText}</span></PopoverNote>}
    </div>
  );
};

export default SubscriptionRateLimitPanel;
