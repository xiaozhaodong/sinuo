// ============================================
// CodexSubscriptionProxyCard - Codex 管理页的客户端接入配置卡
// 2026-09-10
//
// 交互边界：
//   - 保存成功不等于启动成功：任何保存/启停后都独立读回运行态再下结论；
//   - 读取失败可重试；保存失败保留输入；revision 冲突只提示刷新，不自动覆盖；
//   - 运行中改监听地址必须先停用（输入框在此状态下禁用，不暗中重启）；
//   - 访问密钥明文只在本组件内存中短暂存在，仅初始化/轮换时出现过一次。
// ============================================

import { useCallback, useEffect, useRef, useState } from 'react';
import {
  AlertCircle,
  CheckCircle2,
  Copy,
  KeyRound,
  RefreshCw,
  Server,
  Shield,
  TerminalSquare
} from 'lucide-react';
import { Button, ErrorMessage, LoadingSpinner } from '@components/ui';
import { resolveErrorMessage } from '@utils/errors.js';
import {
  getCodexSubscriptionProxyConfig,
  getCodexSubscriptionProxyStatus,
  getPrivacySettings,
  isWailsEnvironment,
  normalizeCodexSubscriptionProxyStatus,
  rotateCodexSubscriptionProxyAccess,
  saveCodexSubscriptionProxyConfig,
  setCodexSubscriptionProxyEnabled
} from '@utils/wailsApi.js';
import {
  SUBSCRIPTION_PROXY_MODE_HINTS,
  SUBSCRIPTION_PROXY_MODE_OPTIONS,
  buildCodexConfigSnippet,
  buildSubscriptionProxyAccessURL,
  buildSubscriptionProxyNotice,
  describePrivacyForSubscription,
  isListenLocked,
  isRevisionConflict,
  subscriptionProxyPrivacyWarning,
  subscriptionProxyStateMeta,
  validateSubscriptionProxyForm
} from './subscriptionProxyViewModel.js';
import { subscriptionProxyConfigInput } from './subscriptionProxyOperations.js';

const TONE_CLASSES = {
  neutral: 'bg-surface-mut text-fg-muted',
  success: 'bg-success-soft text-success',
  warn: 'bg-warn-soft text-warn',
  danger: 'bg-danger-soft text-danger'
};

const NOTICE_CLASSES = {
  success: 'bg-success-soft text-success border-success-line',
  error: 'bg-danger-soft text-danger border-danger-line',
  info: 'bg-surface-sub text-fg-body border-line'
};

const DEFAULT_FORM = { mode: 'subscription', listenHost: '127.0.0.1', listenPort: '18089' };

const formFromConfig = (config) => ({
  mode: config?.mode || DEFAULT_FORM.mode,
  listenHost: config?.listenHost || DEFAULT_FORM.listenHost,
  listenPort: String(config?.listenPort ?? DEFAULT_FORM.listenPort)
});

// 判断一次性接入地址是否还对应当前监听地址；只用于比对，不用于拼 URL。
const listenAddressKey = (config) => `${config?.listenHost ?? ''}:${config?.listenPort ?? ''}`;

const StateBadge = ({ state }) => {
  const meta = subscriptionProxyStateMeta(state);
  return (
    <span className={`inline-flex items-center px-2 py-0.5 rounded-lg text-xs font-medium ${TONE_CLASSES[meta.tone]}`}>
      {meta.label}
    </span>
  );
};

const StatusFact = ({ label, value, mono = false }) => (
  <div className="flex items-baseline gap-1.5">
    <span className="text-xs text-fg-subtle">{label}</span>
    <span className={`text-sm font-medium text-fg-body ${mono ? 'font-mono' : ''}`}>{value}</span>
  </div>
);

const ModeSegmented = ({ value, disabled, onChange }) => (
  <div className="inline-flex rounded-lg border border-line bg-surface-sub p-0.5">
    {SUBSCRIPTION_PROXY_MODE_OPTIONS.map((option) => (
      <button
        key={option.value}
        type="button"
        disabled={disabled}
        onClick={() => onChange(option.value)}
        className={`
          px-3 py-1.5 text-xs font-medium rounded-md transition-colors
          ${value === option.value ? 'bg-surface text-accent shadow-sm' : 'text-fg-muted hover:text-fg-body'}
          ${disabled ? 'opacity-50 cursor-not-allowed' : ''}
        `}
      >
        {option.label}
      </button>
    ))}
  </div>
);

