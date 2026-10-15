// Claude Code 客户端接入卡片：专用 loopback 入口的启停、端口、接入模式与客户端配置。
// 接入模式二选一：订阅直通直连 Anthropic，端点与主端口一样走第三方端点；两者不混用。
// 2026-10-07 桌面化：系统设置式分组表单（入口 / 客户端配置两组），由 Claude 管理页的浅灰底承载。

import { useState } from 'react';
import { AlertCircle, CheckCircle2, Copy, RefreshCw } from 'lucide-react';
import { Button } from '@components/ui';
import { FormBlock, FormRow, FormSection } from '@components/desktop/FormSection.jsx';
import SegmentedControl from '@components/desktop/SegmentedControl.jsx';
import Switch from '@components/desktop/Switch.jsx';
import useConfirm from '@hooks/useConfirm.js';
import { copyTextToClipboard } from '@pages/requests/components/clipboard.js';
import useClaudeCodeEntry from './useClaudeCodeEntry.js';
import {
  buildClaudeCodeSettingsSnippet,
  buildClaudeCodeTrialCommand,
  CLAUDE_CODE_ENTRY_MODE_OPTIONS,
  describeClaudeCodeEntryMode,
  describeClaudeCodeEntryState,
  parseClaudeCodeEntryPort
} from './claudeCodeEntryViewModel.js';

const CopyBlock = ({ label, text, onCopied }) => (
  <div className="space-y-1.5">
    <div className="flex items-center justify-between gap-3">
      <span className="text-xs text-fg-body">{label}</span>
      <Button variant="secondary" size="sm" icon={Copy} onClick={async () => onCopied(await copyTextToClipboard(text, label), label)}>复制</Button>
    </div>
    <pre className="overflow-x-auto rounded-md border border-line-soft bg-pane px-3 py-2 font-mono text-xs leading-relaxed text-fg-body">{text}</pre>
  </div>
);

const ClaudeCodeEntryCard = () => {
  const { available, info, busy, error, refresh, save } = useClaudeCodeEntry();
  const { confirm, confirmDialog } = useConfirm();
  // 端口草稿只在用户编辑时存在；未编辑时直接显示已保存端口（含其他页面保存的新值）
  const [portDraft, setPortDraft] = useState(null);
  const [notice, setNotice] = useState('');

  const portInput = portDraft ?? String(info?.listenPort || '');
  const parsedPort = parseClaudeCodeEntryPort(portInput);
  const portDirty = Boolean(info) && portDraft !== null && String(info.listenPort) !== portDraft.trim();
  const state = describeClaudeCodeEntryState(info);
  const controlsDisabled = busy || !info;
  const onCopied = (copied, label) => setNotice(copied ? `已复制${label}` : '');

  const toggleEnabled = async () => {
    if (info.enabled && !(await confirm({
      title: '停用 Claude Code 入口',
      message: '停用后入口不再监听，仍指向它的 Claude Code 会连接失败；Sinuo 不会修改 Claude Code 的配置。确定停用吗？',
      confirmText: '停用',
      danger: true
    }))) {
      return;
    }
    save({ enabled: !info.enabled });
  };

  const savePort = async () => {
    if (await save({ listenPort: parsedPort })) setPortDraft(null);
  };

  return (
    <div>
      {!available ? (
        <p className="mt-5 text-center text-xs text-fg-subtle">仅桌面端可用。</p>
      ) : (
        <>
          {(error || info?.error) && (
            <div role="alert" className="selectable mt-5 flex items-start gap-2 rounded-lg border border-danger-line bg-danger-soft px-3 py-2 text-xs text-danger">
              <AlertCircle size={14} className="mt-0.5 shrink-0" />{error || `入口未能启动：${info.error}`}
            </div>
          )}

          <FormSection
            title="Claude Code 入口"
            description="本机 claude.ai 登录时把 ANTHROPIC_BASE_URL 指向这里"
            actions={(
              <>
                <span className={`${state.tone} rounded border px-1.5 text-[11px] leading-[18px]`}>{state.label}</span>
                <Button variant="secondary" size="sm" icon={RefreshCw} onClick={refresh} disabled={busy}>刷新</Button>
              </>
            )}
          >
            <FormRow label="启用入口" description="专用 loopback 端口，不经访问鉴权；停用后仍指向它的 Claude Code 会连接失败">
              <Switch checked={info?.enabled === true} disabled={controlsDisabled} onChange={toggleEnabled} label="启用 Claude Code 入口" />
            </FormRow>
            <FormRow label="监听端口" description="写进 Claude Code 配置后不会自动更换；被占用时入口启动失败，请换一个端口">
              <span className="font-mono text-xs text-fg-muted">127.0.0.1 :</span>
              <input
                aria-label="Claude Code 入口端口"
                aria-invalid={portDirty && parsedPort === null}
                value={portInput}
                onChange={(event) => setPortDraft(event.target.value)}
                disabled={controlsDisabled}
                inputMode="numeric"
                className="desk-field w-20 text-right font-mono"
              />
              <Button size="sm" variant="secondary" onClick={savePort} disabled={busy || !portDirty || parsedPort === null}>保存</Button>
            </FormRow>
            <FormRow label="接入模式" description={`${describeClaudeCodeEntryMode(info?.mode).hint} 订阅直通与第三方端点二选一、不混用；主端口始终走端点。`}>
              <SegmentedControl
                options={CLAUDE_CODE_ENTRY_MODE_OPTIONS}
                value={info?.mode || 'subscription'}
                disabled={controlsDisabled}
                onChange={(mode) => mode !== (info?.mode || 'subscription') && save({ mode })}
                ariaLabel="Claude Code 接入模式"
              />
            </FormRow>
          </FormSection>
          {portDirty && parsedPort === null && <p className="mx-1 mt-1.5 text-xs text-danger">端口必须是 1-65535 的整数</p>}

          {info?.baseUrl && (
            <FormSection title="客户端配置" description="切换接入模式立即对新请求生效，进行中的请求不受影响">
              <FormBlock>
                <div className="space-y-3">
                  <CopyBlock label="首次试用（另开终端运行，不影响正在用的 Claude Code）" text={buildClaudeCodeTrialCommand(info.baseUrl)} onCopied={onCopied} />
                  <CopyBlock label="确认无误后写入 ~/.claude/settings.json" text={buildClaudeCodeSettingsSnippet(info.baseUrl)} onCopied={onCopied} />
                  <p className="text-[11.5px] leading-relaxed text-fg-muted">
                    不要同时设置 ANTHROPIC_AUTH_TOKEN、ANTHROPIC_API_KEY 或 apiKeyHelper，否则会顶替订阅登录、改按 token 计费。Sinuo 未运行时 Claude Code 无法请求，删掉 ANTHROPIC_BASE_URL 即可恢复直连。
                  </p>
                  {notice && <p className="flex items-center gap-1 text-xs text-success"><CheckCircle2 size={12} />{notice}</p>}
                </div>
              </FormBlock>
            </FormSection>
          )}
        </>
      )}
      {confirmDialog}
    </div>
  );
};

export default ClaudeCodeEntryCard;
