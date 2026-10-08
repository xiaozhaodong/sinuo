// ============================================
// 主 App 组件 - Dashboard 入口
// 2025-11-28 (Updated 2025-12-12)
// 2026-10-07：桌面化外壳——左侧边栏 + 统一工具栏，内容区铺满窗口
// ============================================

import { useState, Suspense, lazy, useCallback, useEffect, startTransition } from 'react';
import Sidebar from '@components/layout/Sidebar.jsx';
import { AppToolbar, ToolbarProvider } from '@components/layout/AppToolbar.jsx';
import { pageTitleOf } from '@components/layout/navigation.js';
import StartupScreen from '@components/layout/StartupScreen.jsx';
import ToastHost from '@components/notifications/ToastHost.jsx';
import { LoadingSpinner } from '@components/ui';
import useSSE from '@hooks/useSSE.js';
import useGlobalToasts from '@hooks/useGlobalToasts.js';
import useNavigationShortcuts from '@hooks/useNavigationShortcuts.js';
import useWindowFocus from '@hooks/useWindowFocus.js';
import { getMigrationStatus, getSystemStatus, isWailsEnvironment } from '@utils/wailsApi.js';
import { connectTrayNavigation } from '@utils/trayNavigation.js';
import { setPendingRequestsFilters } from '@utils/overviewHandoff.js';
import { ThemeProvider } from '@contexts/ThemeContext.jsx';
import { TimezoneProvider } from '@/contexts/TimezoneContext.jsx';
// 概览是启动首屏，直接打进入口：懒加载只会在启动时多出一段 Suspense 占位（React 19 至少停留 300ms）
import OverviewPage from '@pages/overview/index.jsx';

