// ============================================
// LifecyclePanel - 请求生命周期面板（方案 F2）
// 2026-08-13
// 2026-10-07 桌面化：去掉卡片外框，作为详情面板的一栏；
//            耗时标签 + 分段耗时条 + 图例，下方「调度 / 隐私 / 重试」属性行。
//            原来的「计费输入」格移到详情面板的「Token 与成本」。
// 组件不持有调度器；拉取由 useRequestLifecycleDetail 负责。
// ============================================

import { useState } from 'react';
import { ChevronDown, ChevronUp } from 'lucide-react';
import { Property, PropertyList } from '@components/desktop/Inspector.jsx';
import { buildLifecycleSegments, countAccountSwitches, lifecycleSegmentColors } from '../utils/lifecycle.js';
import { formatRouteMode } from '../utils/requestPresentation.js';
import { UNSETTLED_STATUSES } from '../utils/constants.js';
import {
  calculateTokensPerSecond,
  formatOptionalTimingBadge,
  formatTimingBadge,
  formatTpsBadge,
  resolveCompletionMs,
  resolveFirstResponseMs
} from '../utils/timing.js';

const EMPTY_CANDIDATES = [];
const FALLBACK_SEGMENT_COLOR = 'bg-slate-200 dark:bg-slate-600';
const TIMING_CHIP = 'inline-flex items-center rounded border px-1.5 py-px';

// 加载骨架：详情接口返回前的占位，不是「进行中」标识
const LoadingLine = () => <span className="block h-3 w-24 animate-pulse rounded bg-surface-mut" />;
const DashText = () => <span className="text-fg-subtle">—</span>;

// 顶部耗时标签：首字 / 生成 / 总耗 / TPS。
// 未落定时只给首字：后端只在终态写 duration，此刻的 0 是「还不知道」，算出来的生成 / 总耗 / TPS 都是假值。
const TimingChips = ({ request }) => {
  const settled = !UNSETTLED_STATUSES.has(request.status);
  const firstResponseMs = settled
    ? resolveFirstResponseMs(request.firstTokenMs, request.duration, request.isStreaming)
    : (Number.isFinite(request.firstTokenMs) ? Math.max(request.firstTokenMs, 0) : null);
  const completionMs = settled && Number.isFinite(firstResponseMs)
    ? resolveCompletionMs(request.completionMs, request.duration, firstResponseMs, request.isStreaming)
    : null;
  const tokensPerSecond = calculateTokensPerSecond(request.outputTokens, completionMs);

  return (
    <div className="flex flex-wrap items-center gap-1 font-mono text-[11px]">
      {Number.isFinite(firstResponseMs) && <span className={`tone-violet ${TIMING_CHIP}`}>首字 {formatOptionalTimingBadge(firstResponseMs)}</span>}
      {Number.isFinite(completionMs) && <span className={`tone-emerald ${TIMING_CHIP}`}>生成 {formatOptionalTimingBadge(completionMs)}</span>}
      {settled
        ? <span className={`tone-slate ${TIMING_CHIP}`}>总耗 {formatTimingBadge(request.duration)}</span>
        : <span className="font-sans text-xs text-fg-subtle">进行中，完成后显示完整耗时</span>}
      {Number.isFinite(tokensPerSecond) && <span className={`tone-slate ${TIMING_CHIP}`}>TPS {formatTpsBadge(tokensPerSecond)}</span>}
    </div>
  );
};

