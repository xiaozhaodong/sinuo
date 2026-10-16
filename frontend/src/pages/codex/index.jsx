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
import CodexImagesPage from './images/CodexImagesPage.jsx';
import CodexModelsPage from './models/CodexModelsPage.jsx';
import CodexRoutingPage from './routing/CodexRoutingPage.jsx';
import CodexSchedulePage from './schedule/CodexSchedulePage.jsx';
import useCodexPoolData from './useCodexPoolData.js';

const TABS = [
  { id: 'accounts', label: '账号池' },
  { id: 'schedule', label: '调度编排' },
  { id: 'routing', label: '模型路由' },
  { id: 'models', label: '模型目录' },
  { id: 'images', label: '图像生成' },
  { id: 'client-access', label: '客户端接入' }
];

const CodexPage = ({ activeTab = 'accounts', onTabChange }) => {
  const poolData = useCodexPoolData({ enabled: activeTab === 'accounts' || activeTab === 'schedule' });
  const [inventoryViewRequest, setInventoryViewRequest] = useState(null);
  const [scheduleVisited, setScheduleVisited] = useState(activeTab === 'schedule');
  const [scheduleActionsTarget, setScheduleActionsTarget] = useState(null);
  // 客户端入口首次访问后保持挂载，页签切换不会清掉只展示一次的明文密钥；
  // 离开整个 Codex 管理页时组件仍会卸载，避免扩大明文留存范围。
  const [clientAccessVisited, setClientAccessVisited] = useState(activeTab === 'client-access');
  const [accountActionsTarget, setAccountActionsTarget] = useState(null);
  const [routingActionsTarget, setRoutingActionsTarget] = useState(null);
  const [routingVisited, setRoutingVisited] = useState(activeTab === 'routing');
  const [modelsActionsTarget, setModelsActionsTarget] = useState(null);
  const [modelsVisited, setModelsVisited] = useState(activeTab === 'models');
  const [imagesActionsTarget, setImagesActionsTarget] = useState(null);
  const [imagesVisited, setImagesVisited] = useState(activeTab === 'images');
  // 接住托盘和请求页直接切页签，访问后保持挂载。
  if (activeTab === 'routing' && !routingVisited) setRoutingVisited(true);
  if (activeTab === 'schedule' && !scheduleVisited) setScheduleVisited(true);

  const handleTabChange = (nextTab) => {
    if (nextTab === 'schedule') setScheduleVisited(true);
    if (nextTab === 'routing') setRoutingVisited(true);
    if (nextTab === 'models') setModelsVisited(true);
    if (nextTab === 'images') setImagesVisited(true);
    if (nextTab === 'client-access') {
      setClientAccessVisited(true);
    }
    onTabChange?.(nextTab);
  };

  const handleViewInInventory = (groupKey) => {
    setInventoryViewRequest({ type: 'focus-group', groupKey, requestId: Date.now() });
    handleTabChange('accounts');
  };

  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <PageToolbar>
        <PageTabs tabs={TABS} activeTab={activeTab} onChange={handleTabChange} idPrefix="codex" ariaLabel="Codex 管理" />
        {/* 操作仍由账号池页面管理，投送到工具栏右侧。 */}
        <div ref={setAccountActionsTarget} className="flex min-w-0 flex-1 items-center justify-end" hidden={activeTab !== 'accounts'} />
        <div ref={setScheduleActionsTarget} className="flex min-w-0 flex-1 items-center justify-end" hidden={activeTab !== 'schedule'} />
        <div ref={setRoutingActionsTarget} className="flex min-w-0 flex-1 items-center justify-end" hidden={activeTab !== 'routing'} />
        <div ref={setModelsActionsTarget} className="flex min-w-0 flex-1 items-center justify-end" hidden={activeTab !== 'models'} />
        <div ref={setImagesActionsTarget} className="flex min-w-0 flex-1 items-center justify-end" hidden={activeTab !== 'images'} />
      </PageToolbar>

      <section
        id="codex-panel-accounts"
        role="tabpanel"
        aria-labelledby="codex-tab-accounts"
        hidden={activeTab !== 'accounts'}
        className="flex min-h-0 flex-1"
      >
        <AccountPoolPage embedded active={activeTab === 'accounts'} headerTarget={accountActionsTarget} poolData={poolData} inventoryViewRequest={inventoryViewRequest} />
      </section>

      <section id="codex-panel-schedule" role="tabpanel" aria-labelledby="codex-tab-schedule" hidden={activeTab !== 'schedule'} className="flex min-h-0 flex-1">
        {scheduleVisited && <CodexSchedulePage active={activeTab === 'schedule'} headerTarget={scheduleActionsTarget} poolData={poolData} onViewInInventory={handleViewInInventory} />}
      </section>

      <section id="codex-panel-routing" role="tabpanel" aria-labelledby="codex-tab-routing" hidden={activeTab !== 'routing'} className="flex min-h-0 flex-1">
        {routingVisited && <CodexRoutingPage active={activeTab === 'routing'} headerTarget={routingActionsTarget} />}
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
        id="codex-panel-images"
        role="tabpanel"
        aria-labelledby="codex-tab-images"
        hidden={activeTab !== 'images'}
        className="min-h-0 flex-1 overflow-y-auto bg-pane px-7 pb-12"
      >
        <div className="mx-auto max-w-[640px]">
          {imagesVisited && <CodexImagesPage active={activeTab === 'images'} headerTarget={imagesActionsTarget} />}
        </div>
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
