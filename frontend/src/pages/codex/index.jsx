// ============================================
// Codex 管理页面
// 2026-09-11
// ============================================

import { useState } from 'react';
import { KeyRound, Layers3 } from 'lucide-react';
import CodexSubscriptionProxyCard from '@components/codex-access/CodexSubscriptionProxyCard.jsx';
import AccountPoolPage from '@pages/account-pool/index.jsx';

const TABS = [
  {
    id: 'accounts',
    label: '账号池',
    icon: Layers3
  },
  {
    id: 'client-access',
    label: '客户端接入',
    icon: KeyRound
  }
];

const CodexPage = ({ activeTab = 'accounts', onTabChange }) => {
  // 客户端入口首次访问后保持挂载，页签切换不会清掉只展示一次的明文密钥；
  // 离开整个 Codex 管理页时组件仍会卸载，避免扩大明文留存范围。
  const [clientAccessVisited, setClientAccessVisited] = useState(activeTab === 'client-access');
  const [accountActionsTarget, setAccountActionsTarget] = useState(null);

  const handleTabChange = (nextTab) => {
    if (nextTab === 'client-access') {
      setClientAccessVisited(true);
    }
    onTabChange?.(nextTab);
  };

  return (
    <div className="space-y-4 animate-in fade-in slide-in-from-bottom-2 duration-500">
      <div className="flex items-center gap-3">
        <div className="p-2 bg-inverted rounded-lg text-fg-inverted shadow-lg">
          <Layers3 size={20} />
        </div>
        <div>
          <h1 className="text-2xl font-bold text-fg">Codex 管理</h1>
          <p className="mt-1 text-sm text-fg-muted">
            管理上游账号池，并配置 Codex 原生客户端的独立接入入口
          </p>
        </div>
      </div>

      <div className="flex flex-wrap items-center justify-between gap-3">
        <div
          role="tablist"
          aria-label="Codex 管理"
          className="inline-flex shrink-0 items-center gap-1 rounded-lg border border-line bg-surface-sub p-1"
        >
          {TABS.map((tab) => {
            const Icon = tab.icon;
            const selected = activeTab === tab.id;
            return (
              <button
                key={tab.id}
                id={`codex-tab-${tab.id}`}
                type="button"
                role="tab"
                aria-selected={selected}
                aria-controls={`codex-panel-${tab.id}`}
                onClick={() => handleTabChange(tab.id)}
                className={`inline-flex items-center gap-2 rounded-md px-3 py-1.5 text-sm font-medium transition-colors ${
                  selected
                    ? 'bg-surface text-accent shadow-sm'
                    : 'text-fg-muted hover:bg-surface/60 hover:text-fg-body'
                }`}
              >
                <Icon size={15} className="shrink-0" />
                {tab.label}
              </button>
            );
          })}
        </div>
        {/* 操作仍由账号池页面管理，在同一工具栏展示，避免多占一行。 */}
        <div ref={setAccountActionsTarget} hidden={activeTab !== 'accounts'} />
      </div>

      <section
        id="codex-panel-accounts"
        role="tabpanel"
        aria-labelledby="codex-tab-accounts"
        hidden={activeTab !== 'accounts'}
      >
        <AccountPoolPage embedded active={activeTab === 'accounts'} headerTarget={accountActionsTarget} />
      </section>

      <section
        id="codex-panel-client-access"
        role="tabpanel"
        aria-labelledby="codex-tab-client-access"
        hidden={activeTab !== 'client-access'}
      >
        {clientAccessVisited && <CodexSubscriptionProxyCard />}
      </section>
    </div>
  );
};

export default CodexPage;
