// ============================================
// 主 App 组件 - Dashboard 入口
// 2025-11-28 (Updated 2025-12-12)
// ============================================

import { useState, Suspense, lazy, useCallback, useEffect } from 'react';
import Header from '@components/layout/Header.jsx';
import ToastHost from '@components/notifications/ToastHost.jsx';
import { LoadingSpinner } from '@components/ui';
import useSSE from '@hooks/useSSE.js';
import useGlobalToasts from '@hooks/useGlobalToasts.js';
import { getMigrationStatus } from '@utils/wailsApi.js';
import { ThemeProvider } from '@contexts/ThemeContext.jsx';
import { TimezoneProvider } from '@/contexts/TimezoneContext.jsx';

// 懒加载页面组件
const OverviewPage = lazy(() => import('@pages/overview/index.jsx'));
// Claude 管理页面（端点 + 客户端接入）
const ClaudePage = lazy(() => import('@pages/claude/index.jsx'));
const RequestsPage = lazy(() => import('@pages/requests/index.jsx'));
const PricingPage = lazy(() => import('@pages/pricing/index.jsx'));
// v5.1: 配置页面改为设置页面（可编辑）
const SettingsPage = lazy(() => import('@pages/settings/index.jsx'));
// 保留只读配置页面用于调试
const ConfigPage = lazy(() => import('@pages/config/index.jsx'));
// v5.1: 系统日志页面
const LogsPage = lazy(() => import('@pages/log-viewer/index.jsx'));
// Codex 管理页面（账号池 + 客户端接入）
const CodexPage = lazy(() => import('@pages/codex/index.jsx'));
// v6.1: 隐私保护页面
const PrivacyProtectionPage = lazy(() => import('@pages/privacy-protection/index.jsx'));
const MigrationRecoveryPage = lazy(() => import('@pages/migration-recovery/index.jsx'));

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

  const navigateToCodexAccess = useCallback(() => {
    setActiveCodexTab('client-access');
    setActiveTab('account-pool');
  }, []);

  const navigateToClaudeAccess = useCallback(() => {
    setActiveClaudeTab('client-access');
    setActiveTab('endpoints');
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

  // 渲染当前页面
  const renderPage = () => {
    const pages = {
      // 概览页的模型表需要带筛选条件跳到请求追踪页，因此拿到切页能力
      overview: <OverviewPage onNavigate={setActiveTab} />,
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

  if (migrationStatus.state !== 'ready') {
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
        onTabChange={setActiveTab}
        connectionStatus={connectionStatus}
        proxyStatus={proxyStatus}
      />

      {/* 应用窗口内全局 Toast；不会调用 macOS 通知中心。 */}
      <ToastHost toasts={toasts} pendingCount={pendingCount} onDismiss={dismissToast} />

      {/* 主内容区 */}
      <main id="app-scroll-container" className="flex-1 min-h-0 overflow-y-auto overscroll-none relative z-10">
        <div className="max-w-7xl mx-auto px-6 pt-8 pb-20">
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
