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
import { AlertCircle, CheckCircle2, RefreshCw } from 'lucide-react';
import { Button, ErrorMessage, LoadingSpinner } from '@components/ui';
import { FormBlock, FormRow, FormSection } from '@components/desktop/FormSection.jsx';
import SegmentedControl from '@components/desktop/SegmentedControl.jsx';
import Switch from '@components/desktop/Switch.jsx';
import { resolveErrorMessage } from '@utils/errors.js';
import useConfirm from '@hooks/useConfirm.js';
import {
  getCodexSubscriptionProxyConfig,
  getCodexSubscriptionProxyStatus,
  getPrivacySettings,
  isWailsEnvironment,
  normalizeCodexSubscriptionProxyStatus,
  rotateCodexSubscriptionProxyAccess,
  saveCodexSubscriptionProxyConfig,
  setCodexSubscriptionProxyEnabled,
  subscribeToEvent
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
  resolveSubscriptionProxyDisplayState,
  subscriptionProxyPrivacyWarning,
  subscriptionProxyStateMeta,
  validateSubscriptionProxyForm
} from './subscriptionProxyViewModel.js';
import { subscriptionProxyConfigInput } from './subscriptionProxyOperations.js';
import CodexAccessInfoSection from './CodexAccessInfoSection.jsx';
import CodexPrivacySection from './CodexPrivacySection.jsx';

const TONE_CLASSES = {
  neutral: 'tone-slate',
  success: 'tone-emerald',
  warn: 'tone-amber',
  danger: 'tone-rose'
};

const NOTICE_CLASSES = {
  success: 'bg-success-soft text-success border-success-line',
  error: 'bg-danger-soft text-danger border-danger-line',
  info: 'bg-surface-sub text-fg-body border-line'
};

const DEFAULT_FORM = { mode: 'subscription', listenHost: '127.0.0.1', listenPort: '28088' };

const formFromConfig = (config) => ({
  mode: config?.mode || DEFAULT_FORM.mode,
  listenHost: config?.listenHost || DEFAULT_FORM.listenHost,
  listenPort: String(config?.listenPort ?? DEFAULT_FORM.listenPort)
});

// 判断一次性接入地址是否还对应当前监听地址；只用于比对，不用于拼 URL。
const listenAddressKey = (config) => `${config?.listenHost ?? ''}:${config?.listenPort ?? ''}`;

const StateBadge = ({ config, status }) => {
  const meta = resolveSubscriptionProxyDisplayState({ config, status });
  return (
    <span className={`rounded border px-1.5 text-[11px] leading-[18px] ${TONE_CLASSES[meta.tone]}`}>
      {meta.label}
    </span>
  );
};

const StatusFact = ({ label, value, mono = false }) => (
  <div className="flex items-baseline gap-1.5">
    <span className="text-fg-muted">{label}</span>
    <span className={`font-medium tabular-nums text-fg-body ${mono ? 'font-mono' : ''}`}>{value}</span>
  </div>
);

