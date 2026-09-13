// ============================================
// 布局组件 - Header 导航栏
// 2025-11-28 (Updated 2025-12-12 for proxy status display)
// ============================================

import { Command, Moon, Sun } from 'lucide-react';
import { isWailsEnvironment } from '@utils/wailsApi.js';
import { useTheme } from '@contexts/ThemeContext.jsx';

// 导航标签项
const NavItem = ({ label, active, onClick }) => (
  <button
    onClick={onClick}
    className={`shrink-0 whitespace-nowrap px-3 xl:px-4 py-2 text-sm font-medium rounded-full transition-all duration-200 ${
      active
        ? 'bg-inverted text-fg-inverted shadow-md'
        : 'text-fg-muted hover:text-fg hover:bg-surface-mut'
    }`}
  >
    {label}
  </button>
);

const Header = ({ activeTab, onTabChange, connectionStatus = 'connected', proxyStatus = {} }) => {
  const { resolved, toggle } = useTheme();
  const tabs = [
    { name: 'overview', label: '概览' },
    { name: 'endpoints', label: 'Claude 端点' },
    { name: 'account-pool', label: 'Codex 管理' },
    { name: 'requests', label: '请求追踪' },
    // v6.1: 隐私保护
    { name: 'privacy-protection', label: '隐私保护' },
    { name: 'pricing', label: '基础定价' },
    // v5.1: 新增日志页面
    { name: 'logs', label: '系统日志' },
    // v5.1: 配置改为设置
    { name: 'settings', label: '设置' }
  ];

  // v5.1+: 使用真实的代理网关状态
  const { running: proxyRunning = false, port: proxyPort = 0 } = proxyStatus;

  // 确定显示状态：优先使用代理网关真实状态，回退到连接状态
  const getDisplayStatus = () => {
    // 如果 Wails 事件通道未连接
    if (connectionStatus !== 'connected') {
      return {
        color: 'bg-warn-solid',
        text: connectionStatus === 'connecting' ? '连接中...' : '事件通道断开',
        ping: connectionStatus === 'connecting'
      };
    }
    // Wails 事件通道已连接，检查代理网关状态
    if (proxyRunning && proxyPort > 0) {
      return {
        color: 'bg-success-solid',
        text: `代理端口 :${proxyPort}`,
        ping: true
      };
    }
    // 代理网关未运行或端口未知
    return {
      color: 'bg-danger-solid',
      text: '代理未运行',
      ping: false
    };
  };

  const status = getDisplayStatus();

  // Wails 环境下为窗口按钮预留顶部空间
  const isWails = isWailsEnvironment();
  const titlebarPadding = isWails ? 'pt-7' : '';

  return (
    <nav className={`sticky top-0 z-50 bg-surface/80 backdrop-blur-xl border-b border-line/60 supports-[backdrop-filter]:bg-surface/60 ${titlebarPadding}`}
         style={isWails ? { WebkitAppRegion: 'drag' } : {}}>
      <div className="max-w-7xl mx-auto px-4 xl:px-6 h-16 flex items-center gap-4 xl:gap-6"
           style={isWails ? { WebkitAppRegion: 'no-drag' } : {}}>
        {/* 左侧：Logo（flex-1 与右侧等宽撑开，保证中间导航居中） */}
        <div className="flex flex-1 items-center justify-start">
          <div
            className="flex items-center space-x-2.5 group cursor-pointer"
            onClick={() => onTabChange('overview')}
          >
            <div className="w-8 h-8 bg-inverted rounded-lg flex items-center justify-center text-fg-inverted shadow-lg">
              <Command size={16} strokeWidth={3} />
            </div>
            <span className="font-bold text-base xl:text-lg tracking-tight text-fg whitespace-nowrap">
              AI Switchboard
            </span>
          </div>
        </div>

        {/* 中间：导航标签 */}
        <div className="hidden md:flex min-w-0 items-center overflow-x-auto xl:overflow-visible bg-surface-mut/50 p-1 rounded-full border border-line/60 [scrollbar-width:none] [&::-webkit-scrollbar]:hidden">
          {tabs.map(tab => (
            <NavItem
              key={tab.name}
              label={tab.label}
              active={activeTab === tab.name}
              onClick={() => onTabChange(tab.name)}
            />
          ))}
        </div>

        {/* 右侧：状态指示器 + 主题开关 + 头像 */}
        <div className="flex flex-1 items-center justify-end space-x-3 lg:space-x-4">
          {/* 连接状态 */}
          <div className="hidden sm:flex items-center px-3 py-1.5 bg-surface border border-line rounded-md shadow-sm text-xs text-fg-muted">
            <span className="relative flex h-2 w-2 mr-2">
              {status.ping && (
                <span className={`animate-ping absolute inline-flex h-full w-full rounded-full ${status.color} opacity-75`}></span>
              )}
              <span className={`relative inline-flex rounded-full h-2 w-2 ${status.color}`}></span>
            </span>
            {status.text}
          </div>

          {/* 主题开关：位于 no-drag 内层容器，Wails 下可正常点击 */}
          <button
            type="button"
            onClick={toggle}
            title={resolved === 'dark' ? '切换到浅色' : '切换到深色'}
            aria-label="切换主题"
            className="p-2 rounded-lg text-fg-muted hover:text-fg hover:bg-surface-mut transition-colors"
          >
            {resolved === 'dark' ? <Sun size={16} /> : <Moon size={16} />}
          </button>

          {/* 用户头像 */}
          <div className="w-8 h-8 rounded-full bg-gradient-to-tr from-indigo-500 to-purple-500 border-2 border-surface shadow-md cursor-pointer hover:ring-2 ring-accent-line transition-all"></div>
        </div>
      </div>

      {/* 移动端导航 */}
      <div className="md:hidden flex items-center space-x-2 px-4 pb-3 overflow-x-auto">
        {tabs.map(tab => (
          <NavItem
            key={tab.name}
            label={tab.label}
            active={activeTab === tab.name}
            onClick={() => onTabChange(tab.name)}
          />
        ))}
      </div>
    </nav>
  );
};

export default Header;
