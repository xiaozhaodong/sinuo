// ============================================
// Claude 管理页面：第三方端点 + Claude Code 客户端接入
// 2026-09-24
// 2026-10-07：页签与端点操作放进统一工具栏
// ============================================

import { useState } from 'react';
import ClaudeCodeEntryCard from '@components/claude-code-entry/ClaudeCodeEntryCard.jsx';
import { PageToolbar } from '@components/layout/AppToolbar.jsx';
import PageTabs from '@components/layout/PageTabs.jsx';
import EndpointsPage from '@pages/endpoints/index.jsx';
import ClaudeModelsPage from './models/ClaudeModelsPage.jsx';

const TABS = [
  { id: 'endpoints', label: '端点' },
  { id: 'models', label: '模型目录' },
  { id: 'client-access', label: '客户端接入' }
];

const ClaudePage = ({ activeTab = 'endpoints', onTabChange, modelRoutingOpenRequest = null, onModelRoutingOpenRequestHandled }) => {
  const [endpointActionsTarget, setEndpointActionsTarget] = useState(null);
  const [modelsActionsTarget, setModelsActionsTarget] = useState(null);
  const [modelsVisited, setModelsVisited] = useState(activeTab === 'models');

  const handleTabChange = (nextTab) => {
    if (nextTab === 'models') setModelsVisited(true);
    onTabChange?.(nextTab);
  };

  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <PageToolbar>
        <PageTabs tabs={TABS} activeTab={activeTab} onChange={handleTabChange} idPrefix="claude" ariaLabel="Claude 管理" />
        {/* 端点操作仍由端点页管理，投送到工具栏右侧。 */}
        <div ref={setEndpointActionsTarget} className="flex min-w-0 flex-1 items-center justify-end" hidden={activeTab !== 'endpoints'} />
        <div ref={setModelsActionsTarget} className="flex min-w-0 flex-1 items-center justify-end" hidden={activeTab !== 'models'} />
      </PageToolbar>

      <section
        id="claude-panel-endpoints"
        role="tabpanel"
        aria-labelledby="claude-tab-endpoints"
        hidden={activeTab !== 'endpoints'}
        className="flex min-h-0 flex-1"
      >
        <EndpointsPage embedded active={activeTab === 'endpoints'} headerTarget={endpointActionsTarget} modelRoutingOpenRequest={modelRoutingOpenRequest} onModelRoutingOpenRequestHandled={onModelRoutingOpenRequestHandled} />
      </section>

      <section
        id="claude-panel-models"
        role="tabpanel"
        aria-labelledby="claude-tab-models"
        hidden={activeTab !== 'models'}
        className="flex min-h-0 flex-1"
      >
        {modelsVisited && <ClaudeModelsPage active={activeTab === 'models'} headerTarget={modelsActionsTarget} />}
      </section>

      <section
        id="claude-panel-client-access"
        role="tabpanel"
        aria-labelledby="claude-tab-client-access"
        hidden={activeTab !== 'client-access'}
        className="min-h-0 flex-1 overflow-y-auto bg-pane px-7 pb-12"
      >
        <div className="mx-auto max-w-[640px]">
          <ClaudeCodeEntryCard />
        </div>
      </section>
    </div>
  );
};

export default ClaudePage;
