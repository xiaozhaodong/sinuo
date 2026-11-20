// ============================================
// RequestDetailPane - 请求详情面板（请求追踪页底部）
// 2026-10-07 桌面化：替代双击打开的详情弹窗。选中行即显示，进行中的请求随 request:update 事件刷新。
// 2026-11-20 三栏配平：生命周期与链路 ｜ 概要与模型 ｜ Token 与计费；三栏独立滚动、顶栏常驻摘要锚点。
// ============================================
// 详情接口为权威数据源（F3）；列表行只用于选中瞬间的先行渲染。
// ⚠️ 这里从 request 上读的每个字段都必须在 mergeRequestRows 的 COMPARED_FIELDS 里（静态扫描用例锁定），
//    否则该字段变化会被行引用复用吃掉，面板首帧显示过期值。

import { useEffect, useRef } from 'react';
import RequestBilling from './RequestBilling.jsx';
import { AlertCircle, Copy, MousePointerClick, RefreshCw, Waves, X } from 'lucide-react';
import { InspectorEmpty, Property, PropertyList } from '@components/desktop/Inspector.jsx';
import { formatCost } from '@utils/api.js';
import { useTimezone } from '@contexts/TimezoneContext.jsx';
import RequestStatusBadge from './RequestStatusBadge.jsx';
import LifecyclePanel from './LifecyclePanel.jsx';
import useRequestLifecycleDetail from '../hooks/useRequestLifecycleDetail.js';
import { copyTextToClipboard } from './clipboard.js';
import { isCodexSearchRequest, resolveRequestTypeBadge } from '../utils/requestSource.js';
import { formatRouteMode, getRequestPresentation } from '../utils/requestPresentation.js';
import { fullInputTokens, requestInputIncludesCache } from '../utils/requestInputUsage.js';
import { assessModel, assessReasoning, formatSpeed, parseReasoning, MODEL_LEVELS, REASONING_LEVELS } from '../utils/modelConsistency.js';
import { UNSETTLED_STATUSES } from '../utils/constants.js';
import { formatTimingBadge } from '../utils/timing.js';
import { formatSessionRatio } from '../utils/sessionUsage.js';

// 请求种类展示：legacy 是历史与旧入口记录，不额外占一行
const REQUEST_KIND_LABELS = {
  generation: '生成',
  compaction: '独立压缩'
};

const ENDED_STATUSES = ['failed', 'error', 'cancelled', 'timeout'];
const ICON_BUTTON = 'inline-flex h-6 w-6 shrink-0 items-center justify-center rounded text-fg-muted hover:bg-surface-mut hover:text-fg';

// 模型与思考：档位 pill（§1.3 颜色）
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
  if (!pill) return null;
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

const ModelValue = ({ value, tag = null, pill = null }) => (
  <span className="flex min-w-0 items-center gap-2">
    <span className="truncate font-mono text-xs text-fg">{value}</span>
    {tag && <span className="shrink-0 rounded border border-line bg-surface-sub px-1 text-[10.5px] text-fg-muted">{tag}</span>}
    {pill && <span className="shrink-0">{pill}</span>}
  </span>
);

const WarnNote = ({ children }) => (
  <div className="mt-2 flex items-start gap-1.5 text-xs text-warn">
    <AlertCircle className="mt-0.5 h-3.5 w-3.5 shrink-0" />
    <span>{children}</span>
  </div>
);

/**
 * 「模型与思考」：一致性只用 responseModel 判定；model 是计费模型，不参与判定。
 */
