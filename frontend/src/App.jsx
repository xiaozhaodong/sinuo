// ============================================
// 主 App 组件 - Dashboard 入口
// 2025-11-28 (Updated 2025-12-12)
// ============================================

import { useState, Suspense, lazy, useCallback, useEffect, startTransition } from 'react';
import Header from '@components/layout/Header.jsx';
import ToastHost from '@components/notifications/ToastHost.jsx';
import { LoadingSpinner } from '@components/ui';
import useSSE from '@hooks/useSSE.js';
import useGlobalToasts from '@hooks/useGlobalToasts.js';
import { getMigrationStatus } from '@utils/wailsApi.js';
import { ThemeProvider } from '@contexts/ThemeContext.jsx';
import { TimezoneProvider } from '@/contexts/TimezoneContext.jsx';

// 懒加载页面组件。加载函数单独列出，启动完成后统一预取（见 preloadPages）
const PAGE_MODULES = {
  overview: () => import('@pages/overview/index.jsx'),
  // Claude 管理页面（端点 + 客户端接入）
  claude: () => import('@pages/claude/index.jsx'),
  requests: () => import('@pages/requests/index.jsx'),
  pricing: () => import('@pages/pricing/index.jsx'),
  // v5.1: 配置页面改为设置页面（可编辑）
  settings: () => import('@pages/settings/index.jsx'),
  // 保留只读配置页面用于调试
  config: () => import('@pages/config/index.jsx'),
  // v5.1: 系统日志页面
  logs: () => import('@pages/log-viewer/index.jsx'),
  // Codex 管理页面（账号池 + 客户端接入）
  codex: () => import('@pages/codex/index.jsx'),
  // v6.1: 隐私保护页面
  privacy: () => import('@pages/privacy-protection/index.jsx')
};

const OverviewPage = lazy(PAGE_MODULES.overview);
const ClaudePage = lazy(PAGE_MODULES.claude);
const RequestsPage = lazy(PAGE_MODULES.requests);
const PricingPage = lazy(PAGE_MODULES.pricing);
const SettingsPage = lazy(PAGE_MODULES.settings);
const ConfigPage = lazy(PAGE_MODULES.config);
const LogsPage = lazy(PAGE_MODULES.logs);
const CodexPage = lazy(PAGE_MODULES.codex);
const PrivacyProtectionPage = lazy(PAGE_MODULES.privacy);
const MigrationRecoveryPage = lazy(() => import('@pages/migration-recovery/index.jsx'));

// 启动完成后稍等再预取，不与首屏数据加载抢主线程
const PAGE_PRELOAD_DELAY_MS = 1500;

// 预取只为提速：chunk 的拉取与解析提前做掉，首次切到某页时 transition 几乎立即完成。
// 失败时静默，由真正切页时的 lazy 加载暴露错误。
const preloadPages = () => {
  Object.values(PAGE_MODULES).forEach((load) => {
    load().catch(() => {});
  });
};

// ============================================
// App 组件
// ============================================

