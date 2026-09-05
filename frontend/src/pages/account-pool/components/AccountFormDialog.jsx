// ============================================
// 账号池账号表单弹窗
// 2026-03-07
// ============================================

import { useRef, useState } from 'react';
import { createPortal } from 'react-dom';
import { Eye, EyeOff, Plus, Trash2 } from 'lucide-react';
import { Button, CustomSelect } from '@components/ui';
import useModalLifecycle from '@hooks/useModalLifecycle.js';
import { maskSessionId } from '../utils.js';
import OAuthHelperPanel from './OAuthHelperPanel.jsx';
import { FormField } from './shared.jsx';
import {
  ACCOUNT_GROUP_OPTIONS,
  AUTH_METHOD_OPTIONS,
  DEFAULT_BASE_URL,
  DEFAULT_MODEL_REWRITE_SOURCE,
  DEFAULT_MODEL_REWRITE_TARGET,
  authMethodToProviderType,
  createDefaultModelRewriteRules,
  isAPIKeyProviderType
} from '../utils.js';

const AccountFormDialog = ({
  open,
  editingAccount,
  accountSubmitting,
  accountCredentialLoading,
  accountForm,
  setAccountForm,
  onClose,
  onSubmit,
  oauthSectionExpanded,
  setOauthSectionExpanded,
  oauthActionLoading,
  oauthSession,
  oauthCallbackURL,
  setOauthCallbackURL,
  onGenerateOAuthLink,
  onExtractRTFromCallback,
  onResetOAuthWorkflow,
  showNotice,
  openExternalURL
}) => {
  const [showCredential, setShowCredential] = useState(true);
  const [credentialResetKey, setCredentialResetKey] = useState({ open: null, account: null });
  const closeButtonRef = useRef(null);
  const handleRequestClose = () => {
    if (!accountSubmitting) onClose();
  };

  // 打开弹窗或切换编辑对象时重置凭据可见性（渲染期调整，避免 effect 级联渲染）
  if (credentialResetKey.open !== open || credentialResetKey.account !== editingAccount) {
    setCredentialResetKey({ open, account: editingAccount });
    setShowCredential(!editingAccount);
  }

  useModalLifecycle({
    open,
    onClose: handleRequestClose,
    initialFocusRef: closeButtonRef
  });

  if (!open) return null;

  const isAPIKeyAccount = isAPIKeyProviderType(accountForm.provider_type || accountForm.auth_method);
  const supportsRequestCompression = isAPIKeyAccount || accountForm.auth_method === 'chatgpt_refresh_token';
  const modelRewriteRules = (() => {
    if (Array.isArray(accountForm.modelRewriteRules) && accountForm.modelRewriteRules.length > 0) {
      return accountForm.modelRewriteRules;
    }
    if (accountForm.modelRewriteSource || accountForm.modelRewriteTarget) {
      return [{
        source: accountForm.modelRewriteSource || DEFAULT_MODEL_REWRITE_SOURCE,
        target: accountForm.modelRewriteTarget || DEFAULT_MODEL_REWRITE_TARGET
      }];
    }
    return createDefaultModelRewriteRules();
  })();
  const updateModelRewriteRules = (updater) => {
    setAccountForm(prev => {
      const current = Array.isArray(prev.modelRewriteRules) && prev.modelRewriteRules.length > 0
        ? prev.modelRewriteRules
        : createDefaultModelRewriteRules();
      const nextRules = typeof updater === 'function' ? updater(current) : updater;
      return {
        ...prev,
        modelRewriteRules: Array.isArray(nextRules) && nextRules.length > 0 ? nextRules : createDefaultModelRewriteRules()
      };
    });
  };
  const maskedCredentialPreview = (() => {
    const raw = String(accountForm.credential_raw || '').trim();
    if (!raw) {
      return '';
    }
    if (raw.startsWith('{')) {
      return raw
        .replace(/"refresh_token"\s*:\s*"([^"]+)"/g, (_, value) => `"refresh_token":"${maskSessionId(value)}"`)
        .replace(/"access_token"\s*:\s*"([^"]+)"/g, (_, value) => `"access_token":"${maskSessionId(value)}"`)
        .replace(/"id_token"\s*:\s*"([^"]+)"/g, (_, value) => `"id_token":"${maskSessionId(value)}"`);
    }
    return maskSessionId(raw);
  })();

  const renderMultiplierInput = (label, key, help = '') => (
    <FormField label={label}>
      <div className="space-y-1.5">
        <input
          type="number"
          step="0.01"
          min="0"
          disabled={!isAPIKeyAccount}
          value={accountForm[key]}
          onChange={(event) => setAccountForm(prev => ({ ...prev, [key]: event.target.value }))}
          className={`w-full rounded-lg border px-3 py-2 text-sm focus:border-accent-line focus:outline-none focus:ring-2 focus:ring-accent-soft ${
            isAPIKeyAccount ? 'border-line' : 'border-line-soft bg-surface-sub text-fg-subtle'
          }`}
          placeholder="1.0"
        />
        {help ? <div className="text-[11px] text-fg-subtle">{help}</div> : null}
      </div>
    </FormField>
  );

  return createPortal(
    <div className="fixed inset-0 z-[60] flex items-start justify-center px-4 pt-[15vh]">
      <div className="absolute inset-0 bg-overlay" />
      <form
        onSubmit={onSubmit}
        role="dialog"
        aria-modal="true"
        aria-label={editingAccount ? '编辑账号' : '新增账号'}
        className="relative flex w-full max-w-2xl max-h-[75vh] flex-col overflow-hidden rounded-2xl border border-line bg-surface shadow-2xl"
      >
        <div className="flex items-center justify-between border-b border-line-soft px-6 py-4">
          <h3 className="text-lg font-semibold text-fg">{editingAccount ? '编辑账号' : '新增账号'}</h3>
          <button
            type="button"
            ref={closeButtonRef}
            className="text-sm text-fg-subtle hover:text-fg-body"
            onClick={handleRequestClose}
            disabled={accountSubmitting}
          >
            关闭
          </button>
        </div>

        <div className="flex-1 overflow-y-auto px-6 py-5">
          <div className="space-y-6">
            <section className="space-y-4">
              <h4 className="text-sm font-semibold uppercase tracking-wide text-fg-muted">基本信息</h4>

              <FormField label="账号名称" required>
                <input
                  type="text"
                  value={accountForm.account_name}
                  onChange={(event) => setAccountForm(prev => ({ ...prev, account_name: event.target.value }))}
                  className="w-full rounded-lg border border-line px-3 py-2 text-sm focus:border-accent-line focus:outline-none focus:ring-2 focus:ring-accent-soft"
                  placeholder="例如：openai-auth-main"
                />
              </FormField>

              <div className="space-y-2">
                <div className="text-xs font-medium text-fg-body">
                  授权方式
                  <span className="ml-1 text-danger">*</span>
                </div>
                <div className="grid grid-cols-1 gap-2 md:grid-cols-2">
                  {AUTH_METHOD_OPTIONS.map((option) => {
                    const active = accountForm.auth_method === option.value;
                    return (
                      <button
                        key={option.value}
                        type="button"
                        onClick={() => {
                          const switchingIntoChatGPT = accountForm.auth_method !== 'chatgpt_refresh_token' && option.value === 'chatgpt_refresh_token';
                          setAccountForm(prev => ({
                            ...prev,
                            auth_method: option.value,
                            provider_type: authMethodToProviderType(option.value),
                            ...(option.value === 'api_key' ? {} : {
                              costMultiplier: '1.0',
                              inputCostMultiplier: '1.0',
                              outputCostMultiplier: '1.0',
                              cacheCreationCostMultiplier: '1.0',
                              cacheCreationCostMultiplier1h: '1.0',
                              cacheReadCostMultiplier: '1.0',
                              enableRequestCompression: false,
                              modelRewriteEnabled: false,
                              modelRewriteRules: createDefaultModelRewriteRules()
                            })
                          }));
                          if (option.value !== 'chatgpt_refresh_token') {
                            setOauthSectionExpanded(false);
                            onResetOAuthWorkflow();
                          } else if (!editingAccount || switchingIntoChatGPT) {
                            setOauthSectionExpanded(true);
                          }
                        }}
                        className={`rounded-lg border px-3 py-2 text-left transition-colors ${
                          active
                            ? 'border-success-line bg-success-soft'
                            : 'border-line hover:bg-surface-sub'
                        }`}
                      >
                        <div className={`text-sm font-medium ${active ? 'text-success' : 'text-fg-body'}`}>{option.label}</div>
                        <div className="text-xs text-fg-muted">{option.description}</div>
                      </button>
                    );
                  })}
                </div>
              </div>
            </section>

            <section className="space-y-3">
              <h4 className="text-sm font-semibold uppercase tracking-wide text-fg-muted">请求压缩</h4>
              <label className={`flex items-start gap-2 text-sm ${supportsRequestCompression ? 'text-fg-body' : 'text-fg-subtle'}`}>
                <input
                  type="checkbox"
                  checked={Boolean(accountForm.enableRequestCompression)}
                  disabled={!supportsRequestCompression}
                  onChange={(event) => setAccountForm(prev => ({ ...prev, enableRequestCompression: event.target.checked }))}
                  className="mt-0.5 app-checkbox"
                />
                <span>向上游发送 zstd 压缩请求体</span>
              </label>
              <div className="rounded-lg bg-surface-sub px-3 py-2 text-xs leading-5 text-fg-muted">
                关闭时发送明文 JSON；API Key 和 ChatGPT OAuth 账号均可开启。第三方上游请先确认支持请求头 Content-Encoding: zstd。
              </div>
            </section>

            <section className="space-y-4">
              <h4 className="text-sm font-semibold uppercase tracking-wide text-fg-muted">路由配置</h4>

              <div className="grid grid-cols-[2fr_2fr_3fr] gap-4">
                <FormField label="组别">
                  <CustomSelect
                    options={ACCOUNT_GROUP_OPTIONS}
                    value={accountForm.group_key || 'primary'}
                    onChange={(val) => setAccountForm(prev => ({ ...prev, group_key: val }))}
                    size="md"
                    className="w-full"
                  />
                </FormField>

                <FormField label="组内顺序">
                  <input
                    type="number"
                    min="10"
                    step="10"
                    value={accountForm.priority}
                    onChange={(event) => setAccountForm(prev => ({ ...prev, priority: event.target.value }))}
                    className="w-full rounded-lg border border-line px-3 py-2 text-sm focus:border-accent-line focus:outline-none focus:ring-2 focus:ring-accent-soft"
                    placeholder="10"
                  />
                </FormField>

                <FormField label="Base URL（可选）">
                  <input
                    type="url"
                    value={accountForm.base_url}
                    onChange={(event) => setAccountForm(prev => ({ ...prev, base_url: event.target.value }))}
                    className="w-full rounded-lg border border-line px-3 py-2 text-sm focus:border-accent-line focus:outline-none focus:ring-2 focus:ring-accent-soft"
                    placeholder={DEFAULT_BASE_URL}
                  />
                </FormField>
              </div>

              <div className="rounded-lg bg-surface-sub px-3 py-2 text-xs leading-5 text-fg-muted">
                调度优先级：主组 → 备组 → 冷备，组内按顺序、额度和健康度择优。顺序建议 10、20、30。
              </div>

              <label className="inline-flex items-start gap-2 text-sm text-fg-body">
                <input
                  type="checkbox"
                  checked={accountForm.enabled}
                  onChange={(event) => setAccountForm(prev => ({ ...prev, enabled: event.target.checked }))}
                  className="mt-0.5 app-checkbox"
                />
                <span>{editingAccount ? '保持账号启用状态' : '创建后立即启用'}</span>
              </label>
            </section>

            <section className="space-y-4">
              <h4 className="text-sm font-semibold uppercase tracking-wide text-fg-muted">上游兼容</h4>

              <label className="flex items-start gap-2 text-sm text-fg-body">
                <input
                  type="checkbox"
                  checked={accountForm.stripCodexInternalMetadata === true}
                  disabled={accountSubmitting}
                  onChange={(event) => setAccountForm(prev => ({ ...prev, stripCodexInternalMetadata: event.target.checked }))}
                  className="mt-0.5 app-checkbox"
                  aria-describedby="codex-metadata-help"
                />
                <span>移除 Codex 内部元数据</span>
              </label>
              <div id="codex-metadata-help" className="rounded-lg bg-surface-sub px-3 py-2 text-xs leading-5 text-fg-muted">
                用于不接受 Codex 内部工具追踪或内容分类元数据的上游。开启后仅清理内部元数据，不改变工具参数、返回值或压缩内容。不提供远程压缩兼容。
              </div>

              <label className={`flex items-start gap-2 text-sm ${isAPIKeyAccount ? 'text-fg-body' : 'text-fg-subtle'}`}>
                <input
                  type="checkbox"
                  checked={Boolean(accountForm.modelRewriteEnabled)}
                  disabled={!isAPIKeyAccount}
                  onChange={(event) => setAccountForm(prev => ({
                    ...prev,
                    modelRewriteEnabled: event.target.checked,
                    modelRewriteRules: Array.isArray(prev.modelRewriteRules) && prev.modelRewriteRules.length > 0
                      ? prev.modelRewriteRules
                      : createDefaultModelRewriteRules()
                  }))}
                  className="mt-0.5 app-checkbox"
                />
                <span>启用模型兼容改写</span>
              </label>

              {isAPIKeyAccount && accountForm.modelRewriteEnabled && (
                <div className="space-y-3">
                  {modelRewriteRules.map((rule, index) => (
                    <div key={index} className="grid grid-cols-1 gap-3 md:grid-cols-[1fr_1fr_auto]">
                      <FormField label={`匹配模型${modelRewriteRules.length > 1 ? ` ${index + 1}` : ''}`}>
                        <input
                          type="text"
                          value={rule.source ?? ''}
                          onChange={(event) => updateModelRewriteRules((rules) => rules.map((item, itemIndex) => (
                            itemIndex === index ? { ...item, source: event.target.value } : item
                          )))}
                          className="w-full rounded-lg border border-line px-3 py-2 text-sm focus:border-accent-line focus:outline-none focus:ring-2 focus:ring-accent-soft"
                          placeholder={DEFAULT_MODEL_REWRITE_SOURCE}
                        />
                      </FormField>
                      <FormField label={`替代模型${modelRewriteRules.length > 1 ? ` ${index + 1}` : ''}`}>
                        <input
                          type="text"
                          value={rule.target ?? ''}
                          onChange={(event) => updateModelRewriteRules((rules) => rules.map((item, itemIndex) => (
                            itemIndex === index ? { ...item, target: event.target.value } : item
                          )))}
                          className="w-full rounded-lg border border-line px-3 py-2 text-sm focus:border-accent-line focus:outline-none focus:ring-2 focus:ring-accent-soft"
                          placeholder={DEFAULT_MODEL_REWRITE_TARGET}
                        />
                      </FormField>
                      <div>
                        <div className="mb-1.5 text-xs font-medium text-transparent select-none" aria-hidden="true">删除</div>
                        <button
                          type="button"
                          aria-label={`删除模型兼容规则 ${index + 1}`}
                          title="删除规则"
                          disabled={modelRewriteRules.length <= 1}
                          onClick={() => updateModelRewriteRules((rules) => rules.filter((_, itemIndex) => itemIndex !== index))}
                          className={`inline-flex h-10 w-10 items-center justify-center rounded-lg border transition-colors ${
                            modelRewriteRules.length <= 1
                              ? 'cursor-not-allowed border-line-soft text-fg-subtle'
                              : 'border-line text-fg-subtle hover:border-danger-line hover:bg-danger-soft hover:text-danger'
                          }`}
                        >
                          <Trash2 size={15} />
                        </button>
                      </div>
                    </div>
                  ))}
                  <button
                    type="button"
                    onClick={() => updateModelRewriteRules((rules) => [...rules, { source: '', target: '' }])}
                    className="inline-flex items-center gap-1.5 rounded-lg border border-line px-3 py-2 text-sm font-medium text-fg-body transition-colors hover:border-accent-line hover:bg-accent-soft hover:text-accent"
                  >
                    <Plus size={15} />
                    添加规则
                  </button>
                </div>
              )}

              <div className="rounded-lg bg-surface-sub px-3 py-2 text-xs leading-5 text-fg-muted">
                仅在 Codex /v1/responses 与 /v1/responses/compact 转发前替换请求模型。
              </div>
            </section>

            <section className="space-y-4">
              <h4 className="text-sm font-semibold uppercase tracking-wide text-fg-muted">认证信息</h4>

              <FormField label={accountForm.auth_method === 'chatgpt_refresh_token' ? 'ChatGPT Refresh Token (rt)' : 'API Key'} required>
                <div className="space-y-2">
                  <div className="flex items-center justify-between">
                    <div className="text-[11px] text-fg-subtle">
                      {editingAccount ? '已改为按需读取完整凭据，默认隐藏' : '支持直接粘贴完整凭据'}
                    </div>
                    <button
                      type="button"
                      onClick={() => setShowCredential(prev => !prev)}
                      className="inline-flex items-center gap-1 rounded-md border border-line px-2 py-1 text-xs text-fg-muted transition-colors hover:bg-surface-sub hover:text-fg-body"
                      title={showCredential ? '隐藏凭据' : '显示凭据'}
                    >
                      {showCredential ? <EyeOff size={14} /> : <Eye size={14} />}
                      {showCredential ? '隐藏' : '显示'}
                    </button>
                  </div>

                  <textarea
                    value={showCredential ? accountForm.credential_raw : maskedCredentialPreview}
                    onChange={(event) => {
                      if (!showCredential) {
                        return;
                      }
                      setAccountForm(prev => ({ ...prev, credential_raw: event.target.value }));
                    }}
                    readOnly={!showCredential || accountCredentialLoading}
                    className={`min-h-[120px] w-full rounded-lg border px-3 py-2 text-sm font-mono focus:border-accent-line focus:outline-none focus:ring-2 focus:ring-accent-soft ${
                      !showCredential || accountCredentialLoading
                        ? 'border-line-soft bg-surface-sub text-fg-subtle'
                        : 'border-line'
                    }`}
                    placeholder={
                      accountCredentialLoading
                        ? '正在加载已保存凭据...'
                        : accountForm.auth_method === 'chatgpt_refresh_token'
                          ? '粘贴 Refresh Token，例如：rt-xxxxxx'
                          : '例如: sk-xxxxxx'
                    }
                  />
                </div>
              </FormField>

              {accountForm.auth_method === 'chatgpt_refresh_token' && (
                <OAuthHelperPanel
                  editingAccount={editingAccount}
                  oauthSectionExpanded={oauthSectionExpanded}
                  setOauthSectionExpanded={setOauthSectionExpanded}
                  oauthActionLoading={oauthActionLoading}
                  oauthSession={oauthSession}
                  oauthCallbackURL={oauthCallbackURL}
                  setOauthCallbackURL={setOauthCallbackURL}
                  onGenerateOAuthLink={onGenerateOAuthLink}
                  onExtractRTFromCallback={onExtractRTFromCallback}
                  showNotice={showNotice}
                  openExternalURL={openExternalURL}
                />
              )}
            </section>

            <section className="space-y-4">
              <h4 className="text-sm font-semibold uppercase tracking-wide text-fg-muted">成本倍率配置</h4>
              <div className="rounded-lg bg-surface-sub px-3 py-2 text-xs leading-5 text-fg-muted">
                {isAPIKeyAccount
                  ? '仅 API Key 账号支持自定义成本倍率，默认为 1.0。'
                  : '当前账号类型固定使用默认倍率 1.0，不支持自定义。'}
              </div>
              <div className="grid grid-cols-1 gap-4 md:grid-cols-2">
                {renderMultiplierInput('总成本倍率', 'costMultiplier')}
                {renderMultiplierInput('输入成本倍率', 'inputCostMultiplier')}
                {renderMultiplierInput('输出成本倍率', 'outputCostMultiplier')}
                {renderMultiplierInput('缓存读取成本倍率', 'cacheReadCostMultiplier')}
                {renderMultiplierInput('5分钟缓存创建倍率', 'cacheCreationCostMultiplier', 'Claude / Codex 默认短缓存口径')}
                {renderMultiplierInput('1小时缓存创建倍率', 'cacheCreationCostMultiplier1h')}
              </div>
            </section>
          </div>
        </div>

        <div className="flex items-center justify-end gap-2 border-t border-line-soft bg-surface px-6 py-4">
          <Button type="button" variant="ghost" onClick={handleRequestClose} disabled={accountSubmitting}>
            取消
          </Button>
          <Button type="submit" loading={accountSubmitting}>
            {editingAccount ? '保存修改' : '创建账号'}
          </Button>
        </div>
      </form>
    </div>,
    document.body
  );
};

export default AccountFormDialog;
