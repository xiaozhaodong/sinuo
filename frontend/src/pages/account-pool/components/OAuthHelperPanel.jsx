// ============================================
// Account Pool OAuth 辅助面板
// 2026-03-07
// ============================================

import { ChevronDown, ChevronUp } from 'lucide-react';
import { Button } from '@components/ui';
import { FormFieldRow, FormSection } from '@components/desktop/FormSection.jsx';
import { copyTextToClipboard } from '../../requests/components/clipboard.js';
import { shouldAutoExtractCallback } from '../utils.js';

const OAuthHelperPanel = ({
  editingAccount,
  oauthSectionExpanded,
  setOauthSectionExpanded,
  oauthActionLoading,
  oauthSession,
  oauthCallbackURL,
  setOauthCallbackURL,
  onGenerateOAuthLink,
  onExtractRTFromCallback,
  showNotice,
  openExternalURL
}) => {
  const handleBrowserLogin = async () => {
    const session = await onGenerateOAuthLink({ notifySuccess: false });
    if (!session) return;

    try {
      const opened = await openExternalURL(session.auth_url);
      if (opened) {
        showNotice('success', '已在浏览器打开登录页，完成后粘贴回调 URL');
      } else {
        showNotice('error', '打开授权页失败，请改用复制链接');
      }
    } catch {
      showNotice('error', '打开授权页失败，请改用复制链接');
    }
  };

  const handleCopyLink = async () => {
    const session = oauthSession?.auth_url ? oauthSession : await onGenerateOAuthLink();
    if (!session) return;

    try {
      const copied = await copyTextToClipboard(session.auth_url, '授权链接');
      showNotice(copied ? 'success' : 'error', copied ? '授权链接已复制' : '复制失败，请手动复制');
    } catch {
      showNotice('error', '复制失败，请手动复制');
    }
  };

  const handleCallbackPaste = (event) => {
    const text = event.clipboardData.getData('text');
    // 保留原生粘贴，由 onChange 更新输入值；提取直接使用本次粘贴文本。
    if (oauthSession?.session_id && !oauthActionLoading && shouldAutoExtractCallback(text)) {
      void onExtractRTFromCallback(text);
    }
  };

  return (
    <FormSection
      title={editingAccount ? '重新授权 / 更新 RT' : '通过 ChatGPT 登录获取'}
      description={editingAccount
        ? '已有 RT 可直接在上方凭据框修改；仅在需要重新登录提取时再展开。'
        : '不想手动复制 RT 时，可在这里完成登录并自动提取。'}
      actions={
        <Button
          type="button"
          variant="ghost"
          size="sm"
          icon={oauthSectionExpanded ? ChevronUp : ChevronDown}
          onClick={() => setOauthSectionExpanded(prev => !prev)}
        >
          {oauthSectionExpanded ? '收起' : '展开'}
        </Button>
      }
    >
      {oauthSectionExpanded && (
        <>
          <FormFieldRow label="① 登录" hint="登录完成后，复制浏览器地址栏中的完整回调地址">
            <div className="flex flex-wrap gap-2">
              <Button type="button" size="sm" onClick={handleBrowserLogin} loading={oauthActionLoading}>
                在浏览器中登录
              </Button>
              <Button type="button" variant="secondary" size="sm" onClick={handleCopyLink} disabled={oauthActionLoading}>
                复制链接
              </Button>
            </div>
          </FormFieldRow>
          <FormFieldRow
            label="② 回调 URL"
            htmlFor="account-oauth-callback-url"
            hint="粘贴后自动提取，提取后填入上方 Refresh Token"
          >
            <div className="flex items-center gap-2">
              <input
                id="account-oauth-callback-url"
                type="text"
                value={oauthCallbackURL}
                onChange={(event) => setOauthCallbackURL(event.target.value)}
                onPaste={handleCallbackPaste}
                className="desk-field w-full font-mono"
                placeholder="粘贴登录后浏览器地址栏中的完整回调 URL"
              />
              <Button
                type="button"
                variant="secondary"
                size="sm"
                onClick={onExtractRTFromCallback}
                loading={oauthActionLoading}
                disabled={!oauthSession?.session_id || !oauthCallbackURL.trim()}
              >
                提取 RT
              </Button>
            </div>
          </FormFieldRow>
        </>
      )}
    </FormSection>
  );
};

export default OAuthHelperPanel;
