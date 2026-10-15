// ============================================
// 布局组件 - 侧边栏导航
// 2026-10-07：替代顶部胶囊页签，分「监控 / 路由 / 策略」三组，设置与代理状态固定在底部
// ============================================
// 顶部 52px 与工具栏同高：macOS 下留给系统红绿灯（统一工具栏，实测横跨 x≈19–79pt），
// 其他平台没有红绿灯，放品牌标识。

import { Activity, Boxes, LayoutDashboard, ScrollText, Server, Settings, ShieldCheck, Tag } from 'lucide-react';
import { formatShortcut, isMacPlatform } from '@utils/platform.js';
import BrandMark from './BrandMark.jsx';
import ThemeMenu from './ThemeMenu.jsx';
import { NAV_SECTIONS, SETTINGS_NAV_ITEM } from './navigation.js';

const NAV_ICONS = {
  overview: LayoutDashboard,
  requests: Activity,
  logs: ScrollText,
  endpoints: Server,
  'account-pool': Boxes,
  'privacy-protection': ShieldCheck,
  pricing: Tag,
  settings: Settings
};

// 代理状态：事件通道未连通时先报通道问题，连通后以代理网关真实状态为准
const resolveProxyStatus = (connectionStatus, { running = false, port = 0 }) => {
  if (connectionStatus !== 'connected') {
    return { dot: 'bg-warn-solid', text: connectionStatus === 'connecting' ? '连接中…' : '事件通道断开', port: 0 };
  }
  if (running && port > 0) return { dot: 'bg-success-solid', text: '代理运行中', port };
  return { dot: 'bg-danger-solid', text: '代理未运行', port: 0 };
};

const NavItem = ({ item, active, onSelect, className = '' }) => {
  const Icon = NAV_ICONS[item.name];
  return (
    <button
      type="button"
      onClick={() => onSelect(item.name)}
      aria-current={active ? 'page' : undefined}
      title={`${item.label}  ${formatShortcut(item.shortcut)}`}
      className={`flex h-7 w-full items-center gap-2 rounded-md px-2 text-left text-[13px] ${
        active ? 'bg-sidebar-sel font-medium text-fg' : 'text-fg-body'
      } ${className}`}
    >
      <Icon size={16} strokeWidth={1.75} className="sidebar-icon shrink-0" />
      <span className="truncate">{item.label}</span>
    </button>
  );
};

const Sidebar = ({ activeTab, onTabChange, connectionStatus = 'connected', proxyStatus = {} }) => {
  const status = resolveProxyStatus(connectionStatus, proxyStatus);
  const mac = isMacPlatform();

  return (
    <nav aria-label="主导航" className="flex w-[220px] shrink-0 flex-col border-r border-line bg-sidebar">
      <div className="app-drag flex h-[52px] shrink-0 items-center px-4">
        {!mac && (
          <div className="flex items-center gap-2 text-xs font-medium text-fg-body">
            <BrandMark className="h-3.5 w-3.5" />
            Sinuo
          </div>
        )}
      </div>

      <div className="flex-1 overflow-y-auto px-2.5 pb-2.5">
        {NAV_SECTIONS.map((section) => (
          <div key={section.label}>
            <div className="px-2 pb-1 pt-3 text-[11px] font-semibold text-fg-subtle">{section.label}</div>
            {section.items.map((item) => (
              <NavItem key={item.name} item={item} active={activeTab === item.name} onSelect={onTabChange} />
            ))}
          </div>
        ))}
      </div>

      <div className="border-t border-line px-2.5 pb-2.5 pt-2">
        <div className="flex items-center gap-2 px-2 pb-1.5 pt-1 text-xs text-fg-body" title="主端口的代理网关状态">
          <span className={`h-[7px] w-[7px] shrink-0 rounded-full ${status.dot}`} />
          <span className="truncate">{status.text}</span>
          {status.port > 0 && <span className="ml-auto font-mono text-[11.5px] text-fg-muted">:{status.port}</span>}
        </div>
        <div className="flex items-center gap-1">
          <NavItem item={SETTINGS_NAV_ITEM} active={activeTab === SETTINGS_NAV_ITEM.name} onSelect={onTabChange} className="flex-1" />
          <ThemeMenu placement="top" />
        </div>
      </div>
    </nav>
  );
};

export default Sidebar;
