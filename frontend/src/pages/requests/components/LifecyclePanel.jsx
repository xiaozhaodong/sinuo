// ============================================
// LifecyclePanel - 请求生命周期面板（方案 F2）
// 2026-08-13
// 2026-10-07 桌面化：去掉卡片外框，作为详情面板的一栏；
// 2026-11-20 布局精简：10px 纯色分段条 + 悬浮联动图例 + 克制指标行，
//            总耗时置顶至区段右上角，候选决策内联进调度行，链路轨迹通栏展开。
// 组件不持有调度器；拉取由 useRequestLifecycleDetail 负责。
// ============================================

import { useState } from 'react';
import { ChevronDown, ChevronUp } from 'lucide-react';
import { Property, PropertyList } from '@components/desktop/Inspector.jsx';
import { buildLifecycleSegments, countAccountSwitches, railSegmentColors, requestPrivacySummary, responsePrivacySummary } from '../utils/lifecycle.js';
import { retryPropertyVisible } from '../utils/retryTiming.js';
import { formatRouteMode } from '../utils/requestPresentation.js';
import { UNSETTLED_STATUSES } from '../utils/constants.js';
import { firstContentDelayMs, requestSpeedMetrics } from '../utils/streamPhases.js';
import { summarizeFlowIntegrity } from '../utils/requestFlow.js';
import RequestFlowTrail from './RequestFlowTrail.jsx';
import StreamPhaseProperties from './StreamPhaseProperties.jsx';
import PrivacyWarningBadge from './PrivacyWarningBadge.jsx';
import {
  formatOptionalTimingBadge,
  formatTimingBadge,
  resolveCompletionMs,
  resolveFirstResponseMs
} from '../utils/timing.js';

const EMPTY_CANDIDATES = [];
const FALLBACK_SEGMENT_COLOR = 'bg-slate-300 dark:bg-slate-500';

// 加载骨架：详情接口返回前的占位，不是「进行中」标识
const LoadingLine = () => <span className="block h-3 w-24 animate-pulse rounded bg-surface-mut" />;
const DashText = () => <span className="text-fg-subtle">—</span>;

// 轻量指标条：计时与列表同口径，生成吞吐与流式输出速度各自独立；
// 去掉多色描边胶囊堆叠，仅在集中下发等异常态保留警示着色。
const TimingSummary = ({ request }) => {
  const settled = !UNSETTLED_STATUSES.has(request.status);
  const contentDelayMs = firstContentDelayMs(request);
  let firstResponseMs = Number.isFinite(request.firstTokenMs) ? Math.max(request.firstTokenMs, 0) : null;
  if (settled) firstResponseMs = resolveFirstResponseMs(request.firstTokenMs, request.duration, request.isStreaming);
  let completionMs = null;
  if (settled || Number.isFinite(request.completionMs)) {
    if (Number.isFinite(firstResponseMs)) {
      completionMs = resolveCompletionMs(request.completionMs, request.duration, firstResponseMs, request.isStreaming);
    }
  }

  const metrics = [
    Number.isFinite(firstResponseMs) && {
      key: 'first',
      label: '首响',
      value: formatOptionalTimingBadge(firstResponseMs),
      title: '写完请求到上游第一个事件（受理、排队）'
    },
    contentDelayMs !== null && {
      key: 'content',
      label: '首字',
      value: formatOptionalTimingBadge(contentDelayMs),
      title: '写完请求到首个内容（含思考或等待）'
    },
    Number.isFinite(completionMs) && {
      key: 'completion',
      label: '生成',
      value: formatOptionalTimingBadge(completionMs),
      title: '上游第一个事件到流完成'
    },
    ...requestSpeedMetrics(request).map((metric) => ({
      key: metric.label,
      label: metric.label,
      value: metric.value,
      title: metric.title,
      warn: metric.tone === 'tone-amber'
    }))
  ].filter(Boolean);

  if (metrics.length === 0 && settled) return null;

  return (
    <div data-timing-summary className="mt-2 flex flex-wrap items-center gap-x-3.5 gap-y-1 rounded-md bg-surface-sub px-2.5 py-1.5 text-[11.5px] text-fg-muted">
      {metrics.map((item) => (
        <span
          key={item.key}
          data-timing-metric={item.key}
          className={`inline-flex items-center gap-1 ${item.warn ? 'text-warn' : ''}`}
          title={item.title}
        >
          <span>{item.label}</span>
          <span className={`font-mono font-medium tabular-nums ${item.warn ? 'text-warn' : 'text-fg'}`}>
            {item.value}
          </span>
        </span>
      ))}
      {!settled && <span className="text-fg-subtle">进行中，完成后显示完整耗时</span>}
    </div>
  );
};

