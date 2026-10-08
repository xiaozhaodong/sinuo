// 隐私读取失败只降级本区块，不影响父组件的启停与保存。
import { RefreshCw } from 'lucide-react';
import { Button } from '@components/ui';
import { FormBlock, FormSection } from '@components/desktop/FormSection.jsx';

const CodexPrivacySection = ({ privacyError, privacyInfo, privacyWarning, busy, onRetry }) => (
  <FormSection title="隐私保护">
    <FormBlock>
      <div className="text-[13px] text-fg-body">
        当前隐私模式：<span className="font-medium">{privacyError ? '未读到' : privacyInfo.label}</span>
      </div>
      {privacyError ? (
        <div className="mt-1 flex items-center justify-between gap-3">
          <span className="text-xs text-warn">隐私设置读取失败：{privacyError}</span>
          <Button variant="secondary" size="sm" icon={RefreshCw} onClick={onRetry} disabled={busy}>
            重试
          </Button>
        </div>
      ) : (
        <p className="mt-0.5 text-xs text-fg-muted">{privacyInfo.detail}</p>
      )}
      <p className="mt-1 text-xs text-fg-muted">
        隐私关闭、仅检测或没有适用规则都不影响启用；实际处理结果按请求详情与隐私保护页为准。
      </p>
      {privacyWarning && <p className="mt-1 text-xs text-warn">{privacyWarning}</p>}
    </FormBlock>
  </FormSection>
);

export default CodexPrivacySection;
