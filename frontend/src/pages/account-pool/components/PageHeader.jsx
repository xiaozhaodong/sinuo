// ============================================
// Account Pool 页面头部
// 2026-03-22
// 2026-10-07：嵌入 Codex 管理页时投送到统一工具栏，按钮改为工具栏按钮
// ============================================

import { Plus, RefreshCw, ShieldCheck, SlidersHorizontal } from 'lucide-react';
import { ToolbarButton, ToolbarSeparator } from '@components/layout/ToolbarButton.jsx';

const PageHeader = ({ embedded = false, loading = false, onCreate, onRefresh, onOpenScheduler }) => (
  <div className={`flex items-center gap-3 ${embedded ? 'justify-end' : 'justify-between'}`}>
    {!embedded && (
      <div className="flex items-center gap-3">
        <div className="p-2 bg-inverted rounded-lg text-fg-inverted shadow-lg">
          <ShieldCheck className="w-5 h-5" />
        </div>
        <div>
          <h1 className="text-2xl font-bold text-fg">账号管理</h1>
          <p className="text-sm text-fg-muted">管理上游账号池，支持 API Key 与 ChatGPT OAuth 两种授权方式</p>
        </div>
      </div>
    )}
    <div className="flex items-center gap-1">
      <ToolbarButton variant="bordered" icon={SlidersHorizontal} label="调度编排" onClick={onOpenScheduler} />
      <ToolbarSeparator />
      <ToolbarButton icon={RefreshCw} title="刷新数据" onClick={onRefresh} loading={loading} />
      <ToolbarButton icon={Plus} title="新增账号" onClick={onCreate} />
    </div>
  </div>
);

export default PageHeader;