// 10px 纯色分段耗时条：与列表 TraceRail 同色系，支持与下方图例悬停联动。
const SegmentBar = ({ segments, hoveredKey, onHoverSegment }) => {
  if (segments.length === 0) return null;
  return (
    <>
      <div className="flex h-2.5 items-stretch gap-0.5 overflow-hidden rounded-full bg-surface-mut">
        {segments.map((segment) => {
          const tooltip = `${segment.label} ${formatTimingBadge(segment.ms)}${segment.detail ? `\n${segment.detail}` : ''}`;
          const dimmed = hoveredKey && hoveredKey !== segment.key;
          return (
            <div
              key={segment.key}
              className={`${railSegmentColors[segment.key] || FALLBACK_SEGMENT_COLOR} min-w-[6px] first:rounded-l-full last:rounded-r-full transition-opacity duration-150 ${dimmed ? 'opacity-45' : ''}`}
              style={{ flexGrow: Math.max(segment.ms, 1), flexBasis: 0 }}
              title={tooltip}
              onMouseEnter={() => onHoverSegment(segment.key)}
              onMouseLeave={() => onHoverSegment(null)}
            />
          );
        })}
      </div>
      <div data-lifecycle-legend className="mt-2 flex flex-wrap items-center gap-x-3 gap-y-1 text-[11.5px] text-fg-muted">
        {segments.map((segment) => {
          const highlighted = hoveredKey === segment.key;
          return (
            <span
              key={segment.key}
              className={`inline-flex min-w-0 items-center gap-1.5 transition-colors duration-150 ${highlighted ? 'font-semibold text-fg' : ''}`}
              title={segment.detail || undefined}
              onMouseEnter={() => onHoverSegment(segment.key)}
              onMouseLeave={() => onHoverSegment(null)}
            >
              <span className={`h-2 w-2 shrink-0 rounded-full ${railSegmentColors[segment.key] || FALLBACK_SEGMENT_COLOR}`} />
              <span className="max-w-[180px] truncate">{segment.label}</span>
              <span className="font-mono tabular-nums text-fg-body">{formatTimingBadge(segment.ms)}</span>
            </span>
          );
        })}
      </div>
    </>
  );
};

