// ============================================
// LifecyclePanel - 请求生命周期面板（方案 F2）
// 2026-08-13
// 顶部分段耗时条 + 底部四格指标行（调度快照 / 计费输入 / 隐私扫描 / 重试）。
// 组件不持有调度器；拉取由 useRequestLifecycleDetail 负责。
// ============================================

import { useState } from 'react';
import { ChevronDown, ChevronUp, Shield, Zap } from 'lucide-react';
import { buildLifecycleSegments, lifecycleSegmentColors } from '../utils/lifecycle.js';
import {
  calculateTokensPerSecond,
  formatOptionalTimingBadge,
  formatTimingBadge,
  formatTpsBadge,
  resolveCompletionMs,
  resolveFirstResponseMs
} from '../utils/timing.js';

const EMPTY_CANDIDATES = [];

const LifecycleCell = ({ icon: Icon, iconClass, title, loading, children }) => (
  <div className="p-4 min-w-0">
    <div className="flex items-center gap-2 text-sm font-medium text-fg-muted mb-3">
      <Icon className={`w-4 h-4 ${iconClass}`} />
      <span>{title}</span>
    </div>
    {loading ? (
      <div className="space-y-2 animate-pulse">
        <div className="h-3 bg-surface-mut rounded w-3/4" />
        <div className="h-3 bg-surface-mut rounded w-1/2" />
      </div>
    ) : (
      children
    )}
  </div>
);

const DashText = () => <span className="text-sm text-fg-subtle">—</span>;

const EmptyCell = ({ icon: Icon, iconClass, title }) => (
  <LifecycleCell icon={Icon} iconClass={iconClass} title={title} loading={false}>
    <DashText />
  </LifecycleCell>
);

