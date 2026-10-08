// ============================================
// 设置页：客户端接入快捷入口（Codex / Claude Code 的接入配置已移到各自管理页）
// 2026-09-24
// 2026-10-07：桌面化——「客户端接入」分组里的一行，右侧是跳转按钮
// ============================================

import { ArrowRight } from 'lucide-react';
import { Button } from '@components/ui';
import { FormRow } from '@components/desktop/FormSection.jsx';

const AccessShortcutCard = ({ title, description, onNavigate }) => (
  <FormRow label={title} description={description}>
    <Button variant="secondary" size="sm" icon={ArrowRight} onClick={onNavigate} disabled={!onNavigate}>
      前往
    </Button>
  </FormRow>
);

export default AccessShortcutCard;