const ModelConsistencySection = ({ request }) => {
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
    <>
      <PropertyList labelWidth={56}>
        <Property label="请求模型" truncate={false}>
          <ModelValue value={requestModel || '—'} pill={isLegacy ? <span className="text-xs text-fg-subtle">旧数据</span> : null} />
        </Property>
        {showUpstream && (
          <Property label="上游模型" truncate={false}><ModelValue value={upstreamModel} tag={upstreamTag} /></Property>
        )}
        <Property label="响应模型" truncate={false}><ModelValue value={responseModel || '—'} pill={responsePill} /></Property>
        <Property label="请求思考" truncate={false}><ModelValue value={reasoning.requestLabel} /></Property>
        <Property label="响应思考" truncate={false}><ModelValue value={reasoning.responseLabel} pill={reasoningPill} /></Property>
        <Property label="速度档位" truncate={false}><ModelValue value={formatSpeed(request)} /></Property>
      </PropertyList>
      {reasoning.level === REASONING_LEVELS.CONSISTENT && reasoning.note && (
        <div className="mt-2 text-xs text-fg-subtle">{reasoning.note}：Claude 响应不回显 effort，只能校验是否出现思考块。</div>
      )}
      {model.level === MODEL_LEVELS.MISMATCH && (
        <WarnNote>
          {model.rewritten
            ? '上游返回的模型与改写后实际发出的模型不一致，请检查该上游。'
            : '上游返回的模型与实际请求的模型不一致，本次未应用模型改写规则，请检查该端点。'}
        </WarnNote>
      )}
      {reasoning.level === REASONING_LEVELS.MISMATCH && model.level !== MODEL_LEVELS.MISMATCH && (
        <WarnNote>上游返回的思考等级与请求不一致。</WarnNote>
      )}
    </>
  );
};

const TokenFigure = ({ label, value, title, tone, className = '' }) => (
  <div className={`flex min-w-0 items-baseline justify-between gap-2 ${className}`} title={title}>
    <span className="truncate text-fg-muted">{label}</span>
    <span className={`shrink-0 font-mono text-xs tabular-nums ${tone}`}>{value}</span>
  </div>
);

const TokenPairRow = ({ left, right }) => (
  <div className="grid grid-cols-2 items-baseline gap-3">
    <TokenFigure {...left} />
    <TokenFigure {...right} className="border-l border-line-soft pl-3" />
  </div>
);

/**
 * 「Token 与成本」。未知用量与成本显示占位符（与列表同口径）：在途时后端还没写这几个字段，
 * 终态但没收到 usage 时同理 —— 0 是「还不知道」而不是「用了 0 个」。
 */
