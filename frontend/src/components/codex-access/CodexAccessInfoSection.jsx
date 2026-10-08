// 接入信息展示：明文与复制状态仍由父组件的内存状态承载。
import { AlertCircle, Copy } from 'lucide-react';
import { Button } from '@components/ui';
import { FormBlock, FormSection } from '@components/desktop/FormSection.jsx';

const CodexAccessInfoSection = ({ accessInfo, config, configSnippet, copiedKey, busy, onRotate, onCopy, onHide }) => (
  <FormSection
    title="接入信息"
    actions={(
      <Button variant="secondary" size="sm" onClick={onRotate} disabled={busy || !config?.initialized}>
        轮换密钥
      </Button>
    )}
  >
    <FormBlock>
      {accessInfo ? (
        <div className="space-y-3">
          <div className="flex items-start gap-2 rounded-lg border border-warn-line bg-warn-soft px-3 py-2 text-xs text-warn">
            <AlertCircle size={14} className="mt-0.5 shrink-0" />
            <span>访问密钥明文只在初始化与轮换时展示这一次，离开页面后无法再取回；丢失只能轮换。</span>
          </div>
          <div className="space-y-1.5">
            <div className="flex items-center justify-between gap-3">
              <span className="text-xs text-fg-body">Codex 接入地址</span>
              <Button variant="secondary" size="sm" icon={Copy} onClick={() => onCopy('url', accessInfo.url)}>
                {copiedKey === 'url' ? '已复制' : '复制'}
              </Button>
            </div>
            <code className="block break-all rounded-md border border-line-soft bg-pane px-3 py-2 font-mono text-xs leading-relaxed text-fg-body">
              {accessInfo.url}
            </code>
          </div>
          <div className="space-y-1.5">
            <div className="flex items-center justify-between gap-3">
              <span className="text-xs text-fg-body">Codex 配置片段（不会自动写入你的配置）</span>
              <Button variant="secondary" size="sm" icon={Copy} onClick={() => onCopy('snippet', configSnippet)}>
                {copiedKey === 'snippet' ? '已复制' : '复制'}
              </Button>
            </div>
            <pre className="overflow-x-auto rounded-md border border-line-soft bg-pane px-3 py-2 font-mono text-xs leading-relaxed text-fg-body">
              {configSnippet}
            </pre>
            <p className="text-xs text-fg-muted">
              写入用户级 ~/.codex/config.toml；项目级同名配置不能覆盖。请保留 /backend-api/codex 后缀，
              改完完整退出并重启 Codex 后再使用。日常切换模式可在本页或请求追踪页完成。
            </p>
          </div>
          <button type="button" onClick={onHide} className="text-xs text-fg-muted underline hover:text-fg">
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
    </FormBlock>
  </FormSection>
);

export default CodexAccessInfoSection;