const AppRoutes = () => {
  const [activeTab, setActiveTab] = useState('overview');
  const [activeCodexTab, setActiveCodexTab] = useState('accounts');
  const [activeClaudeTab, setActiveClaudeTab] = useState('endpoints');
  const [migrationStatus, setMigrationStatus] = useState({ state: 'initializing' });
  const { toasts, pendingCount, showToast, dismissToast } = useGlobalToasts();
  // v5.1+: 代理网关真实状态（来自后端 system:status 事件）
  const [proxyStatus, setProxyStatus] = useState({
    running: false,
    port: 0,
    host: '127.0.0.1'
  });

  // 处理 system:status 事件
  const handleStatusUpdate = useCallback((data) => {
    const payload = data?.data && typeof data.data === 'object' ? data.data : data;
    if (payload?.proxy_running !== undefined) {
      setProxyStatus({
        running: payload.proxy_running,
        port: payload.proxy_port || 0,
        host: payload.proxy_host || '127.0.0.1'
      });
    }
  }, []);

  const handleRealtimeEvent = useCallback((data, eventType) => {
    if (eventType === 'notification' || data?.event === 'notification') {
      showToast(data);
      return;
    }
    // 兼容命名 SSE 事件与默认 message 事件；非状态数据会被 handleStatusUpdate 忽略。
    handleStatusUpdate(data);
  }, [handleStatusUpdate, showToast]);

  // SSE 连接状态（用于全局状态指示）
  const { connectionStatus } = useSSE(handleRealtimeEvent, { events: 'status,notification' });

  // 切页一律走 transition：新页面代码未就绪时保留当前页，而不是露出 Suspense 兜底。
  // 兜底一旦露出，React 19 会让它至少停留 300ms 才换成内容，切页会显得拖沓。
  const switchTab = useCallback((tab) => {
    startTransition(() => setActiveTab(tab));
  }, []);

  const navigateToCodexAccess = useCallback(() => {
    startTransition(() => {
      setActiveCodexTab('client-access');
      setActiveTab('account-pool');
    });
  }, []);

  const navigateToClaudeAccess = useCallback(() => {
    startTransition(() => {
      setActiveClaudeTab('client-access');
      setActiveTab('endpoints');
    });
  }, []);

  useEffect(() => {
    let cancelled = false;
    let timer = null;

    const pollMigrationStatus = async () => {
      try {
        const status = await getMigrationStatus();
        if (cancelled) return;
        setMigrationStatus(status);
        if (status.state !== 'ready') {
          timer = window.setTimeout(pollMigrationStatus, status.state === 'migration_failed' ? 1000 : 400);
        }
      } catch (error) {
        if (cancelled) return;
        setMigrationStatus({ state: 'migration_failed', error: error?.message || '迁移状态读取失败', retryAllowed: true });
        timer = window.setTimeout(pollMigrationStatus, 1000);
      }
    };

    pollMigrationStatus();
    return () => {
      cancelled = true;
      if (timer !== null) window.clearTimeout(timer);
    };
  }, []);

  const isReady = migrationStatus.state === 'ready';
  useEffect(() => {
    if (!isReady) return undefined;
    const timer = window.setTimeout(preloadPages, PAGE_PRELOAD_DELAY_MS);
    return () => window.clearTimeout(timer);
  }, [isReady]);

  // 渲染当前页面
  const renderPage = () => {
    const pages = {
      // 概览页的模型表需要带筛选条件跳到请求追踪页，因此拿到切页能力
      overview: <OverviewPage onNavigate={switchTab} />,
      endpoints: <ClaudePage activeTab={activeClaudeTab} onTabChange={setActiveClaudeTab} />,
      requests: <RequestsPage />,
      // v6.1: 隐私保护页面
      'privacy-protection': <PrivacyProtectionPage />,
      pricing: <PricingPage />,
      'account-pool': <CodexPage activeTab={activeCodexTab} onTabChange={setActiveCodexTab} />,
      // v5.1: 设置页面（可编辑）
      settings: <SettingsPage onNavigateToCodexAccess={navigateToCodexAccess} onNavigateToClaudeAccess={navigateToClaudeAccess} />,
      // 保留只读配置页面
      config: <ConfigPage />,
      // v5.1: 系统日志页面
      logs: <LogsPage />
    };

    return pages[activeTab] || <OverviewPage />;
  };

  if (!isReady) {
    return <MigrationRecoveryPage status={migrationStatus} onStatusChange={setMigrationStatus} />;
  }

  return (
    <TimezoneProvider>
      <div className="h-screen overflow-hidden bg-canvas font-sans text-fg flex flex-col">
      {/* 背景纹理 */}
      <div
        className="fixed inset-0 pointer-events-none opacity-[0.4]"
        style={{
          backgroundImage: 'radial-gradient(var(--app-texture-dot) 1px, transparent 1px)',
          backgroundSize: '24px 24px'
        }}
      />

      {/* 顶部导航 */}
      <Header
        activeTab={activeTab}
        onTabChange={switchTab}
        connectionStatus={connectionStatus}
        proxyStatus={proxyStatus}
      />

      {/* 应用窗口内全局 Toast；不会调用 macOS 通知中心。 */}
      <ToastHost toasts={toasts} pendingCount={pendingCount} onDismiss={dismissToast} />

      {/* 主内容区 */}
      <main id="app-scroll-container" className="flex-1 min-h-0 overflow-y-auto overscroll-none relative z-10">
        <div className="max-w-7xl mx-auto px-6 pt-8 pb-8">
          <Suspense fallback={<LoadingSpinner text="加载页面..." />}>
            {renderPage()}
          </Suspense>
        </div>
      </main>
      </div>
    </TimezoneProvider>
  );
};

// ThemeProvider 必须在 AppRoutes 的 migration early return 之外：
// 迁移恢复页与 TimezoneProvider 的 loading / error 分支都在那之前返回，
// 包在内层它们就拿不到主题。
function App() {
  return (
    <ThemeProvider>
      <AppRoutes />
    </ThemeProvider>
  );
}

export default App;