const TokenSection = ({ request, decision, lifecycleLoading = false }) => {
  const pending = UNSETTLED_STATUSES.has(request.status);
  const usageKnown = !pending && request.usageKnown !== false;
  const costKnown = !pending && request.costKnown !== false;
  const usageTone = usageKnown ? 'text-fg' : 'text-fg-subtle';
  const formatUsage = (value) => (usageKnown ? (value || 0).toLocaleString() : '—');

  const inputTokens = request.inputTokens || 0;
  const outputTokens = request.outputTokens || 0;
  const cacheReadTokens = request.cacheReadTokens || 0;
  const cacheCreationTokens = request.cacheCreationTokens;
  const inputIncludesCache = requestInputIncludesCache(request.requestFamily, request.path);
  const fullInput = fullInputTokens(inputIncludesCache, inputTokens, cacheCreationTokens, cacheReadTokens);
  // 直接复用规范化后的 request.cacheRatio，与列表缓存率列保持唯一口径：
  // 缺失分项字段时为 null（留空不伪造比例），确定零命中时为 0（展示 0.0%）。
  const cacheHitRatio = usageKnown && typeof request.cacheRatio === 'number' && Number.isFinite(request.cacheRatio)
    ? request.cacheRatio
    : null;
  const cacheHitTone = cacheHitRatio > 0
    ? 'border-success-line bg-success-soft text-success'
    : 'border-line bg-surface-sub text-fg-muted';
  const waitingWriteSplit = inputIncludesCache && usageKnown && cacheCreationTokens > 0 && lifecycleLoading && !decision;
  const separateWrites = decision?.separate_cache_writes;
  const billableInput = inputIncludesCache
    ? Math.max(inputTokens - cacheReadTokens - (separateWrites ? (cacheCreationTokens ?? 0) : 0), 0)
    : inputTokens;
  const billableHint = waitingWriteSplit
    ? '正在确认缓存写入计费口径'
    : inputIncludesCache
      ? (separateWrites ? '输入扣除缓存读取和缓存写入' : '输入扣除缓存读取')
      : '普通输入；缓存读写单独计费';
  const totalHint = inputIncludesCache
    ? '输入 + 输出；缓存读写已包含在输入中'
    : '输入 + 输出 + 缓存创建 + 缓存读取';
  // Codex 响应回显的推理 token（来自 response_reasoning 串，非 usage）
  const reasoningTokens = request.requestFamily === 'codex'
    ? parseReasoning(request.responseReasoning).reasoning_tokens
    : undefined;
  const totalDisplay = fullInput === null ? '—' : formatUsage(fullInput + outputTokens);
  const costDisplay = costKnown ? formatCost(request.cost) : '—';
  const showCodexCacheSplit = inputIncludesCache && (separateWrites || request.cacheCreation30mTokens != null || request.cacheCreation12hTokens != null);

  return (
    <div>
      <div className="mb-2 flex items-center justify-between gap-2">
        <h3 className="text-[11.5px] font-semibold tracking-[0.02em] text-fg-body">Token 与成本</h3>
        {cacheHitRatio !== null && (
          <span
            className={`shrink-0 rounded-full border px-2 py-px font-mono text-[11px] tabular-nums ${cacheHitTone}`}
            title="按本条记录计算：缓存读取 ÷ 完整输入"
          >
            缓存命中 {formatSessionRatio(cacheHitRatio)}
          </span>
        )}
      </div>

      {/* 顶置汇总头：默认 280px 高度下首屏直见本次成本与合计 Token，避免被下方明细淹没 */}
      <div className="grid grid-cols-2 gap-3 rounded-lg border border-line bg-surface px-3 py-2">
        <div className="min-w-0">
          <div className="text-[11.5px] text-fg-muted">本次成本</div>
          <div className={`mt-0.5 truncate font-mono text-[15px] font-semibold leading-tight tabular-nums ${costKnown ? 'text-warn' : 'text-fg-subtle'}`}>
            {costDisplay}
          </div>
        </div>
        <div className="min-w-0 border-l border-line-soft pl-3" title={totalHint}>
          <div className="text-[11.5px] text-fg-muted">合计 Token</div>
          <div className={`mt-0.5 truncate font-mono text-[15px] font-semibold leading-tight tabular-nums ${usageTone}`}>
            {totalDisplay}
          </div>
        </div>
      </div>

      {/* 成对明细：右列带细竖线防止左列数字与右列标签串行 */}
      <div className="mt-2.5 space-y-1.5 text-[12.5px]">
        <TokenPairRow
          left={{ label: '输入', value: formatUsage(request.inputTokens), tone: usageTone }}
          right={{ label: '输出', value: formatUsage(request.outputTokens), tone: usageTone }}
        />
        <TokenPairRow
          left={{ label: '缓存读取', value: formatUsage(request.cacheReadTokens), tone: usageTone }}
          right={{
            label: '缓存创建',
            value: cacheCreationTokens == null ? '—' : formatUsage(cacheCreationTokens),
            title: inputIncludesCache ? '缓存写入 Token，已包含在输入总量中' : '5 分钟与 1 小时缓存创建合计',
            tone: usageTone
          }}
        />
        {showCodexCacheSplit && (
          <TokenPairRow
            left={{ label: '创建 30m', value: request.cacheCreation30mTokens == null ? '—' : formatUsage(request.cacheCreation30mTokens), tone: usageTone }}
            right={{ label: '创建 12h', value: request.cacheCreation12hTokens == null ? '—' : formatUsage(request.cacheCreation12hTokens), tone: usageTone }}
          />
        )}
        {!inputIncludesCache && (
          <TokenPairRow
            left={{ label: '创建 5m', value: formatUsage(request.cacheCreation5mTokens), tone: usageTone }}
            right={{ label: '创建 1h', value: formatUsage(request.cacheCreation1hTokens), tone: usageTone }}
          />
        )}
        {reasoningTokens !== undefined ? (
          <TokenPairRow
            left={{ label: '计费输入', value: waitingWriteSplit ? '—' : formatUsage(billableInput), title: billableHint, tone: waitingWriteSplit ? 'text-fg-subtle' : usageTone }}
            right={{ label: '推理', value: (Number(reasoningTokens) || 0).toLocaleString(), title: '推理 Token，含在输出 Token 内', tone: 'text-fg' }}
          />
        ) : (
          <TokenFigure
            label="计费输入"
            value={waitingWriteSplit ? '—' : formatUsage(billableInput)}
            title={billableHint}
            tone={waitingWriteSplit ? 'text-fg-subtle' : usageTone}
            className="border-t border-line-soft pt-1.5"
          />
        )}
      </div>
    </div>
  );
};