// 分段耗时条：块宽按耗时比例伸展；栏宽有限，放不下的标签截断，完整信息见 title 与图例
const SegmentBar = ({ segments, totalMs }) => (
  <>
    <div className="mt-2 flex h-6 items-stretch gap-0.5 overflow-hidden">
      {segments.map((segment) => {
        const pct = (segment.ms / totalMs) * 100;
        // 栏宽只有三百多像素：占比不到四分之一的段只显示时间，名称见图例；
        // 终态段 label 为短标签，原始错误全文经 detail 走 tooltip
        const showLabel = pct >= 25;
        const tooltip = `${segment.label} ${formatTimingBadge(segment.ms)}${segment.detail ? `\n${segment.detail}` : ''}`;
        return (
          <div
            key={segment.key}
            className={`${lifecycleSegmentColors[segment.key] || FALLBACK_SEGMENT_COLOR} flex min-w-12 items-center justify-center overflow-hidden rounded px-1`}
            style={{ flexGrow: Math.max(segment.ms, 1), flexBasis: 0 }}
            title={tooltip}
          >
            <span className="truncate font-mono text-[10.5px] font-medium text-slate-700/90 dark:text-slate-100">
              {showLabel ? `${segment.label} ${formatTimingBadge(segment.ms)}` : formatTimingBadge(segment.ms)}
            </span>
          </div>
        );
      })}
    </div>
    <div className="mt-1.5 flex flex-wrap items-center gap-x-3 gap-y-1 text-[11px] text-fg-muted">
      {segments.map((segment) => (
        <span key={segment.key} className="inline-flex min-w-0 items-center gap-1.5" title={segment.detail || undefined}>
          <span className={`h-2 w-2 shrink-0 rounded-sm ${lifecycleSegmentColors[segment.key] || FALLBACK_SEGMENT_COLOR}`} />
          <span className="max-w-[180px] truncate">{segment.label}</span>
        </span>
      ))}
    </div>
  </>
);

// 候选决策列表：账号池为候选账号，端点为端点决策
const DecisionList = ({ rows }) => (
  <div className="mt-2 max-h-40 divide-y divide-line-soft overflow-y-auto rounded-md border border-line-soft bg-surface">
    {rows.length === 0 && <div className="px-2 py-1 text-xs text-fg-subtle">无候选</div>}
    {rows.map((row) => (
      <div key={row.key} className="flex items-center justify-between gap-2 px-2 py-1">
        <span className="truncate text-xs text-fg-body">{row.name}</span>
        <span className="shrink-0 text-[11px] text-fg-subtle">{row.outcome}</span>
      </div>
    ))}
  </div>
);

const toAccountRows = (candidates) => candidates.map((candidate, index) => ({
  key: `${candidate.account_id || candidate.accountId || index}-${index}`,
  name: candidate.account_name || candidate.accountName || candidate.account_id || '—',
  outcome: candidate.runtime_outcome || candidate.runtimeOutcome || candidate.decision || candidate.reason || ''
}));

const toEndpointRows = (decisions) => decisions.map((decision, index) => ({
  key: `${decision.name || index}-${index}`,
  name: decision.name || '—',
  outcome: decision.runtime_outcome || decision.runtimeOutcome || decision.decision || decision.reason || ''
}));

// 调度一行的正文：账号池 = 命中账号 · 档位；端点 = 路由方式 · 模型 / 命中端点 · 结果；无快照时退回请求自带的路由信息
const ScheduleValue = ({ request, snapshot, isAccountRequest }) => {
  if (snapshot && isAccountRequest) {
    const selectedName = snapshot.selected_account_name || snapshot.selectedAccountName || '';
    const selectedReason = snapshot.selected_tier_label || snapshot.selectedTierLabel || '';
    return <div className="truncate text-fg">{selectedName || '—'}{selectedReason && <span className="text-fg-muted"> · {selectedReason}</span>}</div>;
  }
  if (snapshot) {
    // upstream_group 出现即说明这条请求走了模型路由。组名是后端自动维护的实现细节
    // （历史数据里还可能是旧版手工建的组名），不给用户看，只标明路由方式；
    // 具体模型由紧随其后的 request_model 呈现。
    const routedByModelRouting = Boolean(
      snapshot.upstream_group || snapshot.upstreamGroup ||
      snapshot.route_scope_group_name || snapshot.routeScopeGroupName
    );
    const snapshotModel = snapshot.request_model || snapshot.requestModel;
    const endpointSelected = snapshot.selected_endpoint || snapshot.selectedEndpoint || request.effectiveEndpoint || '';
    const finalOutcome = snapshot.final_outcome || snapshot.finalOutcome;
    return (
      <>
        <div className="truncate text-fg">{routedByModelRouting ? '模型路由' : '全局端点'}{snapshotModel && <span className="text-fg-muted"> · {snapshotModel}</span>}</div>
        <div className="truncate text-xs text-fg-muted">{endpointSelected || '—'}{finalOutcome ? ` · ${finalOutcome}` : null}</div>
      </>
    );
  }
  if (isAccountRequest) return <DashText />;
  return (
    <>
      <div className="truncate text-fg">路由 {formatRouteMode(request.routeMode)}</div>
      <div className="truncate text-xs text-fg-muted">
        {request.effectiveEndpoint || request.upstreamName || '—'}
        {request.fallbackReason ? <span className="text-warn"> · 回退</span> : null}
      </div>
    </>
  );
};

