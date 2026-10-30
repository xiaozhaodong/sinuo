// ============================================
// RequestDetailPane - 请求详情面板（请求追踪页底部）
// 2026-10-07 桌面化：替代双击打开的详情弹窗。选中行即显示，进行中的请求随 request:update 事件刷新。
// ============================================
// 三栏（窄时两栏 / 一栏）：生命周期 ｜ 概要 ｜ 模型与思考 + Token 与成本。
// 详情接口为权威数据源（F3）；列表行只用于选中瞬间的先行渲染。
// ⚠️ 这里从 request 上读的每个字段都必须在 mergeRequestRows 的 COMPARED_FIELDS 里（静态扫描用例锁定），
//    否则该字段变化会被行引用复用吃掉，面板首帧显示过期值。

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
import { assessModel, assessReasoning, formatSpeed, parseReasoning, MODEL_LEVELS, REASONING_LEVELS } from '../utils/modelConsistency.js';
import { UNSETTLED_STATUSES } from '../utils/constants.js';

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

const TokenFigure = ({ label, value, title, tone }) => (
  <div className="flex min-w-0 items-baseline justify-between gap-2" title={title}>
    <span className="truncate text-fg-muted">{label}</span>
    <span className={`shrink-0 font-mono text-xs tabular-nums ${tone}`}>{value}</span>
  </div>
);

/**
 * 「Token 与成本」。未知用量与成本显示占位符（与列表同口径）：在途时后端还没写这几个字段，
 * 终态但没收到 usage 时同理 —— 0 是「还不知道」而不是「用了 0 个」。
 * Codex 缓存写入只细分统计，仍包含在 input − cache_read 的计费输入中。
 */
const TokenSection = ({ request }) => {
  const pending = UNSETTLED_STATUSES.has(request.status);
  const usageKnown = !pending && request.usageKnown !== false;
  const costKnown = !pending && request.costKnown !== false;
  const usageTone = usageKnown ? 'text-fg' : 'text-fg-subtle';
  const formatUsage = (value) => (usageKnown ? (value || 0).toLocaleString() : '—');

  const inputTokens = request.inputTokens || 0;
  const outputTokens = request.outputTokens || 0;
  const cacheReadTokens = request.cacheReadTokens || 0;
  const cacheCreationTokens = request.cacheCreationTokens || 0;
  const family = String(request.requestFamily || '').trim().toLowerCase();
  const path = String(request.path || '').trim().toLowerCase();
  const inputIncludesCache = family === 'codex'
    || (family !== 'claude' && /\/responses(?:\/compact)?$/.test(path));
  const totalTokens = inputTokens + outputTokens + (inputIncludesCache ? 0 : cacheReadTokens + cacheCreationTokens);
  const billableInput = inputIncludesCache ? Math.max(inputTokens - cacheReadTokens, 0) : inputTokens;
  const billableHint = inputIncludesCache
    ? '输入 − 缓存读取；缓存写入已包含在内，继续按输入单价计算'
    : '普通输入；缓存读写单独计费';
  const totalHint = inputIncludesCache
    ? '输入 + 输出；缓存读写已包含在输入中'
    : '输入 + 输出 + 缓存创建 + 缓存读取';
  // Codex 响应回显的推理 token（来自 response_reasoning 串，非 usage）
  const reasoningTokens = request.requestFamily === 'codex'
    ? parseReasoning(request.responseReasoning).reasoning_tokens
    : undefined;

  return (
    <div className="grid grid-cols-2 gap-x-4 gap-y-1.5 text-[12.5px]">
      <TokenFigure label="输入" value={formatUsage(request.inputTokens)} tone={usageTone} />
      <TokenFigure label="输出" value={formatUsage(request.outputTokens)} tone={usageTone} />
      <TokenFigure label="缓存读取" value={formatUsage(request.cacheReadTokens)} tone={usageTone} />
      <TokenFigure label="缓存创建" value={formatUsage(cacheCreationTokens)} title={inputIncludesCache ? '缓存写入 Token，已计入输入总量和输入费用' : '5 分钟与 1 小时缓存创建合计'} tone={usageTone} />
      {!inputIncludesCache && (
        <>
          <TokenFigure label="创建 5m" value={formatUsage(request.cacheCreation5mTokens)} tone={usageTone} />
          <TokenFigure label="创建 1h" value={formatUsage(request.cacheCreation1hTokens)} tone={usageTone} />
        </>
      )}
      {reasoningTokens !== undefined && (
        <TokenFigure label="推理" value={(Number(reasoningTokens) || 0).toLocaleString()} title="推理 Token，含在输出 Token 内" tone="text-fg" />
      )}
      <TokenFigure label="合计" value={formatUsage(totalTokens)} title={totalHint} tone={usageTone} />
      <TokenFigure label="计费输入" value={formatUsage(billableInput)} title={billableHint} tone={usageTone} />
      <TokenFigure label="成本" value={costKnown ? formatCost(request.cost) : '—'} tone={costKnown ? 'font-medium text-warn' : 'text-fg-subtle'} />
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
  if (!sessionId) return <span>—</span>;
  return (
    <span className="inline-flex min-w-0 items-center gap-1.5">
      <span className="font-mono text-xs" title={sessionId}>{sessionId.slice(0, 8)}</span>
      <button type="button" title="复制会话 ID" aria-label="复制会话 ID" onClick={() => copyTextToClipboard(sessionId, '会话 ID')} className={ICON_BUTTON}>
        <Copy size={13} />
      </button>
      {onFilter && <button type="button" onClick={() => onFilter(sessionId)} className="shrink-0 text-xs text-accent hover:text-accent-fg">只看此会话</button>}
    </span>
  );
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
        <Property label="上游" title={request.upstreamName}>{request.upstreamName || '未知上游'}</Property>
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
        <Property label="HTTP">{httpStatus || '-'}</Property>
      </PropertyList>
      <OutcomeNote request={request} presentation={presentation} />
    </>
  );
};