// 取消和中断用中性的结束原因；明确失败仍保留错误提示
const OutcomeNote = ({ request, presentation }) => {
  const visible = ENDED_STATUSES.includes(request.status) || presentation.failureReason || presentation.cancelReason;
  if (!visible) return null;
  const neutral = presentation.isNeutral;
  const reasonTone = neutral ? 'text-fg-body' : 'text-danger';

  return (
    <div className={`mt-3 space-y-1 rounded-md border px-3 py-2 text-xs ${neutral ? 'border-line bg-surface-sub' : 'border-danger-line bg-danger-soft'}`}>
      <div className={`flex items-center gap-1.5 font-semibold ${neutral ? 'text-fg-muted' : 'text-danger'}`}>
        <AlertCircle className="h-3.5 w-3.5" />
        {presentation.heading}
      </div>
      {presentation.isInterrupted ? (
        <>
          <p className="font-medium text-fg-body">客户端连接断开</p>
          <p className="leading-relaxed text-fg-muted">{presentation.description}</p>
          <details className="pt-1 text-fg-muted">
            <summary className="select-none">诊断信息</summary>
            <dl className="mt-1 space-y-0.5 break-all">
              <div><dt className="inline">请求 ID：</dt><dd className="inline font-mono">{request.requestId}</dd></div>
              <div><dt className="inline">原因代码：</dt><dd className="inline font-mono">{presentation.failureReason}</dd></div>
            </dl>
          </details>
        </>
      ) : presentation.failureReason && (
        <div>
          <span className="font-medium text-fg-muted">{neutral ? '原因：' : '失败原因：'}</span>
          <span className={`selectable break-all ${reasonTone}`}>{presentation.failureReason}</span>
        </div>
      )}
      {presentation.cancelReason && (
        <div>
          <span className="font-medium text-fg-muted">取消原因：</span>
          <span className={`selectable break-all ${reasonTone}`}>{presentation.cancelReason}</span>
        </div>
      )}
    </div>
  );
};

const SessionValue = ({ sessionId, onFilter }) => {
  if (!sessionId) return <span className="text-fg-subtle">—</span>;
  return (
    <span className="inline-flex min-w-0 items-center gap-1">
      <span className="font-mono text-xs" title={sessionId}>{sessionId.slice(0, 8)}</span>
      <button type="button" title="复制会话 ID" aria-label="复制会话 ID" onClick={() => copyTextToClipboard(sessionId, '会话 ID')} className={ICON_BUTTON}>
        <Copy size={13} />
      </button>
      {onFilter && <button type="button" onClick={() => onFilter(sessionId)} className="ml-0.5 shrink-0 text-xs font-medium text-accent hover:text-accent-fg">只看此会话</button>}
    </span>
  );
};

const resolveHttpStatusTone = (status) => {
  if (status >= 500) return 'text-danger';
  if (status >= 400) return 'text-warn';
  if (status >= 200 && status < 300) return 'text-success';
  return 'text-fg';
};

