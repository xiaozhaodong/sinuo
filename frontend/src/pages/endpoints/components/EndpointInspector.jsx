// ============================================
// Claude 端点检查器：选中端点的状态、调度、认证与模型改写
// 2026-10-07：替代表格行内的按钮组与网格卡片；编辑仍走 EndpointForm 弹窗（字段多、需校验）
// ============================================

import { Activity, Info, Pencil, Server, Trash2 } from 'lucide-react';
import { Button } from '@components/ui';
import { ToolbarButton } from '@components/layout/ToolbarButton.jsx';
import Switch from '@components/desktop/Switch.jsx';
import {
  Inspector,
  InspectorBody,
  InspectorEmpty,
  InspectorFooter,
  InspectorHeader,
  InspectorSection,
  Property,
  PropertyList
} from '@components/desktop/Inspector.jsx';
import { useTimezone } from '@contexts/TimezoneContext.jsx';
import { getEndpointLastCheckDisplayValue } from '../utils/lastCheckDisplay.js';
import { parseEndpointModelRewriteSettings } from '../utils/modelRewrite.js';
import { summarizeEndpointAuthentication } from '../utils/endpointFormState.js';
import ActionButtons from './ActionButtons.jsx';
import EndpointCooldownInfo from './EndpointCooldownInfo.jsx';
import { HealthCell, RoutingTags } from './EndpointsTable.jsx';

const EndpointInspector = ({
  endpoint,
  routingState,
  busy,
  modelRoutingEnabled,
  onTest,
  onEdit,
  onDelete,
  onAvailabilityChange,
  onAutoScheduleChange,
  onClearCooldown,
  onSetRouting
}) => {
  const { formatTimestamp } = useTimezone();

  if (!endpoint) {
    return (
      <Inspector label="端点详情">
        <InspectorEmpty icon={Server}>选择一个端点查看状态与调度</InspectorEmpty>
      </Inspector>
    );
  }

  const lastCheckRaw = getEndpointLastCheckDisplayValue(endpoint);
  const lastCheck = lastCheckRaw === '-' ? '—' : formatTimestamp(lastCheckRaw);
  const rewrite = parseEndpointModelRewriteSettings(endpoint.modelRewriteRules || '');

  return (
    <Inspector label="端点详情">
      <InspectorHeader
        title={endpoint.name}
        subtitle={endpoint.url}
        badges={<span className="flex shrink-0 items-center"><RoutingTags endpoint={endpoint} routingState={routingState} /></span>}
      />

      <InspectorBody>
        <InspectorSection title="状态">
          <PropertyList>
            <Property label="连通性"><HealthCell endpoint={endpoint} /></Property>
            <Property label="最近检测" title={lastCheck}>{lastCheck}</Property>
            <Property label="冷却" truncate={false}>
              <EndpointCooldownInfo endpoint={endpoint} busy={busy} onClearCooldown={onClearCooldown} emptyFallback={<span className="text-fg-subtle">无</span>} />
            </Property>
          </PropertyList>
          <p className="mt-2.5 flex gap-1.5 text-[11.5px] leading-relaxed text-fg-subtle">
            <Info size={12} className="mt-0.5 shrink-0" />
            「可达 / 不可达」只表示最近一次主动检测；真实请求仍按硬启用、自动调度资格、请求级失败与冷却选择候选。
          </p>
        </InspectorSection>

        <InspectorSection title="调度">
          <PropertyList>
            <Property label="优先级" title="数字越小越优先">{endpoint.priority ?? 1}</Property>
            <Property label="硬启用" truncate={false}>
              <Switch checked={endpoint.availabilityEnabled} disabled={busy} onChange={(checked) => onAvailabilityChange(endpoint.name, checked)} label="硬启用" />
            </Property>
            <Property label="自动调度" truncate={false}>
              <Switch checked={endpoint.failoverEnabled} disabled={busy || !endpoint.availabilityEnabled} onChange={(checked) => onAutoScheduleChange(endpoint.name, checked)} label="参与自动调度" />
            </Property>
            <Property label="路由" truncate={false}>
              <div className="flex justify-start">
                <ActionButtons
                  endpointName={endpoint.name}
                  routingState={routingState}
                  onSetRouting={onSetRouting}
                  disabled={busy || !endpoint.availabilityEnabled || modelRoutingEnabled}
                  disabledReason="模型路由开启中，请在模型路由面板按模型设置"
                />
              </div>
            </Property>
          </PropertyList>
        </InspectorSection>

        <InspectorSection title="认证">
          <PropertyList>
            <Property label="方式">{summarizeEndpointAuthentication(endpoint)}</Property>
          </PropertyList>
        </InspectorSection>

        <InspectorSection title="模型改写">
          {rewrite.enabled ? (
            <div className="space-y-1.5">
              {rewrite.rules.map((rule) => (
                <div key={`${rule.source}->${rule.target}`} className="flex items-center gap-1.5 rounded-md border border-line bg-surface px-2 py-1 font-mono text-xs text-fg-body">
                  <span className="min-w-0 truncate" title={rule.source}>{rule.source}</span>
                  <span className="shrink-0 text-fg-subtle">→</span>
                  <span className="min-w-0 truncate" title={rule.target}>{rule.target}</span>
                </div>
              ))}
            </div>
          ) : (
            <p className="text-[12.5px] text-fg-subtle">未配置，按原始模型名转发</p>
          )}
        </InspectorSection>
      </InspectorBody>

      <InspectorFooter>
        <Button variant="secondary" icon={Activity} disabled={busy} onClick={() => onTest(endpoint.name)}>检测</Button>
        <Button variant="secondary" icon={Pencil} disabled={busy} onClick={() => onEdit(endpoint)}>编辑…</Button>
        <span className="flex-1" />
        <ToolbarButton icon={Trash2} title="删除端点" disabled={busy} onClick={() => onDelete(endpoint)} />
      </InspectorFooter>
    </Inspector>
  );
};

export default EndpointInspector;