// 候选决策列表：账号池为候选账号，端点为端点决策
const DecisionList = ({ rows }) => (
  <div className="mt-2 max-h-40 divide-y divide-line-soft overflow-y-auto rounded-md border border-line bg-surface">
    {rows.length === 0 && <div className="px-2.5 py-1.5 text-xs text-fg-subtle">无候选</div>}
    {rows.map((row) => (
      <div key={row.key} className="flex items-center justify-between gap-2 px-2.5 py-1.5">
        <span className="truncate font-mono text-xs text-fg-body">{row.name}</span>
        <span className="shrink-0 text-[11.5px] text-fg-muted">{row.outcome}</span>
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

// 调度一行的正文：首行右侧可内联挂候选决策展开入口，消除外部硬缩进错位。
const ScheduleValue = ({ request, snapshot, isAccountRequest, expandButton = null }) => {
  if (snapshot && isAccountRequest) {
    const selectedName = snapshot.selected_account_name || snapshot.selectedAccountName || '';
    const selectedReason = snapshot.selected_tier_label || snapshot.selectedTierLabel || '';
    return (
      <div className="flex flex-wrap items-center justify-between gap-x-2 gap-y-0.5">
        <span className="min-w-0 truncate text-fg">{selectedName || '—'}{selectedReason && <span className="text-fg-muted"> · {selectedReason}</span>}</span>
        {expandButton}
      </div>
    );
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
        <div className="flex flex-wrap items-center justify-between gap-x-2 gap-y-0.5">
          <span className="min-w-0 truncate text-fg">{routedByModelRouting ? '模型路由' : '全局端点'}{snapshotModel && <span className="text-fg-muted"> · {snapshotModel}</span>}</span>
          {expandButton}
        </div>
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

const PrivacyValue = ({ privacyScan, warnings }) => {
  const scan = requestPrivacySummary(privacyScan);
  const restore = responsePrivacySummary(privacyScan?.restore);
  if (!scan && !restore && !warnings?.length) return <DashText />;
  return (
    <dl className="grid grid-cols-[auto_1fr] items-baseline gap-x-2 gap-y-0.5">
      {warnings?.length > 0 && <><dt className="text-xs text-fg-muted">异常记录</dt><dd><PrivacyWarningBadge codes={warnings} detailed /></dd></>}
      {scan && <><dt className="text-xs text-fg-muted">请求</dt><dd className={scan.tone}>{scan.text}</dd></>}
      {restore && <><dt className="text-xs text-fg-muted">响应</dt><dd className={restore.tone}>{restore.text}</dd></>}
    </dl>
  );
};

const LifecyclePanel = ({ request = {}, lifecycle = null, lifecycleLoading = false }) => {
  const [scheduleExpanded, setScheduleExpanded] = useState(false);
  const [hoveredSegmentKey, setHoveredSegmentKey] = useState(null);
  const settled = !UNSETTLED_STATUSES.has(request.status);
  const segments = buildLifecycleSegments(request).filter((segment) => settled || segment.key !== 'total');

  const snapshot = lifecycle?.scheduleSnapshot || null;
  const flow = lifecycle?.flow || null;
  const integrityWarnings = summarizeFlowIntegrity(lifecycle?.integrity);
  const isAccountRequest = request.upstreamType === 'account' || request.requestFamily === 'codex';
  const candidates = Array.isArray(snapshot?.candidates) ? snapshot.candidates : EMPTY_CANDIDATES;
  const endpointDecisions = Array.isArray(snapshot?.decisions) ? snapshot.decisions : EMPTY_CANDIDATES;
  // 账号池快照总能展开（空候选显示「无候选」）；端点快照有决策才给展开入口
  const decisionRows = snapshot ? (isAccountRequest ? toAccountRows(candidates) : toEndpointRows(endpointDecisions)) : EMPTY_CANDIDATES;
  const canExpand = Boolean(snapshot) && (isAccountRequest || endpointDecisions.length > 0);
  const accountSwitchCount = countAccountSwitches(candidates);
  const hasExpandableFlow = Boolean(flow && ((Array.isArray(flow.contacts) && flow.contacts.length > 0) || flow.downstream_delivery_done_offset_ms != null || flow.first_client_write_offset_ms != null));

  const expandButton = !lifecycleLoading && canExpand ? (
    <button
      type="button"
      onClick={() => setScheduleExpanded((value) => !value)}
      className="inline-flex shrink-0 items-center gap-0.5 text-xs font-medium text-accent hover:text-accent-fg"
    >
      候选决策（{decisionRows.length}）
      {scheduleExpanded ? <ChevronUp className="h-3.5 w-3.5" /> : <ChevronDown className="h-3.5 w-3.5" />}
    </button>
  ) : null;

  return (
    <div>
      <div className="mb-2 flex items-baseline justify-between gap-2">
        <h3 className="text-[11.5px] font-semibold tracking-[0.02em] text-fg-body">生命周期</h3>
        <span data-lifecycle-total className={`inline-flex items-baseline gap-1 text-xs font-semibold ${settled ? 'text-fg' : 'text-fg-subtle'}`}>
          <span className="font-normal text-fg-muted">总耗时</span>
          <span className={settled ? 'font-mono tabular-nums' : undefined}>{settled ? formatTimingBadge(request.duration) : '进行中'}</span>
        </span>
      </div>

      <SegmentBar segments={segments} hoveredKey={hoveredSegmentKey} onHoverSegment={setHoveredSegmentKey} />
      <TimingSummary request={request} />

      <div className="mt-3">
        <PropertyList labelWidth={72} align="baseline">
          <StreamPhaseProperties request={request} />
          <Property label="调度" truncate={false}>
            {lifecycleLoading ? <LoadingLine /> : <ScheduleValue request={request} snapshot={snapshot} isAccountRequest={isAccountRequest} expandButton={expandButton} />}
          </Property>
          <Property label="隐私" truncate={false}>
            {lifecycleLoading ? <LoadingLine /> : <PrivacyValue privacyScan={lifecycle?.privacyScan || null} warnings={request.privacyWarnings} />}
          </Property>
          {retryPropertyVisible(request, accountSwitchCount, isAccountRequest) && <Property label="重试" truncate={false}>
            {Number.isFinite(request.upstreamFirstDispatchMs) && <><span className="font-mono tabular-nums">{request.retryCount ?? 0}</span> 次</>}
            {isAccountRequest && accountSwitchCount > 0 && <span className="ml-1.5 text-xs text-fg-muted">换号 {accountSwitchCount} 次</span>}
            {!isAccountRequest && request.requestedEndpoint && request.effectiveEndpoint
              && request.requestedEndpoint !== request.effectiveEndpoint && (
              <span className="ml-1.5 text-xs text-warn">故障转移</span>
            )}
          </Property>}
          {!lifecycleLoading && integrityWarnings.length > 0 && (
            <Property label="完整性提示" truncate={false}>
              <span className="text-xs text-warn">{integrityWarnings.join(' · ')}</span>
            </Property>
          )}
        </PropertyList>
        {!lifecycleLoading && canExpand && scheduleExpanded && <DecisionList rows={decisionRows} />}
      </div>

      {!lifecycleLoading && flow && (
        <div className="mt-3 border-t border-line-soft pt-3">
          <div className="mb-2 flex items-baseline justify-between gap-2">
            <h4 className="text-[11.5px] font-semibold tracking-[0.02em] text-fg-body">链路轨迹</h4>
            {hasExpandableFlow && <span className="text-[11px] text-fg-subtle">点击节点展开毫秒里程碑</span>}
          </div>
          <RequestFlowTrail key={request.requestId} flow={flow} settled={settled} requestStatus={request.status} />
        </div>
      )}
    </div>
  );
};

export default LifecyclePanel;
