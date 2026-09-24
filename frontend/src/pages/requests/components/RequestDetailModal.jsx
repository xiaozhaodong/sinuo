// ============================================
// RequestDetailModal - 请求详情模态框
// Command Palette 风格
// 2025-12-01
// ============================================

import { useState, useEffect, useRef } from 'react';
import {
  X,
  Copy,
  Check,
  Activity,
  DollarSign,
  Server,
  FileText,
  Waves,
  RefreshCw,
  Calendar,
  Database,
  AlertCircle,
  TrendingUp,
  TrendingDown,
  Zap,
  Brain
} from 'lucide-react';
import RequestStatusBadge from './RequestStatusBadge.jsx';
import LifecyclePanel from './LifecyclePanel.jsx';
import { formatCost } from '@utils/api.js';
import { useTimezone } from '@contexts/TimezoneContext.jsx';
import useModalLifecycle from '@hooks/useModalLifecycle.js';
import useRequestLifecycleDetail from '../hooks/useRequestLifecycleDetail.js';
import { copyTextToClipboard } from './clipboard.js';
import { getRequestFamilyMeta } from '../utils/requestSource.js';
import { isSubscriptionRequest } from '../utils/subscriptionUpstream.js';
import { getRequestPresentation } from '../utils/requestPresentation.js';
import { assessModel, assessReasoning, parseReasoning, MODEL_LEVELS, REASONING_LEVELS } from '../utils/modelConsistency.js';

/**
 * 信息行组件
 */
const InfoRow = ({ icon: Icon, label, value, copyable = false }) => {
  const [copied, setCopied] = useState(false);
  const resetTimerRef = useRef(null);

  useEffect(() => () => {
    if (resetTimerRef.current) {
      clearTimeout(resetTimerRef.current);
      resetTimerRef.current = null;
    }
  }, []);

  const handleCopy = async () => {
    if (copyable && typeof value === 'string' && value) {
      const didCopy = await copyTextToClipboard(value, label);
      if (!didCopy) {
        setCopied(false);
        return;
      }

      setCopied(true);
      if (resetTimerRef.current) {
        clearTimeout(resetTimerRef.current);
      }
      resetTimerRef.current = window.setTimeout(() => {
        setCopied(false);
        resetTimerRef.current = null;
      }, 2000);
    }
  };

  return (
    <div className="flex items-start justify-between py-3 border-b border-line-soft last:border-b-0 group">
      <div className="flex items-center gap-2 text-fg-muted min-w-[120px]">
        <Icon className="w-4 h-4 flex-shrink-0" />
        <span className="text-sm font-medium">{label}</span>
      </div>
      <div className="flex items-center gap-2 flex-1 justify-end">
        <span className="text-sm font-mono text-fg text-right">
          {value || '-'}
        </span>
        {copyable && (
          <button
            onClick={handleCopy}
            className="p-1 opacity-0 group-hover:opacity-100 hover:bg-surface-mut rounded transition-all"
            title="复制"
          >
            {copied ? (
              <Check className="w-3.5 h-3.5 text-success" />
            ) : (
              <Copy className="w-3.5 h-3.5 text-fg-subtle" />
            )}
          </button>
        )}
      </div>
    </div>
  );
};

/**
 * Token 指标卡片
 */
const TokenCard = ({ icon: Icon, label, value, tone }) => (
  <div className={`${tone} rounded-lg p-4 border`}>
    <div className="flex items-center justify-between mb-2">
      <Icon className="w-4 h-4" />
    </div>
    <div className="text-2xl font-bold font-mono">
      {(value || 0).toLocaleString()}
    </div>
    <div className="text-xs opacity-80 mt-1">{label}</div>
  </div>
);

// 模型与思考卡片：档位 pill（§1.3 颜色）
const CONSISTENCY_PILLS = {
  consistent: { dot: 'bg-success-solid', text: 'text-success', label: '一致', solid: true },
  alias: { dot: 'border-line', text: 'text-fg-subtle', label: '别名展开', solid: false },
  hint: { dot: 'border-line', text: 'text-fg-subtle', label: '提示', solid: false },
  unverifiable: { dot: 'border-line', text: 'text-fg-subtle', label: '等级无法校验', solid: false },
  mismatch: { dot: 'bg-warn-solid', text: 'text-warn', label: '不一致', solid: true, icon: AlertCircle },
  no_response: { text: 'text-fg-subtle', label: '无响应模型' },
  legacy: { text: 'text-fg-subtle', label: '旧数据' },
  none: null
};