const LifecyclePanel = ({ request = {}, lifecycle = null, lifecycleLoading = false }) => {
  const [scheduleExpanded, setScheduleExpanded] = useState(false);
  const segments = buildLifecycleSegments(request);
  const totalMs = Math.max(request.duration || 0, 1);

  // 指标行（吸收原「耗时指标」行，含 TPS）。
  const firstResponseMs = resolveFirstResponseMs(request.firstTokenMs, request.duration, request.isStreaming);
  const completionMs = Number.isFinite(firstResponseMs)
    ? resolveCompletionMs(request.completionMs, request.duration, firstResponseMs, request.isStreaming)
    : null;
  const tokensPerSecond = calculateTokensPerSecond(request.outputTokens, completionMs);

  // 调度快照格
  const snapshot = lifecycle?.scheduleSnapshot || null;
  const isAccountRequest = request.upstreamType === 'account' || request.requestFamily === 'codex';
  const candidates = Array.isArray(snapshot?.candidates) ? snapshot.candidates : EMPTY_CANDIDATES;
  const endpointDecisions = Array.isArray(snapshot?.decisions) ? snapshot.decisions : EMPTY_CANDIDATES;
  // upstream_group 出现即说明这条请求走了模型路由。组名是后端自动维护的实现细节
  // （历史数据里还可能是旧版手工建的组名），不给用户看，只标明路由方式；
  // 具体模型由紧随其后的 request_model 呈现。
  const routedByModelRouting = Boolean(
    snapshot?.upstream_group || snapshot?.upstreamGroup ||
    snapshot?.route_scope_group_name || snapshot?.routeScopeGroupName
  );
  const endpointSelected = snapshot?.selected_endpoint || snapshot?.selectedEndpoint || request.effectiveEndpoint || '';
  const selectedReason = snapshot ? snapshot.selected_tier_label || snapshot.selectedTierLabel || '' : '';
  const selectedName = snapshot ? snapshot.selected_account_name || snapshot.selectedAccountName || '' : '';

  const privacyScan = lifecycle?.privacyScan || null;
  const privacyActionLabel = {
    redact: '已脱敏',
    detect: '仅检测',
    blocked: '已拦截'
  }[privacyScan?.action];

  const runtimeOutcomeCount = candidates.filter((candidate) => {
    const outcome = candidate.runtime_outcome || candidate.runtimeOutcome || '';
    return outcome && outcome !== 'success';
  }).length;

  return (
    <div className="bg-surface rounded-xl border border-line overflow-hidden">
      {/* 顶部分段耗时条 */}
      <div className="px-4 py-4 border-b border-line-soft bg-surface-sub">
        <div className="flex items-center justify-between mb-3">
          <h3 className="text-sm font-semibold text-fg">生命周期</h3>
          <div className="inline-flex items-center gap-1.5 flex-wrap justify-end text-xs font-mono">
            {Number.isFinite(firstResponseMs) && (
              <span className="tone-violet inline-flex items-center px-2 py-1 rounded border">
                首字 {formatOptionalTimingBadge(firstResponseMs)}
              </span>
            )}
            {Number.isFinite(completionMs) && (
              <span className="tone-emerald inline-flex items-center px-2 py-1 rounded border">
                生成 {formatOptionalTimingBadge(completionMs)}
              </span>
            )}
            <span className="tone-slate inline-flex items-center px-2 py-1 rounded border">
              总耗 {formatTimingBadge(request.duration)}
            </span>
            {Number.isFinite(tokensPerSecond) && (
              <span className="tone-slate inline-flex items-center px-2 py-1 rounded border">
                TPS {formatTpsBadge(tokensPerSecond)}
              </span>
            )}
          </div>
        </div>

        <div className="flex items-stretch gap-1 h-7">
          {segments.map((segment) => {
            const pct = (segment.ms / totalMs) * 100;
            // 块宽按耗时比例伸展，但保证文字放得下（min-w-max）；
            // 占比过小的段只显示时间，名称见下方图例与 title。
            // 终态段 label 为短标签，原始错误全文经 detail 走 tooltip。
            const showLabel = pct >= 10;
            const tooltip = `${segment.label} ${formatTimingBadge(segment.ms)}${segment.detail ? `\n${segment.detail}` : ''}`;
            return (
              <div
                key={segment.key}
                className={`${lifecycleSegmentColors[segment.key] || 'bg-slate-200 dark:bg-slate-600'} rounded-md flex items-center justify-center px-2 min-w-max transition-all`}
                style={{ flexGrow: Math.max(segment.ms, 1), flexBasis: 0 }}
                title={tooltip}
              >
                <span className="text-[11px] font-mono font-medium text-slate-700/90 dark:text-slate-100 whitespace-nowrap max-w-[220px] overflow-hidden text-ellipsis">
                  {showLabel ? `${segment.label} ${formatTimingBadge(segment.ms)}` : formatTimingBadge(segment.ms)}
                </span>
              </div>
            );
          })}
        </div>
        <div className="flex items-center flex-wrap gap-x-4 gap-y-1 mt-2 text-[11px] text-fg-muted">
          {segments.map((segment) => (
            <span
              key={segment.key}
              className="inline-flex items-center gap-1.5 min-w-0"
              title={segment.detail || undefined}
            >
              <span className={`w-2.5 h-2.5 rounded-sm shrink-0 ${lifecycleSegmentColors[segment.key] || 'bg-slate-200 dark:bg-slate-600'}`} />
              <span className="truncate max-w-[220px]">{segment.label}</span>
            </span>
          ))}
        </div>
      </div>

      {/* 底部四格指标行 */}
      <div className="grid grid-cols-2 divide-x divide-line-soft">
        {/* 调度快照 */}
        {lifecycleLoading ? (
          <LifecycleCell icon={Zap} iconClass="text-accent" title="调度快照" loading>
            <span />
          </LifecycleCell>
        ) : isAccountRequest && snapshot ? (
          <LifecycleCell icon={Zap} iconClass="text-accent" title="调度快照" loading={false}>
            <div className="text-sm">
              <div className="font-medium text-fg truncate">
                {selectedName || '—'}
                {selectedReason ? <span className="text-fg-muted ml-1">· {selectedReason}</span> : null}
              </div>
              <button
                type="button"
                onClick={() => setScheduleExpanded((value) => !value)}
                className="mt-2 inline-flex items-center gap-1 text-xs text-accent hover:text-accent-fg"
              >
                {scheduleExpanded ? <ChevronUp className="w-3.5 h-3.5" /> : <ChevronDown className="w-3.5 h-3.5" />}
                候选决策（{candidates.length}）
              </button>
            </div>
            {scheduleExpanded && (
              <div className="mt-2 max-h-48 overflow-y-auto rounded-lg border border-line-soft divide-y divide-line-soft">
                {candidates.length === 0 && (
                  <div className="px-2 py-1.5 text-xs text-fg-subtle">无候选</div>
                )}
                {candidates.map((candidate, index) => {
                  const outcome = candidate.runtime_outcome || candidate.runtimeOutcome || '';
                  return (
                    <div key={`${candidate.account_id || candidate.accountId || index}-${index}`} className="px-2 py-1.5 flex items-center justify-between gap-2">
                      <span className="text-xs text-fg-body truncate">
                        {candidate.account_name || candidate.accountName || candidate.account_id || '—'}
                      </span>
                      <span className="text-[11px] text-fg-subtle shrink-0">
                        {outcome || candidate.decision || candidate.reason || ''}
                      </span>
                    </div>
                  );
                })}
              </div>
            )}
          </LifecycleCell>
        ) : !isAccountRequest && snapshot ? (
          <LifecycleCell icon={Zap} iconClass="text-accent" title="调度快照" loading={false}>
            <div className="text-sm">
              <div className="font-medium text-fg truncate">
                {routedByModelRouting ? '模型路由' : '全局端点'}
                {snapshot.request_model || snapshot.requestModel ? (
                  <span className="text-fg-muted ml-1">· {snapshot.request_model || snapshot.requestModel}</span>
                ) : null}
              </div>
              <div className="text-xs text-fg-muted mt-0.5">
                {endpointSelected || '—'}
                {snapshot.final_outcome || snapshot.finalOutcome ? ` · ${snapshot.final_outcome || snapshot.finalOutcome}` : null}
              </div>
              {endpointDecisions.length > 0 && (
                <button
                  type="button"
                  onClick={() => setScheduleExpanded((value) => !value)}
                  className="mt-2 inline-flex items-center gap-1 text-xs text-accent hover:text-accent-fg"
                >
                  {scheduleExpanded ? <ChevronUp className="w-3.5 h-3.5" /> : <ChevronDown className="w-3.5 h-3.5" />}
                  候选决策（{endpointDecisions.length}）
                </button>
              )}
            </div>
            {scheduleExpanded && endpointDecisions.length > 0 && (
              <div className="mt-2 max-h-48 overflow-y-auto rounded-lg border border-line-soft divide-y divide-line-soft">
                {endpointDecisions.map((decision, index) => (
                  <div key={`${decision.name || index}-${index}`} className="px-2 py-1.5 flex items-center justify-between gap-2">
                    <span className="text-xs text-fg-body truncate">{decision.name || '—'}</span>
                    <span className="text-[11px] text-fg-subtle shrink-0">
                      {decision.runtime_outcome || decision.runtimeOutcome || decision.decision || decision.reason || ''}
                    </span>
                  </div>
                ))}
              </div>
            )}
          </LifecycleCell>
        ) : isAccountRequest ? (
          <EmptyCell icon={Zap} iconClass="text-accent" title="调度快照" />
        ) : (
          <LifecycleCell icon={Zap} iconClass="text-accent" title="调度快照" loading={false}>
            <div className="text-sm text-fg-body">
              <div>路由 {request.routeMode === 'manual_fixed' ? '严格固定' : request.routeMode === 'manual_preferred' ? '手动优选' : '自动'}</div>
              <div className="text-xs text-fg-muted mt-0.5">
                {request.effectiveEndpoint || request.upstreamName || '—'}
                {request.fallbackReason ? <span className="text-warn"> · 回退</span> : null}
              </div>
            </div>
          </LifecycleCell>
        )}

        {/* 计费输入 */}
        <LifecycleCell icon={Zap} iconClass="text-warn-solid" title="计费输入" loading={false}>
          {request.usageKnown === false ? (
            <DashText />
          ) : isAccountRequest ? (
            <div className="font-mono text-lg text-fg">
              {Math.max((request.inputTokens || 0) - (request.cacheReadTokens || 0), 0).toLocaleString()}
              <span className="text-xs text-fg-subtle ml-1">input − cache_read</span>
            </div>
          ) : (
            <div className="font-mono text-lg text-fg">
              {(request.inputTokens || 0).toLocaleString()}
              <span className="text-xs text-fg-subtle ml-1">
                {request.cacheReadTokens ? `含缓存读 ${request.cacheReadTokens.toLocaleString()}` : '无缓存读'}
              </span>
            </div>
          )}
        </LifecycleCell>

        {/* 隐私扫描 */}
        {lifecycleLoading ? (
          <LifecycleCell icon={Shield} iconClass="text-success-solid" title="隐私扫描" loading>
            <span />
          </LifecycleCell>
        ) : privacyScan && privacyActionLabel ? (
          <LifecycleCell icon={Shield} iconClass="text-success-solid" title="隐私扫描" loading={false}>
            <div className="text-sm text-fg-body">
              <span className={privacyScan.action === 'blocked' ? 'text-danger' : 'text-success'}>
                {privacyActionLabel} · {privacyScan.hit_count ?? 0} 规则命中
              </span>
              {Number(privacyScan.scan_count) > 1 && (
                <span className="text-xs text-fg-subtle ml-1">共扫描 {privacyScan.scan_count} 次</span>
              )}
              {privacyScan.blocked_code && (
                <div className="text-xs text-danger mt-0.5">{privacyScan.blocked_code}</div>
              )}
            </div>
          </LifecycleCell>
        ) : (
          <EmptyCell icon={Shield} iconClass="text-success-solid" title="隐私扫描" />
        )}

        {/* 重试 / 故障转移 */}
        <LifecycleCell icon={Zap} iconClass="text-danger-solid" title="重试" loading={false}>
          <div className="text-sm text-fg-body">
            <span className="font-mono text-lg text-fg mr-1">{request.retryCount ?? 0}</span>
            次
            {isAccountRequest && runtimeOutcomeCount > 0 && (
              <span className="text-xs text-fg-muted ml-1">换号 {runtimeOutcomeCount} 次</span>
            )}
            {!isAccountRequest && request.requestedEndpoint && request.effectiveEndpoint
              && request.requestedEndpoint !== request.effectiveEndpoint && (
              <span className="text-xs text-warn ml-1">故障转移</span>
            )}
          </div>
        </LifecycleCell>
      </div>
    </div>
  );
};

export default LifecyclePanel;
