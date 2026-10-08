import { AlertTriangle, Pencil, Trash2, X } from 'lucide-react';
import { Button } from '@components/ui';
import { ToolbarButton } from '@components/layout/ToolbarButton.jsx';
import { Inspector, InspectorBody, InspectorFooter, InspectorHeader, InspectorSection, Property, PropertyList } from '@components/desktop/Inspector.jsx';
import { useTimezone } from '@contexts/TimezoneContext.jsx';
import { bindingDisplayName, ENDPOINT_STATUS_LABELS, endpointStatusTone, isWebToolsBinding, routeModeLabel } from '../../endpoints/utils/modelRouting.js';
import { routingOutcomeLabel } from '@components/model-routing/routingView.js';

const EndpointDetails = ({ binding, endpoints, formatTimestamp }) => (
  <InspectorSection title={`成员端点 · ${binding.endpoints.length}`}>
    <div className="space-y-3">
      {binding.endpoints.map((endpoint) => {
        const record = endpoints.find((item) => item.name === endpoint.name);
        return (
          <div key={endpoint.name} className="min-w-0 text-[11.5px]">
            <div className="flex flex-wrap items-center gap-1.5">
              <span className="selectable break-all font-medium text-fg">{endpoint.name}</span>
              <span className={`${endpointStatusTone(endpoint.status)} rounded border px-1.5`}>{ENDPOINT_STATUS_LABELS[endpoint.status] || endpoint.status}</span>
              {endpoint.name === binding.route_target && binding.route_mode !== 'auto' && <span className="text-accent">目标</span>}
            </div>
            {record?.url && <div className="selectable mt-1 break-all font-mono text-fg-muted">{record.url}</div>}
            {endpoint.detail && <div className="selectable mt-1 break-words text-fg-muted">{endpoint.detail}</div>}
            <div className="mt-1 text-fg-muted">优先级 {endpoint.priority} · {endpoint.availability_enabled ? '可用性开启' : '可用性关闭'} · {endpoint.auto_schedule_enabled ? '参与自动调度' : '未参与自动调度'}</div>
            {endpoint.cooldown_until && <div className="mt-1 text-warn">冷却至 {formatTimestamp(endpoint.cooldown_until)}{endpoint.cooldown_reason ? ` · ${endpoint.cooldown_reason}` : ''}</div>}
            {(endpoint.status === 'missing' || !record) && <div className="mt-1 text-danger">端点已不存在，编辑保存前请取消选择。</div>}
          </div>
        );
      })}
      {binding.endpoints.length === 0 && <p className="text-xs text-danger">未选择任何端点</p>}
    </div>
  </InspectorSection>
);

const BindingWarnings = ({ binding }) => {
  const blocked = binding.endpoints.filter((endpoint) => endpoint.status !== 'ok');
  if (!blocked.length && !binding.shared_models.length) return null;
  return (
    <InspectorSection title="映射提示">
      <div className="selectable space-y-2 text-[11.5px] text-warn">
        {blocked.length > 0 && <p className="flex items-start gap-1.5"><AlertTriangle size={13} className="shrink-0" />{blocked.length} 个端点当前不可调度</p>}
        {binding.shared_models.length > 0 && <p className="break-words">与 {binding.shared_models.join('、')} 共用旧端点组，编辑后自动拆开</p>}
      </div>
    </InspectorSection>
  );
};

const ClaudeRoutingInspector = ({ binding, endpoints, busy, onEdit, onDelete, onClearSelection }) => {
  const { formatTimestamp } = useTimezone();
  return (
    <Inspector label="Claude 模型路由详情">
      <InspectorHeader title={isWebToolsBinding(binding) ? '联网工具' : binding.model} subtitle="模型路由"
        badges={<button type="button" onClick={onClearSelection} aria-label="清除选择，查看路由试算" title="路由试算" className="ml-auto rounded p-1 text-fg-muted hover:bg-surface-mut"><X size={14} /></button>} />
      <InspectorBody>
        <InspectorSection title="映射">
          <PropertyList labelWidth={60} align="start">
            <Property label="模型" truncate={false}><span className="selectable break-all">{bindingDisplayName(binding)}</span></Property>
            <Property label="状态">{binding.enabled ? '已启用' : '已停用'}</Property>
            <Property label="策略" truncate={false}>{routeModeLabel(binding.route_mode, binding.route_target)}</Property>
            {binding.note && <Property label="备注" truncate={false}><span className="selectable break-words">{binding.note}</span></Property>}
          </PropertyList>
        </InspectorSection>
        <EndpointDetails binding={binding} endpoints={endpoints} formatTimestamp={formatTimestamp} />
        <InspectorSection title="最近命中">
          <PropertyList labelWidth={60}>
            <Property label="端点" title={binding.last_effective_endpoint}>{binding.last_effective_endpoint || '—'}</Property>
            <Property label="时间" title={formatTimestamp(binding.last_decision_at)}>{binding.last_decision_at ? formatTimestamp(binding.last_decision_at) : '—'}</Property>
            <Property label="结果" truncate={false}>{routingOutcomeLabel(binding.last_outcome)}</Property>
          </PropertyList>
        </InspectorSection>
        <BindingWarnings binding={binding} />
      </InspectorBody>
      <InspectorFooter>
        <Button variant="secondary" icon={Pencil} disabled={busy} onClick={() => onEdit(binding)}>编辑…</Button>
        <span className="flex-1" />
        <ToolbarButton icon={Trash2} title="删除映射" disabled={busy} onClick={() => onDelete(binding)} />
      </InspectorFooter>
    </Inspector>
  );
};

export default ClaudeRoutingInspector;