const ConsistencyPill = ({ level, label }) => {
  const pill = CONSISTENCY_PILLS[level];
  if (!pill) {
    return null;
  }
  const Icon = pill.icon;
  return (
    <span className={`inline-flex items-center gap-1 text-xs ${pill.text}`}>
      {Icon ? (
        <Icon className="h-3.5 w-3.5" />
      ) : pill.dot ? (
        <span className={`inline-block h-2 w-2 rounded-full ${pill.solid ? pill.dot : `border ${pill.dot}`}`} />
      ) : null}
      {label || pill.label}
    </span>
  );
};

const ConsistencyRow = ({ label, value, pill, tag }) => (
  <div className="flex items-center justify-between gap-3 py-2">
    <span className="text-sm font-medium text-fg-muted">{label}</span>
    <span className="flex min-w-0 items-center gap-2 text-right">
      <span className="truncate text-sm font-mono text-fg">{value}</span>
      {tag && <span className="rounded border border-line bg-surface-sub px-1.5 py-0.5 text-[11px] text-fg-muted">{tag}</span>}
      {pill}
    </span>
  </div>
);

/**
 * 「模型与思考」卡片：一致性只用 responseModel 判定；model 是计费模型，不参与判定。
 */
const ModelConsistencyCard = ({ request }) => {
  const model = assessModel(request);
  const reasoning = assessReasoning(request);
  const requestModel = (request.requestModel || '').trim();
  const upstreamModel = (request.upstreamModel || '').trim();
  const responseModel = (request.responseModel || '').trim();
  const isLegacy = model.level === MODEL_LEVELS.LEGACY;
  // 上游模型行：改写后显示「已改写」；图像请求省略 model 时后端注入默认模型，显示「默认模型」
  const showUpstream = upstreamModel !== '' && upstreamModel.toLowerCase() !== requestModel.toLowerCase();
  const upstreamTag = requestModel === '' ? '默认模型' : '已改写';
  const responsePill = model.level === MODEL_LEVELS.NO_RESPONSE || isLegacy
    ? <span className="text-xs text-fg-subtle">{CONSISTENCY_PILLS[model.level].label}</span>
    : <ConsistencyPill level={model.level} />;
  const reasoningNoteLevel = reasoning.level === REASONING_LEVELS.CONSISTENT && reasoning.note ? 'unverifiable' : reasoning.level;
  const reasoningPill = reasoning.level === REASONING_LEVELS.NONE
    ? null
    : <ConsistencyPill level={reasoningNoteLevel} label={reasoning.level === REASONING_LEVELS.HINT || reasoning.level === REASONING_LEVELS.UNVERIFIABLE ? reasoning.note || undefined : undefined} />;

  return (
    <div className="bg-surface rounded-xl border border-line overflow-hidden">
      <div className="px-4 py-3 bg-surface-sub border-b border-line-soft">
        <h3 className="text-sm font-semibold text-fg">模型与思考</h3>
      </div>
      <div className="p-4 divide-y divide-line-soft">
        <ConsistencyRow label="请求模型" value={requestModel || '—'} pill={isLegacy ? <span className="text-xs text-fg-subtle">旧数据</span> : null} />
        {showUpstream && (
          <ConsistencyRow label="上游模型" value={upstreamModel} tag={upstreamTag} />
        )}
        <ConsistencyRow label="响应模型" value={responseModel || '—'} pill={responsePill} />
        <ConsistencyRow label="请求思考" value={reasoning.requestLabel} />
        <ConsistencyRow label="响应思考" value={reasoning.responseLabel} pill={reasoningPill} />
        {reasoning.level === REASONING_LEVELS.CONSISTENT && reasoning.note && (
          <div className="pt-2 text-xs text-fg-subtle">{reasoning.note}：Claude 响应不回显 effort，只能校验是否出现思考块。</div>
        )}
        {model.level === MODEL_LEVELS.MISMATCH && (
          <div className="flex items-start gap-2 pt-3 text-xs text-warn">
            <AlertCircle className="mt-0.5 h-3.5 w-3.5 flex-shrink-0" />
            <span>
              {model.rewritten
                ? '上游返回的模型与改写后实际发出的模型不一致，请检查该上游。'
                : '上游返回的模型与实际请求的模型不一致，本次未应用模型改写规则，请检查该端点。'}
            </span>
          </div>
        )}
        {reasoning.level === REASONING_LEVELS.MISMATCH && model.level !== MODEL_LEVELS.MISMATCH && (
          <div className="flex items-start gap-2 pt-3 text-xs text-warn">
            <AlertCircle className="mt-0.5 h-3.5 w-3.5 flex-shrink-0" />
            <span>上游返回的思考等级与请求不一致。</span>
          </div>
        )}
      </div>
    </div>
  );
};