// 其余页面懒加载。加载函数单独列出，启动完成后统一预取（见 preloadPages）
const PAGE_MODULES = {
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

// 启动状态轮询：正常启动后端只需百余毫秒，勤查以便就绪后立即进主界面；真正迁移与失败重试放缓
const STARTUP_POLL_INTERVAL_MS = 100;
const MIGRATING_POLL_INTERVAL_MS = 400;
const FAILED_POLL_INTERVAL_MS = 1000;

const pollIntervalFor = (state) => {
  if (state === 'initializing') return STARTUP_POLL_INTERVAL_MS;
  if (state === 'migration_failed') return FAILED_POLL_INTERVAL_MS;
  return MIGRATING_POLL_INTERVAL_MS;
};

// initializing 只表示后端在做常规初始化，不是迁移；其余非 ready 状态都属于迁移 / 恢复流程
const isMigrationFlowState = (state) => state !== 'initializing' && state !== 'ready';

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
  // 请求页只在挂载时消费一次筛选交接；托盘带条件跳转时若已停在请求页，换 key 让它重新挂载
  const [requestsPageKey, setRequestsPageKey] = useState(0);
  const [migrationStatus, setMigrationStatus] = useState({ state: 'initializing' });
  // 一旦进入迁移 / 恢复流程就停在迁移页直到就绪：重试与迁移收尾阶段后端也会短暂报 initializing，
  // 不据此切回启动画面，避免两页来回切换
  const [migrationFlowSeen, setMigrationFlowSeen] = useState(false);
  const applyMigrationStatus = useCallback((status) => {
    setMigrationStatus(status);
    if (isMigrationFlowState(status.state)) setMigrationFlowSeen(true);
  }, []);
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

  useNavigationShortcuts(switchTab);
  const windowFocused = useWindowFocus();

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

  const navigateToClaudeModelRouting = useCallback(() => {
    startTransition(() => {
      setActiveClaudeTab('routing');
      setActiveTab('endpoints');
    });
  }, []);

  const navigateToCodexModelRouting = useCallback(() => {
    startTransition(() => {
      setActiveCodexTab('routing');
      setActiveTab('account-pool');
    });
  }, []);

  useEffect(() => {
    if (!isWailsEnvironment()) return undefined;
    return connectTrayNavigation(() => import('@wailsio/runtime'), (route) => {
      startTransition(() => {
        setActiveTab(route.tab);
        if (route.claudeTab) setActiveClaudeTab(route.claudeTab);
        if (route.codexTab) setActiveCodexTab(route.codexTab);
        if (route.requestsFilters) {
          setPendingRequestsFilters(route.requestsFilters);
          setRequestsPageKey((key) => key + 1);
        }
      });
    }, (error) => console.error('托盘导航初始化失败:', error));
  }, []);

  useEffect(() => {
    let cancelled = false;
    let timer = null;

    const pollMigrationStatus = async () => {
      try {
        const status = await getMigrationStatus();
        if (cancelled) return;
        applyMigrationStatus(status);
        if (status.state !== 'ready') {
          timer = window.setTimeout(pollMigrationStatus, pollIntervalFor(status.state));
        }
      } catch (error) {
        if (cancelled) return;
        applyMigrationStatus({ state: 'migration_failed', error: error?.message || '迁移状态读取失败', retryAllowed: true });
        timer = window.setTimeout(pollMigrationStatus, FAILED_POLL_INTERVAL_MS);
      }
    };

    pollMigrationStatus();
    return () => {
      cancelled = true;
      if (timer !== null) window.clearTimeout(timer);
    };
  }, [applyMigrationStatus]);

  const isReady = migrationStatus.state === 'ready';
  useEffect(() => {
    if (!isReady) return undefined;
    const timer = window.setTimeout(preloadPages, PAGE_PRELOAD_DELAY_MS);
    return () => window.clearTimeout(timer);
  }, [isReady]);

  // 就绪时主动拉一次代理状态：DOM 就绪时那次推送可能早于订阅、或被启动锁压到启动末尾，
  // 定时推送又是 2 秒一次，不拉的话页头可能先显示「代理未运行」再变绿
  useEffect(() => {
    if (!isReady) return undefined;
    let cancelled = false;
    getSystemStatus()
      .then((status) => { if (!cancelled) handleStatusUpdate(status); })
      .catch(() => {}); // 拉取失败时仍有定时推送兜底
    return () => { cancelled = true; };
  }, [isReady, handleStatusUpdate]);

  // 渲染当前页面
  const renderPage = () => {
    const pages = {
      // 概览页的模型表需要带筛选条件跳到请求追踪页，因此拿到切页能力
      overview: <OverviewPage onNavigate={switchTab} />,
      endpoints: <ClaudePage activeTab={activeClaudeTab} onTabChange={setActiveClaudeTab} />,
      requests: <RequestsPage key={requestsPageKey} onOpenClaudeModelRouting={navigateToClaudeModelRouting} onOpenCodexModelRouting={navigateToCodexModelRouting} onNavigateToCodexAccess={navigateToCodexAccess} />,
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

  if (!isReady && !migrationFlowSeen) {
    return <StartupScreen />;
  }

  if (!isReady) {
    return <MigrationRecoveryPage status={migrationStatus} onStatusChange={applyMigrationStatus} />;
  }

  return (
    <TimezoneProvider>
      <ToolbarProvider>
        <div className={`motion-app flex h-screen overflow-hidden bg-canvas font-sans text-fg ${windowFocused ? '' : 'window-inactive'}`}>
          <Sidebar
            activeTab={activeTab}
            onTabChange={switchTab}
            connectionStatus={connectionStatus}
            proxyStatus={proxyStatus}
          />

          <div className="flex min-w-0 flex-1 flex-col">
            <AppToolbar title={pageTitleOf(activeTab)} />

            {/* 应用窗口内全局 Toast；不会调用 macOS 通知中心。 */}
            <ToastHost toasts={toasts} pendingCount={pendingCount} onDismiss={dismissToast} />

            {/* 主内容区：铺满窗口，不加统一内边距——列表页贴边（表格 + 检查器），
                滚动型页面自己加 p-5。内层固定为窗口剩余高度（h-full 而不是 min-h-full）：
                列表页（flex-1 + min-h-0）恰好占满、在内部滚动，内容再高也不会把整页撑长、把状态栏挤出窗口；
                滚动型页面的内容超出时照常由 main 滚动 */}
            <main id="app-scroll-container" className="relative min-h-0 flex-1 overflow-y-auto overscroll-none">
              <div className="flex h-full flex-col">
                <Suspense fallback={<LoadingSpinner text="加载页面..." />}>
                  {renderPage()}
                </Suspense>
              </div>
            </main>
          </div>
        </div>
      </ToolbarProvider>
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