const PrivacyValue = ({ privacyScan }) => {
  const actionLabel = { redact: '已脱敏', detect: '仅检测', blocked: '已拦截' }[privacyScan?.action];
  if (!privacyScan || !actionLabel) return <DashText />;
  return (
    <>
      <span className={privacyScan.action === 'blocked' ? 'text-danger' : 'text-success'}>
        {actionLabel} · {privacyScan.hit_count ?? 0} 规则命中
      </span>
      {Number(privacyScan.scan_count) > 1 && <span className="ml-1 text-xs text-fg-subtle">共扫描 {privacyScan.scan_count} 次</span>}
      {privacyScan.blocked_code && <div className="text-xs text-danger">{privacyScan.blocked_code}</div>}
    </>
  );
};

const LifecyclePanel = ({ request = {}, lifecycle = null, lifecycleLoading = false }) => {
  const [scheduleExpanded, setScheduleExpanded] = useState(false);
  const segments = buildLifecycleSegments(request);
  // 进行中 duration 还是 0：按已有分段之和算占比，否则每段都会被当成「大段」而挂上被截断的长标签
  const totalMs = Math.max(request.duration || 0, segments.reduce((sum, segment) => sum + segment.ms, 0), 1);

  const snapshot = lifecycle?.scheduleSnapshot || null;
  const isAccountRequest = request.upstreamType === 'account' || request.requestFamily === 'codex';
  const candidates = Array.isArray(snapshot?.candidates) ? snapshot.candidates : EMPTY_CANDIDATES;
  const endpointDecisions = Array.isArray(snapshot?.decisions) ? snapshot.decisions : EMPTY_CANDIDATES;
  // 账号池快照总能展开（空候选显示「无候选」）；端点快照有决策才给展开入口
  const decisionRows = snapshot ? (isAccountRequest ? toAccountRows(candidates) : toEndpointRows(endpointDecisions)) : EMPTY_CANDIDATES;
  const canExpand = Boolean(snapshot) && (isAccountRequest || endpointDecisions.length > 0);
  const accountSwitchCount = countAccountSwitches(candidates);

  return (
    <div>
      <TimingChips request={request} />
      <SegmentBar segments={segments} totalMs={totalMs} />

      <div className="mt-3">
        <PropertyList labelWidth={40} align="start">
          <Property label="调度" truncate={false}>
            {lifecycleLoading ? <LoadingLine /> : <ScheduleValue request={request} snapshot={snapshot} isAccountRequest={isAccountRequest} />}
          </Property>
          <Property label="隐私" truncate={false}>
            {lifecycleLoading ? <LoadingLine /> : <PrivacyValue privacyScan={lifecycle?.privacyScan || null} />}
          </Property>
          <Property label="重试" truncate={false}>
            <span className="font-mono">{request.retryCount ?? 0}</span> 次
            {isAccountRequest && accountSwitchCount > 0 && <span className="ml-1 text-xs text-fg-muted">换号 {accountSwitchCount} 次</span>}
            {!isAccountRequest && request.requestedEndpoint && request.effectiveEndpoint
              && request.requestedEndpoint !== request.effectiveEndpoint && (
              <span className="ml-1 text-xs text-warn">故障转移</span>
            )}
          </Property>
        </PropertyList>
        {!lifecycleLoading && canExpand && (
          <button
            type="button"
            onClick={() => setScheduleExpanded((value) => !value)}
            className="ml-[50px] mt-1.5 inline-flex items-center gap-1 text-xs text-accent hover:text-accent-fg"
          >
            {scheduleExpanded ? <ChevronUp className="h-3.5 w-3.5" /> : <ChevronDown className="h-3.5 w-3.5" />}
            候选决策（{decisionRows.length}）
          </button>
        )}
        {!lifecycleLoading && canExpand && scheduleExpanded && <DecisionList rows={decisionRows} />}
      </div>
    </div>
  );
};

export default LifecyclePanel;
