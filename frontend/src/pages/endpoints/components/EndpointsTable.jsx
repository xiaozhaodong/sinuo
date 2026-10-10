// ============================================
// Claude 端点表格（桌面化）：一行一个端点，32px 行高
// 2026-10-07：替代原来的宽表格（9 列 + 行内按钮）与网格卡片；操作移到检查器与右键菜单
// ============================================

import { summarizeEndpointModelRewriteRules } from '../utils/modelRewrite.js';

const statusDotClass = (endpoint) => {
  if (!endpoint.availabilityEnabled) return 'bg-fg-subtle';
  if (endpoint.inCooldown) return 'bg-warn-solid';
  if (endpoint.neverChecked || !endpoint.lastCheck) return 'bg-fg-subtle';
  return endpoint.healthy ? 'bg-success-solid' : 'bg-danger-solid';
};

export const HealthCell = ({ endpoint }) => {
  if (endpoint.neverChecked || !endpoint.lastCheck) return <span className="text-xs text-fg-subtle">未检测</span>;
  return (
    <span className="inline-flex items-center gap-1.5 text-[12.5px]">
      <span className={endpoint.healthy ? 'text-success' : 'text-danger'}>{endpoint.healthy ? '可达' : '不可达'}</span>
      {endpoint.healthy && endpoint.responseTimeMs > 0 && <span className="font-mono text-[11.5px] text-fg-subtle">{Math.round(endpoint.responseTimeMs)}ms</span>}
    </span>
  );
};

// 路由标签：手动优选 / 固定的目标端点，以及最近一次实际命中的端点
export const RoutingTags = ({ endpoint, routingState }) => {
  const mode = routingState?.mode || 'auto';
  const target = routingState?.endpointName || routingState?.endpoint_name || '';
  const effective = routingState?.lastEffectiveEndpoint || routingState?.last_effective_endpoint || '';
  return (
    <>
      {target === endpoint.name && mode === 'manual_preferred' && <span className="ml-1.5 rounded border border-accent-line px-1 text-[10.5px] text-accent">优选</span>}
      {target === endpoint.name && mode === 'manual_fixed' && <span className="ml-1.5 rounded border border-danger-line px-1 text-[10.5px] text-danger">固定</span>}
      {effective === endpoint.name && <span className="ml-1.5 rounded bg-accent-soft px-1 text-[10.5px] text-accent-fg" title="最近一次路由命中">当前</span>}
    </>
  );
};

const EndpointsTable = ({ endpoints, routingState, selectedName, busy, onSelect, onRowContextMenu, onAvailabilityChange, onAutoScheduleChange }) => (
  <table className="desk-table">
    <thead>
      <tr>
        <th className="w-[18px]" aria-label="状态" />
        <th>名称</th>
        <th>URL</th>
        <th className="text-center">优先级</th>
        <th className="text-center">启用</th>
        <th className="text-center">调度</th>
        <th>连通性</th>
        <th>冷却</th>
      </tr>
    </thead>
    <tbody>
      {endpoints.map((endpoint) => {
        const rewrite = summarizeEndpointModelRewriteRules(endpoint.modelRewriteRules || '');
        return (
          <tr
            key={endpoint.id || endpoint.name}
            data-row-id={endpoint.name}
            aria-selected={selectedName === endpoint.name}
            onClick={() => onSelect(endpoint.name)}
            onContextMenu={(event) => onRowContextMenu(event, endpoint)}
            className={endpoint.availabilityEnabled ? '' : 'text-fg-subtle'}
          >
            <td><span className={`block h-[7px] w-[7px] rounded-full ${statusDotClass(endpoint)}`} /></td>
            <td className={`font-medium ${endpoint.availabilityEnabled ? 'text-fg' : ''}`}>
              {endpoint.name}
              <RoutingTags endpoint={endpoint} routingState={routingState} />
              {rewrite && <span className="ml-1.5 rounded border border-line px-1 text-[10.5px] font-normal text-fg-muted" title={rewrite.title}>改写 {rewrite.count}</span>}
            </td>
            <td><span className="block max-w-[180px] truncate font-mono text-xs text-fg-subtle" title={endpoint.url}>{endpoint.url.replace(/^https?:\/\//, '')}</span></td>
            <td className="text-center tabular-nums">{endpoint.priority ?? 1}</td>
            <td className="text-center">
              <input
                type="checkbox"
                className="app-checkbox mx-auto"
                checked={endpoint.availabilityEnabled}
                disabled={busy}
                onChange={(event) => onAvailabilityChange(endpoint.name, event.target.checked)}
                aria-label={`${endpoint.name} 硬启用`}
                title="硬启用"
              />
            </td>
            <td className="text-center">
              <input
                type="checkbox"
                className="app-checkbox mx-auto"
                checked={endpoint.failoverEnabled}
                disabled={busy || !endpoint.availabilityEnabled}
                onChange={(event) => onAutoScheduleChange(endpoint.name, event.target.checked)}
                aria-label={`${endpoint.name} 参与自动调度`}
                title="参与自动调度"
              />
            </td>
            <td><HealthCell endpoint={endpoint} /></td>
            <td className="text-xs">
              {endpoint.inCooldown
                ? <span className="text-warn" title={endpoint.cooldownReason}>冷却中</span>
                : endpoint.cooldownPersistPending
                  ? <span className="text-warn/80" title="上次解除未完全持久化">待重试</span>
                  : <span className="text-fg-subtle">—</span>}
            </td>
          </tr>
        );
      })}
    </tbody>
  </table>
);

export default EndpointsTable;