const PaneHeader = ({ request, presentation, timestampText, onClose }) => {
  const StreamIcon = request.isStreaming ? Waves : RefreshCw;
  return (
    <div className="flex h-[34px] shrink-0 items-center gap-2 border-b border-line pl-4 pr-2">
      <span className="truncate font-mono text-[12.5px] font-medium text-fg">{request.requestId}</span>
      <button type="button" title="复制请求 ID" aria-label="复制请求 ID" onClick={() => copyTextToClipboard(request.requestId, '请求 ID')} className={ICON_BUTTON}>
        <Copy size={13} />
      </button>
      <RequestStatusBadge status={presentation.status} />
      <span className="inline-flex shrink-0 items-center gap-1 text-xs text-fg-muted">
        <StreamIcon size={12} className={request.isStreaming ? 'text-info' : undefined} />
        {request.isStreaming ? '流式' : '常规'}
      </span>
      <span className="flex-1" />
      <span className="shrink-0 text-xs tabular-nums text-fg-muted">{timestampText}</span>
      <button type="button" title="隐藏详情面板" aria-label="隐藏详情面板" onClick={onClose} className={ICON_BUTTON}>
        <X size={14} />
      </button>
    </div>
  );
};

const PaneSection = ({ title, className = '', children }) => (
  <section className={`min-w-0 px-4 py-3 ${className}`}>
    <h3 className="mb-2 text-[11.5px] font-semibold text-fg-muted">{title}</h3>
    {children}
  </section>
);

const RequestDetailPane = ({ request: initialRequest, onClose, onFilterSession }) => {
  const { formatTimestamp } = useTimezone();
  const { lifecycle, lifecycleLoading } = useRequestLifecycleDetail(initialRequest?.requestId, Boolean(initialRequest));

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
      {/* 列间 1px 分隔：网格底色当线，单元格铺面板底色；第三栏在两栏布局下占满一行 */}
      <div className="@container min-h-0 flex-1 overflow-y-auto">
        <div className="grid min-h-full grid-cols-1 gap-px bg-line-soft @xl:grid-cols-2 @4xl:grid-cols-[1.3fr_1fr_1.1fr]">
          <PaneSection title="生命周期" className="bg-inspector">
            {/* 按请求重挂载：候选决策的展开状态不跨请求保留 */}
            <LifecyclePanel key={request.requestId} request={request} lifecycle={lifecycle} lifecycleLoading={lifecycleLoading} />
          </PaneSection>
          <PaneSection title="概要" className="bg-inspector">
            <SummarySection request={request} presentation={presentation} onFilterSession={onFilterSession} />
          </PaneSection>
          <div className="min-w-0 bg-inspector @xl:col-span-2 @4xl:col-span-1">
            <PaneSection title="模型与思考">
              <ModelConsistencySection request={request} />
            </PaneSection>
            <PaneSection title="Token 与成本" className="pt-1">
              <TokenSection request={request} />
            </PaneSection>
          </div>
        </div>
      </div>
    </>
  );
};

export default RequestDetailPane;