const CodexSubscriptionProxyCard = () => {
  const [config, setConfig] = useState(null);
  const [status, setStatus] = useState(null);
  const [privacy, setPrivacy] = useState(null);
  const [form, setForm] = useState(DEFAULT_FORM);
  const [loading, setLoading] = useState(true);
  const [loadError, setLoadError] = useState('');
  const [statusError, setStatusError] = useState('');
  const [privacyError, setPrivacyError] = useState('');
  const [busy, setBusy] = useState(false);
  const [notice, setNotice] = useState(null);
  // 一次性接入信息（明文密钥）只驻留在这里，不写 localStorage、不进日志。
  // secret 必须留着：普通保存不再回传明文，改监听地址后要用它重建接入地址。
  const [accessInfo, setAccessInfo] = useState(null);
  const [copiedKey, setCopiedKey] = useState('');

  // 运行态独立读取：保存成功不代表启动成功，结论只能来自这里。
  const refreshRuntime = useCallback(async () => {
    try {
      const nextStatus = await getCodexSubscriptionProxyStatus();
      setStatus(nextStatus);
      setStatusError('');
      return nextStatus;
    } catch (err) {
      setStatusError(resolveErrorMessage(err, '运行态读取失败'));
      return null;
    }
  }, []);

  // 隐私是补充信息：单独读取，失败只降级隐私区块，不隐藏代理控制能力。
  const loadPrivacy = useCallback(async () => {
    try {
      setPrivacy(await getPrivacySettings());
      setPrivacyError('');
    } catch (err) {
      setPrivacy(null);
      setPrivacyError(resolveErrorMessage(err, '隐私设置读取失败'));
    }
  }, []);

  const load = useCallback(async () => {
    setLoading(true);
    setLoadError('');
    try {
      const nextConfig = await getCodexSubscriptionProxyConfig();
      setConfig(nextConfig);
      setForm(formFromConfig(nextConfig));
    } catch (err) {
      setLoadError(resolveErrorMessage(err, '加载 Codex 订阅代理配置失败'));
      setLoading(false);
      return;
    }
    setLoading(false);
    // 配置是必需项；运行态与隐私任一失败都不阻断其余控制。
    await Promise.all([refreshRuntime(), loadPrivacy()]);
  }, [refreshRuntime, loadPrivacy]);

  useEffect(() => {
    load();
  }, [load]);

  // 运行态摘要事件：只在本卡片没有自己的操作在飞时套用，
  // 避免和「保存后独立读回运行态」的结果互相覆盖。
  const busyRef = useRef(false);
  useEffect(() => {
    busyRef.current = busy;
  }, [busy]);

  useEffect(() => {
    if (!isWailsEnvironment()) return undefined;
    let unsubscribe = null;
    let cancelled = false;

    (async () => {
      try {
        const { EventsOn } = await import('@wailsjs/runtime/runtime');
        if (cancelled) return;
        unsubscribe = EventsOn('codex-subscription-proxy:status', (payload) => {
          if (busyRef.current) return;
          setStatus(normalizeCodexSubscriptionProxyStatus(payload || {}));
          setStatusError('');
        });
      } catch {
        // 事件订阅失败不影响读取与保存：运行态仍可用刷新按钮独立读回。
      }
    })();

    return () => {
      cancelled = true;
      if (typeof unsubscribe === 'function') unsubscribe();
    };
  }, []);

  const applySaveResult = useCallback(async (result, intent, flags = {}) => {
    setConfig(result.config);
    setForm(formFromConfig(result.config));
    if (result.accessUrl) {
      // 初始化/轮换：以服务端返回的权威地址为准。
      setAccessInfo({
        secret: result.accessSecret,
        url: result.accessUrl,
        address: listenAddressKey(result.config)
      });
    }
    const nextStatus = await refreshRuntime();
    setNotice(buildSubscriptionProxyNotice({ intent, config: result.config, status: nextStatus, ...flags }));
  }, [refreshRuntime]);

  // 普通保存不回传明文，但监听地址可能已变：用内存中的密钥按新地址重建，
  // 否则复制出来的仍是旧端口。只在地址确实变化时重建，不覆盖服务端权威地址。
  useEffect(() => {
    if (!accessInfo?.secret || !config) return;
    const address = listenAddressKey(config);
    if (accessInfo.address === address) return;
    setAccessInfo({
      secret: accessInfo.secret,
      url: buildSubscriptionProxyAccessURL(config, accessInfo.secret),
      address
    });
  }, [accessInfo, config]);

  const runAction = useCallback(async (action, intent, flags) => {
    setBusy(true);
    setNotice(null);
    try {
      const result = await action();
      await applySaveResult(result, intent, flags);
    } catch (err) {
      const message = resolveErrorMessage(err, '操作失败');
      setNotice({
        type: 'error',
        text: isRevisionConflict(message) ? `${message}（请先刷新配置，不会自动覆盖）` : message,
        action: isRevisionConflict(message) ? 'refresh' : ''
      });
    } finally {
      setBusy(false);
    }
  }, [applySaveResult]);

  const formDirty = config !== null && (
    form.mode !== config.mode
    || form.listenHost !== config.listenHost
    || Number(form.listenPort) !== config.listenPort
  );

  const handleToggleEnabled = useCallback(() => {
    if (!config || busy) return;
    const nextEnabled = !config.enabled;
    if (nextEnabled && !config.initialized) {
      setNotice({ type: 'error', text: '请先保存一次配置完成初始化（会生成一次性接入密钥），再启用。' });
      return;
    }
    // 启停只切换启用意图，不代提交表单；有未保存改动时先保存，避免编辑被静默丢弃。
    if (formDirty) {
      setNotice({ type: 'error', text: '有未保存的模式或监听地址改动，请先保存配置再启停。' });
      return;
    }
    const current = config;
    runAction(
      () => setCodexSubscriptionProxyEnabled(nextEnabled, current.revision),
      nextEnabled ? 'enable' : 'disable'
    );
  }, [config, busy, formDirty, runAction]);

  const handleSave = useCallback(() => {
    if (!config || busy) return;
    const validationError = validateSubscriptionProxyForm(form);
    if (validationError) {
      setNotice({ type: 'error', text: validationError });
      return;
    }
    const current = config;
    const nextInput = subscriptionProxyConfigInput({
      ...current,
      mode: form.mode,
      listenHost: form.listenHost,
      listenPort: Number(form.listenPort)
    });
    runAction(
      () => saveCodexSubscriptionProxyConfig(nextInput, current.revision),
      'save',
      {
        modeChanged: current.mode !== form.mode,
        listenChanged: current.listenHost !== form.listenHost || current.listenPort !== Number(form.listenPort)
      }
    );
  }, [config, busy, form, runAction]);

  const handleRotate = useCallback(() => {
    if (!config || busy) return;
    if (!window.confirm('轮换后旧接入地址立即失效，已配置该地址的 Codex 需要重新填写新地址。确定轮换吗？')) {
      return;
    }
    const current = config;
    runAction(() => rotateCodexSubscriptionProxyAccess(current.revision), 'rotate');
  }, [config, busy, runAction]);

  const handleCopy = useCallback(async (key, text) => {
    try {
      await navigator.clipboard.writeText(text);
      setCopiedKey(key);
      window.setTimeout(() => setCopiedKey(''), 2000);
    } catch {
      setNotice({ type: 'error', text: '复制失败，请手动选中复制。' });
    }
  }, []);

  if (loadError) {
    return (
      <div className="bg-surface rounded-2xl border border-line shadow-sm p-6">
        <ErrorMessage
          title="Codex 客户端接入配置读取失败"
          message={loadError}
          onRetry={load}
        />
      </div>
    );
  }

  if (loading) {
    return (
      <div className="bg-surface rounded-2xl border border-line shadow-sm p-6">
        <LoadingSpinner text="加载 Codex 客户端接入..." />
      </div>
    );
  }

  const listenLocked = isListenLocked(status);
  const privacyInfo = describePrivacyForSubscription(privacy);
  const privacyWarning = subscriptionProxyPrivacyWarning(privacy);
  const modeHint = SUBSCRIPTION_PROXY_MODE_HINTS[form.mode] || '';
  const statusRunning = status?.state === 'running';
  const configSnippet = accessInfo ? buildCodexConfigSnippet(accessInfo.url) : '';

  return (
    <div className="bg-surface rounded-2xl border border-line shadow-sm overflow-hidden">
      <div className="px-6 py-4 bg-surface-sub border-b border-line-soft flex items-center justify-between gap-4">
        <div className="flex items-center min-w-0">
          <div className="p-1.5 bg-accent-soft rounded-lg mr-3">
            <Server size={16} className="text-accent" />
          </div>
          <div className="min-w-0">
            <div className="flex items-center gap-2">
              <h3 className="font-semibold text-fg">Codex 客户端接入</h3>
              <StateBadge state={status?.state} />
            </div>
            <p className="text-xs text-fg-subtle mt-0.5">
              配置客户端接入、运行状态与凭据；日常模式切换也可在请求追踪页完成
            </p>
          </div>
        </div>
        <Button
          icon={RefreshCw}
          variant="secondary"
          size="sm"
          onClick={load}
          disabled={busy}
        >
          刷新
        </Button>
      </div>

      <div className="p-6 space-y-6">
        {/* 运行态 */}
        <div className="flex flex-wrap items-center gap-x-6 gap-y-2 px-4 py-3 bg-surface-sub border border-line rounded-xl">
          <StatusFact label="运行态" value={subscriptionProxyStateMeta(status?.state).label} />
          <StatusFact label="监听" value={status?.listenAddr || '未监听'} mono />
          <StatusFact label="在途请求" value={status?.activeRequests ?? 0} />
          <StatusFact label="底层 WS 连接数" value={status?.activeWebSockets ?? 0} />
          <StatusFact label="访问密钥" value={config?.accessConfigured ? '已配置' : '未配置'} />
        </div>

        {statusError && (
          <div className="flex items-center justify-between gap-3 px-4 py-3 rounded-lg text-sm bg-warn-soft text-warn border border-warn-line">
            <span className="flex items-center gap-2">
              <AlertCircle size={16} />
              运行态读取失败：{statusError}
            </span>
            <Button variant="secondary" size="sm" icon={RefreshCw} onClick={refreshRuntime} disabled={busy}>
              重试
            </Button>
          </div>
        )}

        {status?.lastError && (
          <div className="flex items-start gap-2 px-4 py-3 rounded-lg text-sm bg-danger-soft text-danger border border-danger-line">
            <AlertCircle size={16} className="mt-0.5 shrink-0" />
            <span>最近一次运行错误：{status.lastError}</span>
          </div>
        )}

        {notice && (
          <div className={`flex items-center justify-between gap-3 px-4 py-3 rounded-lg text-sm border ${NOTICE_CLASSES[notice.type] || NOTICE_CLASSES.info}`}>
            <span className="flex items-center gap-2">
              {notice.type === 'success' ? <CheckCircle2 size={16} /> : <AlertCircle size={16} />}
              {notice.text}
            </span>
            {notice.action === 'refresh' && (
              <Button variant="secondary" size="sm" icon={RefreshCw} onClick={load} disabled={busy}>
                刷新配置
              </Button>
            )}
          </div>
        )}

        {/* 启停 */}
        <div className="flex items-center justify-between gap-4 py-3 border-b border-line-soft">
          <div className="min-w-0">
            <div className="text-sm font-medium text-fg-body">启用独立入口</div>
            <p className="text-xs text-fg-subtle mt-0.5">
              {config?.initialized
                ? '保存的是启用意图；实际是否监听以运行态为准。'
                : '尚未初始化：先保存一次配置生成接入密钥，再启用。'}
            </p>
          </div>
          <button
            type="button"
            role="switch"
            aria-checked={config?.enabled === true}
            aria-label="启用 Codex 客户端接入"
            disabled={busy || !config?.initialized}
            onClick={handleToggleEnabled}
            className={`
              relative inline-flex w-11 h-6 shrink-0 rounded-full transition-colors
              ${config?.enabled ? 'bg-indigo-600' : 'bg-surface-emph'}
              ${busy || !config?.initialized ? 'opacity-50 cursor-not-allowed' : ''}
            `}
          >
            <span className={`
              absolute top-0.5 left-[2px] h-5 w-5 rounded-full bg-surface shadow-sm transition-transform
              ${config?.enabled ? 'translate-x-full' : ''}
            `} />
          </button>
        </div>

        {/* 模式 */}
        <div className="space-y-2 py-3 border-b border-line-soft">
          <div className="flex items-center justify-between gap-4">
            <div>
              <div className="text-sm font-medium text-fg-body">模式</div>
              <p className="text-xs text-fg-subtle mt-0.5">仅控制精确匹配的 Responses 请求。</p>
            </div>
            <ModeSegmented
              value={form.mode}
              disabled={busy}
              onChange={(mode) => setForm((prev) => ({ ...prev, mode }))}
            />
          </div>
          <p className="text-xs text-fg-muted">{modeHint}</p>
        </div>

        {/* 监听地址 */}
        <div className="space-y-2 py-3 border-b border-line-soft">
          <div className="flex items-center justify-between gap-4">
            <div>
              <div className="text-sm font-medium text-fg-body">监听地址</div>
              <p className="text-xs text-fg-subtle mt-0.5">只允许回环地址；端口冲突会明确报错，不自动换端口。</p>
            </div>
            <div className="flex items-center gap-2">
              <input
                type="text"
                value={form.listenHost}
                disabled={busy || listenLocked}
                onChange={(event) => setForm((prev) => ({ ...prev, listenHost: event.target.value }))}
                className="w-36 px-3 py-1.5 bg-surface-sub border border-line rounded-lg text-sm text-fg-body font-mono
                  focus:outline-none focus:ring-2 focus:ring-accent-ring disabled:opacity-50 disabled:cursor-not-allowed"
              />
              <input
                type="number"
                min={1}
                max={65535}
                value={form.listenPort}
                disabled={busy || listenLocked}
                onChange={(event) => setForm((prev) => ({ ...prev, listenPort: event.target.value }))}
                className="w-24 px-3 py-1.5 bg-surface-sub border border-line rounded-lg text-sm text-fg-body font-mono
                  focus:outline-none focus:ring-2 focus:ring-accent-ring disabled:opacity-50 disabled:cursor-not-allowed"
              />
            </div>
          </div>
          {listenLocked && (
            <p className="text-xs text-warn">入口正在运行：修改监听地址前请先停用，不会自动重启。</p>
          )}
        </div>

        <div className="flex items-center gap-3">
          <Button variant="primary" onClick={handleSave} disabled={busy} loading={busy}>
            {config?.initialized ? '保存配置' : '初始化并保存'}
          </Button>
          <span className="text-xs text-fg-subtle">
            {formDirty ? '有未保存的模式或监听地址改动。' : ''}
            保存成功不代表已经监听，实际运行态以本卡顶部为准。
          </span>
        </div>

        {/* 接入信息 */}
        <div className="rounded-xl border border-line overflow-hidden">
          <div className="px-4 py-3 bg-surface-sub border-b border-line-soft flex items-center justify-between gap-3">
            <div className="flex items-center gap-2">
              <KeyRound size={15} className="text-accent" />
              <span className="text-sm font-medium text-fg-body">接入信息</span>
            </div>
            <Button variant="secondary" size="sm" onClick={handleRotate} disabled={busy || !config?.initialized}>
              轮换密钥
            </Button>
          </div>
          <div className="p-4 space-y-4">
            {accessInfo ? (
              <div className="space-y-3">
                <div className="flex items-start gap-2 px-3 py-2 rounded-lg bg-warn-soft text-warn border border-warn-line text-xs">
                  <AlertCircle size={14} className="mt-0.5 shrink-0" />
                  <span>访问密钥明文只在初始化与轮换时展示这一次，离开页面后无法再取回；丢失只能轮换。</span>
                </div>
                <div className="space-y-1.5">
                  <div className="text-xs text-fg-subtle">Codex 接入地址</div>
                  <div className="flex items-start gap-2">
                    <code className="flex-1 min-w-0 break-all px-3 py-2 rounded-lg bg-surface-sub border border-line text-xs font-mono text-fg-body">
                      {accessInfo.url}
                    </code>
                    <Button
                      variant="secondary"
                      size="sm"
                      icon={Copy}
                      onClick={() => handleCopy('url', accessInfo.url)}
                    >
                      {copiedKey === 'url' ? '已复制' : '复制'}
                    </Button>
                  </div>
                </div>
                <div className="space-y-1.5">
                  <div className="flex items-center gap-2 text-xs text-fg-subtle">
                    <TerminalSquare size={13} />
                    Codex 配置片段（不会自动写入你的配置）
                  </div>
                  <div className="flex items-start gap-2">
                    <pre className="flex-1 min-w-0 overflow-x-auto px-3 py-2 rounded-lg bg-surface-sub border border-line text-xs font-mono text-fg-body">
                      {configSnippet}
                    </pre>
                    <Button
                      variant="secondary"
                      size="sm"
                      icon={Copy}
                      onClick={() => handleCopy('snippet', configSnippet)}
                    >
                      {copiedKey === 'snippet' ? '已复制' : '复制'}
                    </Button>
                  </div>
                  <p className="text-xs text-fg-subtle">
                    写入用户级 ~/.codex/config.toml；项目级同名配置不能覆盖。请保留 /backend-api/codex 后缀，
                    改完完整退出并重启 Codex 后再使用。日常切换模式可在本页或请求追踪页完成。
                  </p>
                </div>
                <button
                  type="button"
                  onClick={() => setAccessInfo(null)}
                  className="text-xs text-fg-subtle hover:text-fg-body underline"
                >
                  我已保存好，隐藏本次明文
                </button>
              </div>
            ) : (
              <p className="text-xs text-fg-muted">
                {config?.accessConfigured
                  ? '访问密钥已配置。为保证安全，明文只在初始化与轮换时展示一次；需要新地址请轮换。'
                  : '尚未生成访问密钥：保存一次配置即完成初始化，并会展示一次接入地址。'}
              </p>
            )}
          </div>
        </div>

        {/* 隐私：读取失败只降级本区块，不影响上面的启停与保存 */}
        <div className="flex items-start gap-3 px-4 py-3 rounded-xl border border-line bg-surface-sub">
          <Shield size={16} className="text-accent mt-0.5 shrink-0" />
          <div className="min-w-0 flex-1">
            <div className="text-sm text-fg-body">
              当前隐私模式：<span className="font-medium">{privacyError ? '未读到' : privacyInfo.label}</span>
            </div>
            {privacyError ? (
              <div className="flex items-center justify-between gap-3 mt-1">
                <span className="text-xs text-warn">隐私设置读取失败：{privacyError}</span>
                <Button variant="secondary" size="sm" icon={RefreshCw} onClick={loadPrivacy} disabled={busy}>
                  重试
                </Button>
              </div>
            ) : (
              <p className="text-xs text-fg-subtle mt-0.5">{privacyInfo.detail}</p>
            )}
            <p className="text-xs text-fg-subtle mt-1">
              隐私关闭、仅检测或没有适用规则都不影响启用；实际处理结果按请求详情与隐私保护页为准。
            </p>
            {privacyWarning && <p className="text-xs text-warn mt-1">{privacyWarning}</p>}
          </div>
        </div>

        {statusRunning && (status?.activeWebSockets ?? 0) > 0 && (
          <p className="text-xs text-fg-subtle">
            当前底层 WS 连接数为 {status.activeWebSockets}（包含上下游两侧）；已建立连接在重连后读取新模式。
          </p>
        )}
      </div>
    </div>
  );
};

export default CodexSubscriptionProxyCard;
