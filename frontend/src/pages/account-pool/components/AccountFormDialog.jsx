import SoftFailurePolicyFields from '@components/ui/SoftFailurePolicyFields.jsx';

// ============================================
// 账号池账号表单弹窗
// 2026-03-07
// ============================================

import { useRef, useState } from 'react';
import { Eye, EyeOff, Plus, Trash2 } from 'lucide-react';
import { Button, CustomSelect } from '@components/ui';
import Sheet from '@components/desktop/Sheet.jsx';
import Switch from '@components/desktop/Switch.jsx';
import SegmentedControl from '@components/desktop/SegmentedControl.jsx';
import { FormFieldRow, FormRow, FormSection } from '@components/desktop/FormSection.jsx';
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
  const nameInputRef = useRef(null);
  const handleRequestClose = () => {
    if (!accountSubmitting) onClose();
  };

  // 打开弹窗或切换编辑对象时重置凭据可见性（渲染期调整，避免 effect 级联渲染）
  if (credentialResetKey.open !== open || credentialResetKey.account !== editingAccount) {
    setCredentialResetKey({ open, account: editingAccount });
    setShowCredential(!editingAccount);
  }


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
          className={`desk-field w-24 text-right ${
            isAPIKeyAccount ? 'border-line' : 'border-line-soft bg-surface-sub text-fg-subtle'
          }`}
          placeholder="1.0"
        />
        {help ? <div className="text-[11px] text-fg-subtle">{help}</div> : null}
      </div>
    </FormField>
  );

  return (
    <Sheet open={open} title={editingAccount ? '编辑账号' : '新增账号'} label={editingAccount ? '编辑账号' : '新增账号'}
      width={600} onSubmit={onSubmit} onClose={handleRequestClose} closeDisabled={accountSubmitting}
      initialFocusRef={nameInputRef}
      footer={<>
        <Button type="button" variant="ghost" onClick={handleRequestClose} disabled={accountSubmitting}>取消</Button>
        <Button type="submit" loading={accountSubmitting}>{editingAccount ? '保存修改' : '创建账号'}</Button>
      </>}>
            <FormSection title="基本信息">

              <FormField label="账号名称" required>
                <input
                  type="text"
                  ref={nameInputRef}
                  value={accountForm.account_name}
                  onChange={(event) => setAccountForm(prev => ({ ...prev, account_name: event.target.value }))}
                  className="desk-field w-full"
                  placeholder="例如：openai-auth-main"
                />
              </FormField>

              <FormFieldRow
                label="授权方式"
                required
                hint={AUTH_METHOD_OPTIONS.find((option) => option.value === accountForm.auth_method)?.description}
              >
                <SegmentedControl
                  options={AUTH_METHOD_OPTIONS}
                  value={accountForm.auth_method}
                  ariaLabel="授权方式"
                  onChange={(authMethod) => {
                    const switchingIntoChatGPT = accountForm.auth_method !== 'chatgpt_refresh_token' && authMethod === 'chatgpt_refresh_token';
                    setAccountForm(prev => ({
                      ...prev,
                      auth_method: authMethod,
                      provider_type: authMethodToProviderType(authMethod),
                      ...(authMethod === 'api_key' ? {} : {
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
                    if (authMethod !== 'chatgpt_refresh_token') {
                      setOauthSectionExpanded(false);
                      onResetOAuthWorkflow();
                    } else if (!editingAccount || switchingIntoChatGPT) {
                      setOauthSectionExpanded(true);
                    }
                  }}
                />
              </FormFieldRow>
            </FormSection>

            <SoftFailurePolicyFields
              value={accountForm}
              disabled={accountSubmitting || accountCredentialLoading}
              onChange={(patch) => setAccountForm((current) => ({ ...current, ...patch }))}
            />

            <FormSection title="请求压缩">
              <FormRow
                label="向上游发送 zstd 压缩请求体"
                description="关闭时发送明文 JSON；API Key 和 ChatGPT OAuth 账号均可开启。第三方上游请先确认支持请求头 Content-Encoding: zstd。"
              >
                <Switch label="向上游发送 zstd 压缩请求体" checked={Boolean(accountForm.enableRequestCompression)}
                  disabled={!supportsRequestCompression}
                  onChange={(enableRequestCompression) => setAccountForm(prev => ({ ...prev, enableRequestCompression }))} />
              </FormRow>
            </FormSection>

            <FormSection title="路由配置">

              <div className="divide-y divide-line-soft">
                <FormField label="组别">
                  <CustomSelect
                    options={ACCOUNT_GROUP_OPTIONS}
                    value={accountForm.group_key || 'primary'}
                    onChange={(val) => setAccountForm(prev => ({ ...prev, group_key: val }))}
                    size="sm"
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
                    className="desk-field w-24 text-right"
                    placeholder="10"
                  />
                </FormField>

                <FormField label="Base URL（可选）">
                  <input
                    type="url"
                    value={accountForm.base_url}
                    onChange={(event) => setAccountForm(prev => ({ ...prev, base_url: event.target.value }))}
                    className="desk-field w-full"
                    placeholder={DEFAULT_BASE_URL}
                  />
                </FormField>
              </div>

              <div className="px-3.5 py-3 text-xs leading-5 text-fg-muted">
                调度优先级：主组 → 备组 → 冷备，组内按顺序、额度和健康度择优。顺序建议 10、20、30。
              </div>

              <FormRow label={editingAccount ? '保持账号启用状态' : '创建后立即启用'}>
                <Switch label={editingAccount ? '保持账号启用状态' : '创建后立即启用'} checked={accountForm.enabled}
                  onChange={(enabled) => setAccountForm(prev => ({ ...prev, enabled }))} />
              </FormRow>
            </FormSection>

            <FormSection title="上游兼容">

              <FormRow label="移除 Codex 内部元数据">
                <span role="group" aria-label="移除 Codex 内部元数据" aria-describedby="codex-metadata-help" className="inline-flex">
                <Switch label="移除 Codex 内部元数据" checked={accountForm.stripCodexInternalMetadata === true} disabled={accountSubmitting}
                  onChange={(stripCodexInternalMetadata) => setAccountForm(prev => ({ ...prev, stripCodexInternalMetadata }))} />
                </span>
              </FormRow>
              <div id="codex-metadata-help" className="px-3.5 py-3 text-xs leading-5 text-fg-muted">
                用于不接受 Codex 内部工具追踪或内容分类元数据的上游。开启后仅清理内部元数据，不改变工具参数、返回值或压缩内容。不提供远程压缩兼容。
              </div>

              <FormRow label="启用模型兼容改写">
                <Switch label="启用模型兼容改写" checked={Boolean(accountForm.modelRewriteEnabled)} disabled={!isAPIKeyAccount}
                  onChange={(modelRewriteEnabled) => setAccountForm(prev => ({
                    ...prev, modelRewriteEnabled,
                    modelRewriteRules: Array.isArray(prev.modelRewriteRules) && prev.modelRewriteRules.length > 0
                      ? prev.modelRewriteRules : createDefaultModelRewriteRules()
                  }))} />
              </FormRow>

              {isAPIKeyAccount && accountForm.modelRewriteEnabled && (
                <div className="space-y-2 px-3.5 py-3">
                  {modelRewriteRules.map((rule, index) => (
                    <div key={index} className="grid grid-cols-[1fr_1fr_auto] items-end gap-2 [&>label]:block [&>label]:p-0 [&>label>span:first-child]:mb-1 [&>label>span:first-child]:block [&>label>span:first-child]:w-auto [&>label>span:first-child]:text-left">
                      <FormField label={`匹配模型${modelRewriteRules.length > 1 ? ` ${index + 1}` : ''}`}>
                        <input
                          type="text"
                          value={rule.source ?? ''}
                          onChange={(event) => updateModelRewriteRules((rules) => rules.map((item, itemIndex) => (
                            itemIndex === index ? { ...item, source: event.target.value } : item
                          )))}
                          className="desk-field w-full"
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
                          className="desk-field w-full"
                          placeholder={DEFAULT_MODEL_REWRITE_TARGET}
                        />
                      </FormField>
                      <div>
                        <div className="hidden" aria-hidden="true">删除</div>
                        <button
                          type="button"
                          aria-label={`删除模型兼容规则 ${index + 1}`}
                          title="删除规则"
                          disabled={modelRewriteRules.length <= 1}
                          onClick={() => updateModelRewriteRules((rules) => rules.filter((_, itemIndex) => itemIndex !== index))}
                          className={`inline-flex h-6 w-6 items-center justify-center rounded transition-colors ${
                            modelRewriteRules.length <= 1
                              ? 'cursor-not-allowed text-fg-subtle'
                              : 'text-fg-muted hover:bg-surface-mut hover:text-danger'
                          }`}
                        >
                          <Trash2 size={14} />
                        </button>
                      </div>
                    </div>
                  ))}
                  <Button type="button" variant="ghost" size="sm" icon={Plus} onClick={() => updateModelRewriteRules((rules) => [...rules, { source: '', target: '' }])}>添加规则</Button>
                </div>
              )}

              <div className="px-3.5 py-3 text-xs leading-5 text-fg-muted">
                仅在 Codex /v1/responses 与 /v1/responses/compact 转发前替换请求模型。
              </div>
            </FormSection>

            <FormSection title="认证信息">

              <FormField label={accountForm.auth_method === 'chatgpt_refresh_token' ? 'Refresh Token' : 'API Key'} required>
                <div className="space-y-2">
                  <div className="relative">
                    <textarea
                      value={showCredential ? accountForm.credential_raw : maskedCredentialPreview}
                      onChange={(event) => {
                        if (!showCredential) {
                          return;
                        }
                        setAccountForm(prev => ({ ...prev, credential_raw: event.target.value }));
                      }}
                      readOnly={!showCredential || accountCredentialLoading}
                      rows={3}
                      className={`desk-field h-auto w-full py-1.5 pr-9 font-mono leading-relaxed ${
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
                    <button
                      type="button"
                      onClick={() => setShowCredential(prev => !prev)}
                      className="absolute right-1 top-1 inline-flex h-6 w-6 items-center justify-center rounded text-fg-muted transition-colors hover:bg-surface-mut hover:text-fg-body"
                      title={showCredential ? '隐藏凭据' : '显示凭据'}
                      aria-label={showCredential ? '隐藏凭据' : '显示凭据'}
                    >
                      {showCredential ? <EyeOff size={14} /> : <Eye size={14} />}
                    </button>
                  </div>
                  <div className="text-[11.5px] text-fg-muted">
                    {editingAccount ? '已改为按需读取完整凭据，默认隐藏' : '支持直接粘贴完整凭据'}
                  </div>
                </div>
              </FormField>
            </FormSection>

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

            <FormSection title="成本倍率配置">
              <div className="px-3.5 py-3 text-xs leading-5 text-fg-muted">
                {isAPIKeyAccount
                  ? '仅 API Key 账号支持自定义成本倍率，默认为 1.0。'
                  : '当前账号类型固定使用默认倍率 1.0，不支持自定义。'}
              </div>
              <div className="divide-y divide-line-soft">
                {renderMultiplierInput('总成本倍率', 'costMultiplier')}
                {renderMultiplierInput('输入成本倍率', 'inputCostMultiplier')}
                {renderMultiplierInput('输出成本倍率', 'outputCostMultiplier')}
                {renderMultiplierInput('缓存读取成本倍率', 'cacheReadCostMultiplier')}
                {renderMultiplierInput('5分钟缓存创建倍率', 'cacheCreationCostMultiplier', 'Claude / Codex 默认短缓存口径')}
                {renderMultiplierInput('1小时缓存创建倍率', 'cacheCreationCostMultiplier1h')}
              </div>
            </FormSection>
    </Sheet>
  );
};
export default AccountFormDialog;
