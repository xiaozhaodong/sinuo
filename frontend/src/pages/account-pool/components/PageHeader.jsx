// ============================================
// Account Pool 页面头部
// 2026-03-22
// ============================================

import { Database, GitBranch, Plus, RefreshCw, ShieldCheck } from 'lucide-react';
import { Button } from '@components/ui';

const PageHeader = ({ loading = false, onCreate, onRefresh, onOpenScheduler, onOpenCodexModels, onOpenCodexModelRouting }) => (
  <div className="flex flex-col md:flex-row md:items-end justify-between gap-4">
    <div className="flex items-center gap-3">
      <div className="p-2 bg-inverted rounded-lg text-fg-inverted shadow-lg">
        <ShieldCheck className="w-5 h-5" />
      </div>
      <div>
        <h1 className="text-2xl font-bold text-fg">账号管理</h1>
        <p className="text-sm text-fg-muted">管理上游账号池，支持 API Key 与 ChatGPT OAuth 两种授权方式</p>
      </div>
    </div>
    <div className="flex flex-wrap items-center gap-2">
      <Button icon={Plus} onClick={onCreate}>
        新增账号
      </Button>
      <Button icon={RefreshCw} variant="secondary" onClick={onRefresh} loading={loading}>
        刷新数据
      </Button>
      <Button icon={Database} variant="secondary" onClick={onOpenCodexModels}>
        Codex 模型
      </Button>
      <Button icon={GitBranch} variant="secondary" onClick={onOpenCodexModelRouting}>
        模型路由
      </Button>
      <Button icon={GitBranch} variant="secondary" onClick={onOpenScheduler}
        className="tone-indigo hover:bg-accent-soft hover:border-accent-line"
      >
        调度编排
      </Button>
    </div>
  </div>
);

export default PageHeader;
