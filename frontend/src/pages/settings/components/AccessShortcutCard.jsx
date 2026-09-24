// ============================================
// 设置页：客户端接入快捷入口（Codex / Claude Code 的接入配置已移到各自管理页）
// 2026-09-24
// ============================================

import { ArrowRight, TerminalSquare } from 'lucide-react';
import { Button } from '@components/ui';

const AccessShortcutCard = ({ title, description, onNavigate }) => (
  <div className="flex flex-col gap-4 rounded-2xl border border-line bg-surface px-5 py-4 shadow-sm sm:flex-row sm:items-center sm:justify-between">
    <div className="flex min-w-0 items-start gap-3">
      <div className="rounded-lg bg-accent-soft p-2 text-accent">
        <TerminalSquare size={18} />
      </div>
      <div className="min-w-0">
        <h2 className="text-sm font-semibold text-fg">{title}</h2>
        <p className="mt-1 text-xs text-fg-muted">{description}</p>
      </div>
    </div>
    <Button
      variant="secondary"
      size="sm"
      icon={ArrowRight}
      onClick={onNavigate}
      disabled={!onNavigate}
      className="shrink-0"
    >
      前往客户端接入
    </Button>
  </div>
);

export default AccessShortcutCard;
