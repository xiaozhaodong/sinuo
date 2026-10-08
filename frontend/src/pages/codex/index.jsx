// ============================================
// Codex 管理页面
// 2026-09-11
// 2026-10-07：页签与账号池操作放进统一工具栏
// ============================================

import { useState } from 'react';
import CodexSubscriptionProxyCard from '@components/codex-access/CodexSubscriptionProxyCard.jsx';
import { PageToolbar } from '@components/layout/AppToolbar.jsx';
import PageTabs from '@components/layout/PageTabs.jsx';
import AccountPoolPage from '@pages/account-pool/index.jsx';
import CodexModelsPage from './models/CodexModelsPage.jsx';

const TABS = [
  { id: 'accounts', label: '账号池' },
  { id: 'models', label: '模型目录' },
  { id: 'client-access', label: '客户端接入' }
];

const CodexPage = ({ activeTab = 'accounts', onTabChange, modelRoutingOpenRequest = null, onModelRoutingOpenRequestHandled }) => {
  // 客户端入口首次访问后保持挂载，页签切换不会清掉只展示一次的明文密钥；
  // 离开整个 Codex 管理页时组件仍会卸载，避免扩大明文留存范围。
  const [clientAccessVisited, setClientAccessVisited] = useState(activeTab === 'client-access');
  const [accountActionsTarget, setAccountActionsTarget] = useState(null);
  const [modelsActionsTarget, setModelsActionsTarget] = useState(null);
  const [modelsVisited, setModelsVisited] = useState(activeTab === 'models');

  const handleTabChange = (nextTab) => {
    if (nextTab === 'models') setModelsVisited(true);
    if (nextTab === 'client-access') {
      setClientAccessVisited(true);
    }
    onTabChange?.(nextTab);
  };

  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <PageToolbar>
        <PageTabs tabs={TABS} activeTab={activeTab} onChange={handleTabChange} idPrefix="codex" ariaLabel="Codex 管理" />
        {/* 操作仍由账号池页面管理，投送到工具栏右侧。 */}
        <div ref={setAccountActionsTarget} className="flex min-w-0 flex-1 items-center justify-end" hidden={activeTab !== 'accounts'} />
        <div ref={setModelsActionsTarget} className="flex min-w-0 flex-1 items-center justify-end" hidden={activeTab !== 'models'} />
      </PageToolbar>

      <section
        id="codex-panel-accounts"
        role="tabpanel"
        aria-labelledby="codex-tab-accounts"
        hidden={activeTab !== 'accounts'}
        className="flex min-h-0 flex-1"
      >
        <AccountPoolPage embedded active={activeTab === 'accounts'} headerTarget={accountActionsTarget} modelRoutingOpenRequest={modelRoutingOpenRequest} onModelRoutingOpenRequestHandled={onModelRoutingOpenRequestHandled} />
      </section>

      <section
        id="codex-panel-models"
        role="tabpanel"
        aria-labelledby="codex-tab-models"
        hidden={activeTab !== 'models'}
        className="flex min-h-0 flex-1"
      >
        {modelsVisited && <CodexModelsPage active={activeTab === 'models'} headerTarget={modelsActionsTarget} />}
      </section>

      <section
        id="codex-panel-client-access"
        role="tabpanel"
        aria-labelledby="codex-tab-client-access"
        hidden={activeTab !== 'client-access'}
        className="min-h-0 flex-1 overflow-y-auto bg-pane px-7 pb-12"
      >
        <div className="mx-auto max-w-[640px]">
          {clientAccessVisited && <CodexSubscriptionProxyCard />}
        </div>
      </section>
    </div>
  );
};

export default CodexPage;