// 请求种类展示：legacy 是历史与旧入口记录，不额外占一行。
const REQUEST_KIND_LABELS = {
  generation: '生成',
  compaction: '独立压缩'
};

const formatRouteMode = (mode) => {
  switch (mode) {
    case 'manual_fixed':
      return '严格固定';
    case 'manual_preferred':
      return '手动优选';
    default:
      return '自动';
  }
};

const RequestDetailModal = ({ isOpen, onClose, request: initialRequest, subscriptionEndpointNames }) => {
  const { formatTimestamp } = useTimezone();
  const [activeTab, setActiveTab] = useState('overview');
  const closeButtonRef = useRef(null);

  useModalLifecycle({ open: isOpen, onClose, initialFocusRef: closeButtonRef });

  // 详情接口为弹窗唯一权威数据源（F3）；列表回填仅作打开瞬间的先行渲染。
  const { lifecycle, lifecycleLoading } = useRequestLifecycleDetail(initialRequest?.requestId, isOpen);

  if (!isOpen || !initialRequest) return null;

  // 详情接口为弹窗唯一权威数据源；upstreamWriteMs 仅详情携带，需合并进有效行数据，
  // 否则 LifecyclePanel 的 buildLifecycleSegments 永远拿不到它，「连接/准备」段不展示。
  const request = lifecycle?.request
    ? { ...lifecycle.request, upstreamWriteMs: lifecycle.upstreamWriteMs ?? null }
    : initialRequest;

  // 未知用量与成本显示占位符：终态但没收到 usage 时，0 是「还不知道」而不是「用了 0 个」。
  const usageKnown = request.usageKnown !== false;
  const costKnown = request.costKnown !== false;
  const presentation = getRequestPresentation(request);

  // 计算 Token 总数
  const totalTokens = (request.inputTokens || 0) + (request.outputTokens || 0);
  const inputPercent = totalTokens > 0 ? Math.round((request.inputTokens / totalTokens) * 100) : 0;
  const outputPercent = totalTokens > 0 ? Math.round((request.outputTokens / totalTokens) * 100) : 0;

  // 请求类型
  const StreamIcon = request.isStreaming ? Waves : RefreshCw;
  const streamLabel = request.isStreaming ? '流式请求' : '常规请求';
  const streamColor = request.isStreaming ? 'tone-blue' : 'tone-slate';
  const family = getRequestFamilyMeta(request.requestFamily);
  // Codex 响应回显的推理 token（来自 response_reasoning 串，非 usage）
  const reasoningTokens = request.requestFamily === 'codex'
    ? parseReasoning(request.responseReasoning).reasoning_tokens
    : undefined;

  return (
    <div className="fixed inset-0 z-[10000] flex items-start justify-center pt-[15vh] px-4">
      {/* 背景遮罩 */}
      <div
        className="fixed inset-0 bg-overlay backdrop-blur-sm animate-in fade-in duration-200"
        onClick={onClose}
      />

      {/* 模态框内容 - 固定高度，内容区域滚动 */}
      <div
        role="dialog"
        aria-modal="true"
        aria-label="请求详情"
        className="relative w-full max-w-3xl max-h-[80vh] bg-surface rounded-2xl shadow-2xl ring-1 ring-hairline animate-in zoom-in-95 fade-in duration-200 flex flex-col overflow-hidden"
      >
        {/* 头部 - 固定不滚动 */}
        <div className="flex-shrink-0 px-6 py-4 border-b border-line-soft bg-surface rounded-t-2xl flex items-center justify-between">
          <div className="flex items-center gap-3">
            <div className="tone-indigo p-2 rounded-lg">
              <FileText className="w-5 h-5" />
            </div>
            <div>
              <h2 className="text-lg font-bold text-fg">请求详情</h2>
              <div className="flex items-center gap-2 mt-0.5">
                <span className="text-xs text-fg-subtle font-mono">{request.requestId}</span>
                <span className={`inline-flex items-center gap-1 px-2 py-0.5 rounded-md text-xs font-medium ${streamColor}`}>
                  <StreamIcon className="w-3 h-3" />
                  {streamLabel}
                </span>
              </div>
            </div>
          </div>
          <div className="flex items-center gap-2">
            <kbd className="hidden sm:inline-block px-2 py-1 bg-surface-mut border border-line rounded text-xs text-fg-muted">ESC</kbd>
            <button ref={closeButtonRef} aria-label="关闭详情" onClick={onClose} className="p-2 hover:bg-surface-mut rounded-lg transition-colors text-fg-subtle hover:text-fg-muted">
              <X className="w-5 h-5" />
            </button>
          </div>
        </div>

        {/* Tab 导航 - 固定不滚动 */}
        <div className="flex-shrink-0 px-6 pt-4 border-b border-line-soft bg-surface-sub">
          <div className="flex gap-1">
            {[
              { id: 'overview', label: '概览', icon: Activity },
              { id: 'tokens', label: 'Token 详情', icon: Database },
              { id: 'network', label: '网络信息', icon: Server }
            ].map((tab) => (
              <button
                key={tab.id}
                onClick={() => setActiveTab(tab.id)}
                className={`flex items-center gap-2 px-4 py-2 rounded-t-lg text-sm font-medium transition-all ${
                  activeTab === tab.id
                    ? 'bg-surface text-accent shadow-sm border-t border-x border-line'
                    : 'text-fg-muted hover:text-fg-body hover:bg-surface-mut'
                }`}
              >
                <tab.icon className="w-4 h-4" />
                {tab.label}
              </button>
            ))}
          </div>
        </div>

        {/* 内容区域 - 自动填充剩余空间并滚动 */}
        <div className="flex-1 p-6 overflow-y-auto custom-scrollbar min-h-0">
          {/* 概览 Tab */}
          {activeTab === 'overview' && (
            <div className="space-y-6">
              {/* 生命周期面板（详情接口为权威数据源） */}
              <LifecyclePanel
                request={request}
                lifecycle={lifecycle}
                lifecycleLoading={lifecycleLoading}
              />

              {/* 状态 & 成本卡片 */}
              <div className="grid grid-cols-2 gap-4">
                <div className="tone-indigo rounded-xl p-4 border">
                  <div className="flex items-center justify-between mb-3">
                    <span className="text-sm font-medium">请求状态</span>
                    <Activity className="w-4 h-4" />
                  </div>
                  <RequestStatusBadge status={presentation.status} />
                </div>

                <div className="tone-orange rounded-xl p-4 border">
                  <div className="flex items-center justify-between mb-3">
                    <span className="text-sm font-medium">总成本</span>
                    <DollarSign className="w-4 h-4" />
                  </div>
                  <div className="text-2xl font-bold font-mono">
                    {costKnown ? formatCost(request.cost) : '—'}
                  </div>
                </div>
              </div>

              {/* 基本信息 */}
              <div className="bg-surface rounded-xl border border-line overflow-hidden">
                <div className="px-4 py-3 bg-surface-sub border-b border-line-soft">
                  <h3 className="text-sm font-semibold text-fg">基本信息</h3>
                </div>
                <div className="p-4">
                  <InfoRow icon={FileText} label="请求 ID" value={request.requestId} copyable />
                  <InfoRow icon={Calendar} label="时间戳" value={formatTimestamp(request.timestamp)} />
                  <InfoRow icon={Activity} label="类型" value={family.label} />
                  {REQUEST_KIND_LABELS[request.requestKind] && (
                    <InfoRow icon={Activity} label="请求种类" value={REQUEST_KIND_LABELS[request.requestKind]} />
                  )}
                  <InfoRow
                    icon={Server}
                    label="上游"
                    value={`${request.upstreamName || '未知上游'}${isSubscriptionRequest(request, subscriptionEndpointNames) ? '（订阅直通）' : ''}`}
                  />
                  <InfoRow icon={Activity} label="路由模式" value={formatRouteMode(request.routeMode)} />
                  {request.requestedEndpoint && (
                    <InfoRow icon={Server} label="手动目标" value={request.requestedEndpoint} />
                  )}
                  {request.effectiveEndpoint && request.effectiveEndpoint !== request.upstreamName && (
                    <InfoRow icon={Server} label="实际端点" value={request.effectiveEndpoint} />
                  )}
                  {request.fallbackReason && (
                    <InfoRow icon={AlertCircle} label="回退原因" value={request.fallbackReason} />
                  )}
                </div>
              </div>

              {/* 模型与思考（替换原「模型信息」卡片；一致性只看 response_model，不看计费模型） */}
              <ModelConsistencyCard request={request} />

              {/* 取消和中断用中性的结束原因；明确失败仍保留错误提示。 */}
              {(['failed', 'error', 'cancelled', 'timeout'].includes(request.status) ||
                presentation.failureReason || presentation.cancelReason) && (
                <div className={`rounded-xl border overflow-hidden ${presentation.isNeutral ? 'bg-surface-sub border-line' : 'bg-danger-soft border-danger-line'}`}>
                  <div className={`px-4 py-3 border-b ${presentation.isNeutral ? 'border-line' : 'bg-danger-line/30 border-danger-line'}`}>
                    <h3 className={`text-sm font-semibold flex items-center gap-2 ${presentation.isNeutral ? 'text-fg-muted' : 'text-danger'}`}>
                      <AlertCircle className="w-4 h-4" />
                      {presentation.heading}
                    </h3>
                  </div>
                  <div className="p-4 space-y-2">
                    {presentation.isInterrupted ? (
                      <>
                        <p className="text-sm font-medium text-fg-body">客户端连接断开</p>
                        <p className="text-sm leading-relaxed text-fg-muted">{presentation.description}</p>
                        <details className="pt-2 text-xs text-fg-muted">
                          <summary className="cursor-pointer select-none">诊断信息</summary>
                          <dl className="mt-2 space-y-1 break-all">
                            <div><dt className="inline">请求 ID：</dt><dd className="inline font-mono">{request.requestId}</dd></div>
                            <div><dt className="inline">原因代码：</dt><dd className="inline font-mono">{presentation.failureReason}</dd></div>
                          </dl>
                        </details>
                      </>
                    ) : presentation.failureReason && (
                      <div className="text-sm">
                        <span className="font-medium text-fg-muted">{presentation.isNeutral ? '原因：' : '失败原因：'}</span>
                        <span className={presentation.isNeutral ? 'text-fg-body' : 'text-danger'}>{presentation.failureReason}</span>
                      </div>
                    )}
                    {presentation.cancelReason && (
                      <div className="text-sm">
                        <span className="font-medium text-fg-muted">取消原因：</span>
                        <span className={presentation.isNeutral ? 'text-fg-body' : 'text-danger'}>{presentation.cancelReason}</span>
                      </div>
                    )}
                  </div>
                </div>
              )}
            </div>
          )}

          {/* Token 详情 Tab */}
          {activeTab === 'tokens' && (
            <div className="space-y-6">
              {/* Token 卡片 - 基础 */}
              <div className="grid grid-cols-2 gap-4">
                <TokenCard
                  icon={TrendingUp}
                  label="输入 Token"
                  value={usageKnown ? request.inputTokens || 0 : '—'}
                  tone="tone-blue"
                />
                <TokenCard
                  icon={TrendingDown}
                  label="输出 Token"
                  value={usageKnown ? request.outputTokens || 0 : '—'}
                  tone="tone-emerald"
                />
              </div>

              {/* Token 卡片 - 缓存 */}
              <div className="grid grid-cols-2 gap-4">
                <TokenCard
                  icon={Database}
                  label="缓存创建 (5分钟)"
                  value={usageKnown ? request.cacheCreation5mTokens || 0 : '—'}
                  tone="tone-purple"
                />
                <TokenCard
                  icon={Database}
                  label="缓存创建 (1小时)"
                  value={usageKnown ? request.cacheCreation1hTokens || 0 : '—'}
                  tone="tone-violet"
                />
                <TokenCard
                  icon={Zap}
                  label="缓存读取"
                  value={usageKnown ? request.cacheReadTokens || 0 : '—'}
                  tone="tone-amber"
                />
                <TokenCard
                  icon={Database}
                  label="缓存创建 (总计)"
                  value={usageKnown ? request.cacheCreationTokens || 0 : '—'}
                  tone="tone-slate"
                />
              </div>

              {/* 推理 Token（Codex 且上游回显 reasoning_tokens 时） */}
              {reasoningTokens !== undefined && (
                <div className="grid grid-cols-2 gap-4">
                  <TokenCard
                    icon={Brain}
                    label="推理 Token（含在输出 Token 内）"
                    value={Number(reasoningTokens) || 0}
                    tone="tone-violet"
                  />
                </div>
              )}

              {/* Token 总计 */}
              <div className="tone-indigo rounded-xl p-6 border">
                <div className="flex items-center justify-between mb-4">
                  <div className="flex items-center gap-3">
                    <div className="p-2 bg-surface rounded-lg shadow-sm">
                      <FileText className="w-5 h-5 text-accent" />
                    </div>
                    <div>
                      <div className="text-sm font-medium">总 Token 消耗</div>
                      <div className="text-xs opacity-80">Input + Output</div>
                    </div>
                  </div>
                  <div className="text-3xl font-bold font-mono">
                    {usageKnown ? totalTokens.toLocaleString() : '—'}
                  </div>
                </div>

                {/* 分布条：未知用量不画 0% 的假分布 */}
                {usageKnown && (
                  <div className="space-y-2">
                    <div className="flex items-center justify-between text-xs">
                      <span>输入 {inputPercent}%</span>
                      <span>输出 {outputPercent}%</span>
                    </div>
                    <div className="h-2 bg-surface-emph rounded-full overflow-hidden flex">
                      <div className="bg-blue-500" style={{ width: `${inputPercent}%` }} />
                      <div className="bg-success-solid" style={{ width: `${outputPercent}%` }} />
                    </div>
                  </div>
                )}
              </div>
            </div>
          )}

          {/* 网络信息 Tab */}
          {activeTab === 'network' && (
            <div className="bg-surface rounded-xl border border-line overflow-hidden">
              <div className="px-4 py-3 bg-surface-sub border-b border-line-soft">
                <h3 className="text-sm font-semibold text-fg">网络详情</h3>
              </div>
              <div className="p-4">
                <InfoRow icon={Server} label="HTTP 状态码" value={request.http_status_code || request.httpStatusCode || '-'} />
                <InfoRow icon={RefreshCw} label="重试次数" value={`${request.retry_count || request.retryCount || 0} 次`} />
                <InfoRow icon={Activity} label="请求方法" value={request.method || '-'} />
                <InfoRow icon={FileText} label="请求路径" value={request.path || '-'} copyable />
              </div>
            </div>
          )}
        </div>

        {/* 底部操作栏 - 固定不滚动 */}
        <div className="flex-shrink-0 px-6 py-4 border-t border-line-soft bg-surface-sub backdrop-blur-sm rounded-b-2xl flex justify-between items-center">
          <div className="text-xs text-fg-muted">
            Request ID: <span className="font-mono text-fg-body">{request.requestId}</span>
          </div>
          <div className="flex items-center gap-2">
            <button
              onClick={() => copyTextToClipboard(request.requestId, '请求 ID')}
              className="px-3 py-1.5 text-xs font-medium text-fg-body bg-surface border border-line rounded-lg hover:bg-surface-sub transition-colors flex items-center gap-1.5"
            >
              <Copy className="w-3.5 h-3.5" /> 复制 ID
            </button>
            <button
              onClick={onClose}
              className="px-3 py-1.5 text-xs font-medium text-white bg-indigo-600 rounded-lg hover:bg-indigo-700 dark:hover:bg-indigo-500 transition-colors"
            >
              关闭
            </button>
          </div>
        </div>
      </div>
    </div>
  );
};

export default RequestDetailModal;