const SummarySection = ({ request, presentation, onFilterSession }) => {
  const family = resolveRequestTypeBadge(request);
  // 联网搜索没有单独的种类字段，按路径识别后与「生成 / 独立压缩」放在同一位置
  const kindLabel = isCodexSearchRequest(request) ? 'Codex 联网搜索' : REQUEST_KIND_LABELS[request.requestKind];
  const httpStatus = request.http_status_code || request.httpStatusCode;
  const requestLine = `${request.method || '-'} ${request.path || '-'}`;

  return (
    <>
      <PropertyList labelWidth={56}>
        <Property label="类型" truncate={false}>
          <span className="inline-flex items-center gap-1.5">
            {/* 文字包一层 span：grad-tone 的渐变字只作用于内层 span */}
            <span className={`inline-flex items-center rounded border px-1.5 text-[11px] font-semibold leading-4 ${family.className}`}><span>{family.label}</span></span>
            {kindLabel && <span className="text-fg-muted">{kindLabel}</span>}
          </span>
        </Property>
        <Property label="上游" title={request.upstreamName}><span className="font-medium">{request.upstreamName || '未知上游'}</span></Property>
        <Property label="会话" truncate={false}><SessionValue sessionId={request.sessionId} onFilter={onFilterSession} /></Property>
        <Property label="路由">{formatRouteMode(request.routeMode)}</Property>
        {request.requestedEndpoint && (
          <Property label="手动目标" title={request.requestedEndpoint}>{request.requestedEndpoint}</Property>
        )}
        {request.effectiveEndpoint && request.effectiveEndpoint !== request.upstreamName && (
          <Property label="实际端点" title={request.effectiveEndpoint}>{request.effectiveEndpoint}</Property>
        )}
        {request.fallbackReason && (
          <Property label="回退原因" title={request.fallbackReason}>{request.fallbackReason}</Property>
        )}
        <Property label="请求" title={requestLine}><span className="font-mono text-xs">{requestLine}</span></Property>
        <Property label="HTTP">
          {httpStatus
            ? <span className={`font-mono text-xs font-medium tabular-nums ${resolveHttpStatusTone(httpStatus)}`}>{httpStatus}</span>
            : <span className="text-fg-subtle">-</span>}
        </Property>
      </PropertyList>
      <OutcomeNote request={request} presentation={presentation} />
    </>
  );
};

const PaneHeader = ({ request, presentation, timestampText, onClose }) => {
  const StreamIcon = request.isStreaming ? Waves : RefreshCw;
  const settled = !UNSETTLED_STATUSES.has(request.status);
  const costKnown = settled && request.costKnown !== false;
  return (
    <div className="@container flex h-[36px] shrink-0 items-center gap-2.5 border-b border-line bg-surface-sub/80 pl-4 pr-2.5">
      <span className="inline-flex min-w-0 items-center gap-1">
        <span className="truncate font-mono text-[12.5px] font-semibold text-fg">{request.requestId}</span>
        <button type="button" title="复制请求 ID" aria-label="复制请求 ID" onClick={() => copyTextToClipboard(request.requestId, '请求 ID')} className={ICON_BUTTON}>
          <Copy size={13} />
        </button>
      </span>
      <RequestStatusBadge status={presentation.status} />
      <span className="inline-flex shrink-0 items-center gap-1 text-xs text-fg-muted">
        <StreamIcon size={12} className={request.isStreaming ? 'text-info' : undefined} />
        {request.isStreaming ? '流式' : '常规'}
      </span>
      <span className="flex-1" />
      <span
        data-header-anchor
        className="hidden shrink-0 items-center gap-2 rounded-md border border-line bg-surface px-2 py-0.5 text-xs @2xl:inline-flex"
      >
        <span className={settled ? 'font-mono tabular-nums text-fg-body' : 'text-fg-subtle'}>
          {settled ? formatTimingBadge(request.duration) : '进行中'}
        </span>
        <span className="h-3 w-px bg-line" aria-hidden="true" />
        <span className={`font-mono font-semibold tabular-nums ${costKnown ? 'text-warn' : 'text-fg-subtle'}`}>
          {costKnown ? formatCost(request.cost) : '—'}
        </span>
      </span>
      <span className="shrink-0 text-xs tabular-nums text-fg-muted">{timestampText}</span>
      <button type="button" title="隐藏详情面板" aria-label="隐藏详情面板" onClick={onClose} className={ICON_BUTTON}>
        <X size={14} />
      </button>
    </div>
  );
};

const SectionHeading = ({ children, className = '' }) => (
  <h3 className={`mb-2 text-[11.5px] font-semibold tracking-[0.02em] text-fg-body ${className}`}>{children}</h3>
);