const CodexSubscriptionProxyCard = () => {
  const [config, setConfig] = useState(null);
  const [status, setStatus] = useState(null);
  const { confirm, confirmDialog } = useConfirm();
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

    if (!cancelled) {
      unsubscribe = subscribeToEvent('codex-subscription-proxy:status', (payload) => {
        if (busyRef.current) return;
        setStatus(normalizeCodexSubscriptionProxyStatus(payload || {}));
        setStatusError('');
      });
    }

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

  const handleRotate = useCallback(async () => {
    if (!config || busy) return;
    const ok = await confirm({
      title: '轮换接入地址',
      message: '轮换后旧接入地址立即失效，已配置该地址的 Codex 需要重新填写新地址。确定轮换吗？',
      confirmText: '确认轮换',
      danger: true
    });
    if (!ok) return;
    const current = config;
    runAction(() => rotateCodexSubscriptionProxyAccess(current.revision), 'rotate');
  }, [config, busy, confirm, runAction]);

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
      <div className="mt-5">
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
      <div className="mt-10">
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
    <div>
      {statusError && (
        <div role="alert" className="mt-5 flex items-start gap-2 rounded-lg border border-warn-line bg-warn-soft px-3 py-2 text-xs text-warn">
          <AlertCircle size={14} className="mt-0.5 shrink-0" />
          <span className="min-w-0 flex-1">运行态读取失败：{statusError}</span>
          <Button variant="secondary" size="sm" icon={RefreshCw} onClick={refreshRuntime} disabled={busy}>
            重试
          </Button>
        </div>
      )}

      {status?.lastError && (
        <div role="alert" className="mt-5 flex items-start gap-2 rounded-lg border border-danger-line bg-danger-soft px-3 py-2 text-xs text-danger">
          <AlertCircle size={14} className="mt-0.5 shrink-0" />
          <span>最近一次运行错误：{status.lastError}</span>
        </div>
      )}

      {notice && (
        <div className={`mt-5 flex items-start gap-2 rounded-lg border px-3 py-2 text-xs ${NOTICE_CLASSES[notice.type] || NOTICE_CLASSES.info}`}>
          {notice.type === 'success'
            ? <CheckCircle2 size={14} className="mt-0.5 shrink-0" />
            : <AlertCircle size={14} className="mt-0.5 shrink-0" />}
          <span className="min-w-0 flex-1">{notice.text}</span>
          {notice.action === 'refresh' && (
            <Button variant="secondary" size="sm" icon={RefreshCw} onClick={load} disabled={busy}>
              刷新配置
            </Button>
          )}
        </div>
      )}

      <FormSection
        title="Codex 入口"
        description="配置客户端接入、运行状态与凭据；日常模式切换也可在请求追踪页完成"
        actions={(
          <>
            <StateBadge config={config} status={status} />
            <Button variant="secondary" size="sm" icon={RefreshCw} onClick={load} disabled={busy}>刷新</Button>
          </>
        )}
      >
        <FormBlock>
          <div className="flex flex-wrap gap-x-4 gap-y-1 text-xs">
            <StatusFact label="运行态" value={subscriptionProxyStateMeta(status?.state).label} />
            <StatusFact label="监听" value={status?.listenAddr || '未监听'} mono />
            <StatusFact label="在途请求" value={status?.activeRequests ?? 0} />
            <StatusFact label="底层 WS 连接数" value={status?.activeWebSockets ?? 0} />
            <StatusFact label="访问密钥" value={config?.accessConfigured ? '已配置' : '未配置'} />
          </div>
        </FormBlock>
        <FormRow
          label="启用独立入口"
          description={config?.initialized
            ? '保存的是启用意图；实际是否监听以运行态为准。'
            : '尚未初始化：先保存一次配置生成接入密钥，再启用。'}
        >
          <Switch
            checked={config?.enabled === true}
            label="启用 Codex 客户端接入"
            disabled={busy || !config?.initialized}
            onChange={handleToggleEnabled}
          />
        </FormRow>
        <FormRow label="模式" description={`仅控制精确匹配的 Responses 请求。 ${modeHint}`}>
          <SegmentedControl
            options={SUBSCRIPTION_PROXY_MODE_OPTIONS}
            value={form.mode}
            disabled={busy}
            onChange={(mode) => setForm((prev) => ({ ...prev, mode }))}
            ariaLabel="Codex 客户端接入模式"
          />
        </FormRow>
        <FormRow label="监听地址" description="只允许回环地址；端口冲突会明确报错，不自动换端口。">
          <input
            type="text"
            aria-label="监听主机"
            value={form.listenHost}
            disabled={busy || listenLocked}
            onChange={(event) => setForm((prev) => ({ ...prev, listenHost: event.target.value }))}
            className="desk-field w-28 font-mono"
          />
          <span className="text-xs text-fg-muted">:</span>
          <input
            type="number"
            aria-label="监听端口"
            min={1}
            max={65535}
            value={form.listenPort}
            disabled={busy || listenLocked}
            onChange={(event) => setForm((prev) => ({ ...prev, listenPort: event.target.value }))}
            className="desk-field w-20 text-right font-mono"
          />
        </FormRow>
      </FormSection>
      {listenLocked && (
        <p className="mx-1 mt-1.5 text-xs text-warn">入口正在运行：修改监听地址前请先停用，不会自动重启。</p>
      )}

      <div className="mx-1 mt-3 flex items-center gap-3">
        <Button variant="primary" size="sm" onClick={handleSave} disabled={busy} loading={busy}>
          {config?.initialized ? '保存配置' : '初始化并保存'}
        </Button>
        <span className="text-[11.5px] text-fg-muted">
          {formDirty ? '有未保存的模式或监听地址改动。' : ''}
          保存成功不代表已经监听，实际运行态以本页顶部为准。
        </span>
      </div>

      <CodexAccessInfoSection
        accessInfo={accessInfo}
        config={config}
        configSnippet={configSnippet}
        copiedKey={copiedKey}
        busy={busy}
        onRotate={handleRotate}
        onCopy={handleCopy}
        onHide={() => setAccessInfo(null)}
      />
      <CodexPrivacySection
        privacyError={privacyError}
        privacyInfo={privacyInfo}
        privacyWarning={privacyWarning}
        busy={busy}
        onRetry={loadPrivacy}
      />

      {statusRunning && (status?.activeWebSockets ?? 0) > 0 && (
        <p className="mx-1 mt-3 text-[11.5px] text-fg-muted">
          当前底层 WS 连接数为 {status.activeWebSockets}（包含上下游两侧）；已建立连接在重连后读取新模式。
        </p>
      )}
      {confirmDialog}
    </div>
  );
};

export default CodexSubscriptionProxyCard;
