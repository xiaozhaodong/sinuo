// ============================================
// Claude 管理页面：第三方端点 + Claude Code 客户端接入
// 2026-09-24
// ============================================

import { useState } from 'react';
import { KeyRound, Server } from 'lucide-react';
import ClaudeCodeEntryCard from '@components/claude-code-entry/ClaudeCodeEntryCard.jsx';
import PageTabs from '@components/layout/PageTabs.jsx';
import EndpointsPage from '@pages/endpoints/index.jsx';

const TABS = [
  {
    id: 'endpoints',
    label: '端点',
    icon: Server
  },
  {
    id: 'client-access',
    label: '客户端接入',
    icon: KeyRound
  }
];

const ClaudePage = ({ activeTab = 'endpoints', onTabChange, modelRoutingOpenRequest = null, onModelRoutingOpenRequestHandled }) => {
  const [endpointActionsTarget, setEndpointActionsTarget] = useState(null);

  return (
    <div className="space-y-4">
      <div className="flex items-center gap-3">
        <div className="p-2 bg-inverted rounded-lg text-fg-inverted shadow-lg">
          <Server size={20} />
        </div>
        <div>
          <h1 className="text-2xl font-bold text-fg">Claude 管理</h1>
          <p className="mt-1 text-sm text-fg-muted">
            管理第三方端点与模型路由，并配置 Claude Code 的订阅直通接入入口
          </p>
        </div>
      </div>

      <div className="flex flex-wrap items-center justify-between gap-3">
        <PageTabs tabs={TABS} activeTab={activeTab} onChange={onTabChange} idPrefix="claude" ariaLabel="Claude 管理" />
        {/* 端点操作仍由端点页管理，在同一工具栏展示，避免多占一行。 */}
        <div ref={setEndpointActionsTarget} hidden={activeTab !== 'endpoints'} />
      </div>

      <section
        id="claude-panel-endpoints"
        role="tabpanel"
        aria-labelledby="claude-tab-endpoints"
        hidden={activeTab !== 'endpoints'}
      >
        <EndpointsPage embedded active={activeTab === 'endpoints'} headerTarget={endpointActionsTarget} modelRoutingOpenRequest={modelRoutingOpenRequest} onModelRoutingOpenRequestHandled={onModelRoutingOpenRequestHandled} />
      </section>

      <section
        id="claude-panel-client-access"
        role="tabpanel"
        aria-labelledby="claude-tab-client-access"
        hidden={activeTab !== 'client-access'}
      >
        <ClaudeCodeEntryCard />
      </section>
    </div>
  );
};

export default ClaudePage;