const RequestDetailPane = ({ request: initialRequest, onClose, onFilterSession }) => {
  const { formatTimestamp } = useTimezone();
  const scrollRootRef = useRef(null);
  const selectedRequestId = initialRequest?.requestId ?? null;
  const { lifecycle, lifecycleLoading } = useRequestLifecycleDetail(selectedRequestId, Boolean(initialRequest));

  // 仅在切换请求 ID 时复位外层容器与三栏的纵向滚动位置；同一请求实时刷新时保持当前阅读位置。
  useEffect(() => {
    const root = scrollRootRef.current;
    if (!root) return;
    root.scrollTop = 0;
    for (const column of root.querySelectorAll('[data-detail-scroll]')) {
      column.scrollTop = 0;
    }
  }, [selectedRequestId]);

  if (!initialRequest) {
    return <InspectorEmpty icon={MousePointerClick}>选择一条请求查看详情</InspectorEmpty>;
  }

  // 详情接口为权威数据源，补足完整计时、思考信号和上游请求ID。
  const request = lifecycle?.request
    ? {
      ...lifecycle.request,
      upstreamWriteMs: lifecycle.upstreamWriteMs ?? null,
      upstreamFirstDispatchMs: lifecycle.upstreamFirstDispatchMs ?? null,
      upstreamDispatchMs: lifecycle.upstreamDispatchMs ?? null,
      upstreamConnMs: lifecycle.upstreamConnMs ?? null,
      firstReasoningMs: lifecycle.firstReasoningMs ?? null,
      reasoningHidden: lifecycle.reasoningHidden ?? null,
      upstreamRequestId: lifecycle.upstreamRequestId ?? ''
    }
    : initialRequest;
  const presentation = getRequestPresentation(request);

  return (
    <>
      <PaneHeader request={request} presentation={presentation} timestampText={formatTimestamp(request.timestamp)} onClose={onClose} />
      {/* 列间 1px 清晰分隔：桌面三栏独立纵向滚动，窄屏退回整区滚动 */}
      <div ref={scrollRootRef} className="@container min-h-0 flex-1 overflow-y-auto @4xl:overflow-hidden">
        <div className="grid min-h-full grid-cols-1 gap-px bg-line @xl:grid-cols-2 @4xl:h-full @4xl:grid-cols-[1.35fr_1fr_1.05fr]">
          <section data-detail-scroll className="custom-scrollbar min-w-0 bg-inspector px-4 py-3 @4xl:min-h-0 @4xl:overflow-y-auto">
            {/* 按请求重挂载：候选决策的展开状态不跨请求保留 */}
            <LifecyclePanel key={request.requestId} request={request} lifecycle={lifecycle} lifecycleLoading={lifecycleLoading} />
          </section>
          <section data-detail-scroll className="custom-scrollbar min-w-0 bg-inspector px-4 py-3 @4xl:min-h-0 @4xl:overflow-y-auto">
            <SectionHeading>概要</SectionHeading>
            <SummarySection request={request} presentation={presentation} onFilterSession={onFilterSession} />
            <div className="mt-3.5 border-t border-line pt-3">
              <SectionHeading>模型与思考</SectionHeading>
              <ModelConsistencySection request={request} />
            </div>
          </section>
          <section data-detail-scroll className="custom-scrollbar min-w-0 bg-inspector px-4 py-3 @xl:col-span-2 @4xl:col-span-1 @4xl:min-h-0 @4xl:overflow-y-auto">
            <TokenSection request={request} decision={lifecycle?.billingDecision} lifecycleLoading={lifecycleLoading} />
            <div className="mt-3.5 border-t border-line pt-3 empty:hidden">
              {lifecycleLoading ? <p className="text-xs text-fg-muted">正在读取计费依据…</p> :
                <RequestBilling key={`${request.requestId}:${lifecycle?.requestRevision ?? 0}:${lifecycle?.billingRateId ?? 0}`}
                  id={request.requestId} usageKnown={request.usageKnown !== false} version={lifecycle?.billingRateId} decision={lifecycle?.billingDecision}
                  officialRecalculation={lifecycle?.officialRecalculation} billingMultipliers={lifecycle?.billingMultipliers}
                  pending={UNSETTLED_STATUSES.has(request.status)} />}
            </div>
          </section>
        </div>
      </div>
    </>
  );
};

export default RequestDetailPane;
