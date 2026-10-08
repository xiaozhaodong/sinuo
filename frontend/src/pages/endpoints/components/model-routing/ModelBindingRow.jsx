// ============================================
// 模型路由：单条模型映射行
// 2026-09-03
// ============================================

import { AlertTriangle, Globe, Pencil, Trash2 } from 'lucide-react';
import { CustomSelect } from '@components/ui';
import { useTimezone } from '@contexts/TimezoneContext.jsx';
import { ENDPOINT_STATUS_LABELS, ROUTE_MODES, endpointStatusTone, isWebToolsBinding } from '../../utils/modelRouting.js';
import Switch from '@components/desktop/Switch.jsx';

const outcomeLabels = {
  success: '成功',
  cancelled: '已取消',
  quality_incomplete: '响应不完整',
  failed_after_commit: '响应阶段失败',
  passthrough_error: '上游失败',
  no_candidates: '无可用候选',
  manual_fixed_blocked: '固定目标不可用',
  all_candidates_failed: '全部候选失败'
};

const ModelBindingRow = ({ binding, busy, onEdit, onDelete, onPatch }) => {
  const { formatTimestamp } = useTimezone();
  const blocked = binding.endpoints.filter((endpoint) => endpoint.status !== 'ok');
  const targetOptions = binding.endpoints.map((endpoint) => ({ value: endpoint.name, label: endpoint.name }));
  const webTools = isWebToolsBinding(binding);

  return (
    <div className="px-3.5 py-2">
      <div className="flex flex-wrap items-center gap-2">
        <div className="min-w-[180px] flex-1">
          {webTools ? (
            <div
              className={`flex items-center gap-1.5 text-[13px] ${binding.enabled ? 'text-fg-body' : 'text-fg-muted line-through'}`}
              title="请求 tools 里带 web_search / web_fetch 时优先命中，不看模型名"
            >
              <Globe size={13} className="shrink-0 text-accent" />
              <span className="truncate">联网工具</span>
              <span className="shrink-0 rounded border border-line px-1.5 font-mono text-[11px] leading-[18px] text-fg-muted">web_search / web_fetch</span>
            </div>
          ) : (
            <div className={`truncate font-mono text-[13px] ${binding.enabled ? 'text-fg-body' : 'text-fg-muted line-through'}`} title={binding.model}>
              {binding.model}
            </div>
          )}
          {binding.note && (
            <div className="mt-0.5 truncate text-[11px] text-fg-muted" title={binding.note}>{binding.note}</div>
          )}
        </div>

        <CustomSelect
          options={ROUTE_MODES}
          value={binding.route_mode}
          disabled={busy}
          size="sm"
          className="w-[100px] shrink-0"
          onChange={(routeMode) => {
            // 切到优选/固定时后端要求目标必须是成员，只有一个端点就直接替用户定下来。
            const routeTarget =
              routeMode === 'auto' ? '' : binding.route_target || (targetOptions.length === 1 ? targetOptions[0].value : '');
            onPatch({ route_mode: routeMode, route_target: routeTarget });
          }}
        />

        <CustomSelect
          options={targetOptions}
          value={binding.route_target}
          disabled={busy || binding.route_mode === 'auto'}
          size="sm"
          placeholder={binding.route_mode === 'auto' ? '自动挑选' : '选择目标端点'}
          className="w-[170px] shrink-0"
          onChange={(routeTarget) => onPatch({ route_target: routeTarget })}
        />

        <div className="flex shrink-0 items-center gap-1">
          <Switch
            checked={binding.enabled}
            disabled={busy}
            title={binding.enabled ? '停用该映射' : '启用该映射'}
            onChange={(enabled) => onPatch({ enabled })}
          />
          <button
            type="button"
            title="编辑模型与端点"
            disabled={busy}
            onClick={onEdit}
            className="inline-flex h-6 w-6 items-center justify-center rounded text-fg-muted hover:bg-surface-mut hover:text-fg disabled:opacity-50"
          >
            <Pencil size={14} />
          </button>
          <button
            type="button"
            title="删除该模型映射"
            disabled={busy}
            onClick={onDelete}
            className="inline-flex h-6 w-6 items-center justify-center rounded text-fg-muted hover:bg-surface-mut hover:text-danger disabled:opacity-50"
          >
            <Trash2 size={14} />
          </button>
        </div>
      </div>

      <div className="mt-1.5 flex flex-wrap items-center gap-1">
        {binding.endpoints.length === 0 ? (
          <span className="tone-rose rounded border px-1.5 text-[11px] leading-[18px]">未选择任何端点</span>
        ) : (
          binding.endpoints.map((endpoint) => (
            <span
              key={endpoint.name}
              title={endpoint.detail || ENDPOINT_STATUS_LABELS[endpoint.status] || endpoint.status}
              className={`${endpointStatusTone(endpoint.status)} max-w-full truncate rounded border px-1.5 text-[11px] leading-[18px] ${
                endpoint.name === binding.route_target ? 'font-semibold' : ''
              }`}
            >
              {endpoint.name}
              {endpoint.status !== 'ok' && ` · ${ENDPOINT_STATUS_LABELS[endpoint.status] || endpoint.status}`}
            </span>
          ))
        )}
      </div>

      {(binding.last_effective_endpoint || blocked.length > 0 || binding.shared_models.length > 0) && (
        <div className="mt-1.5 flex flex-wrap items-center gap-x-3 gap-y-1 text-[11px] text-fg-muted">
          {binding.last_effective_endpoint && (
            <span>
              最近命中 {binding.last_effective_endpoint}
              {binding.last_decision_at ? ` · ${formatTimestamp(binding.last_decision_at)}` : ''}
              {binding.last_outcome ? ` · ${outcomeLabels[binding.last_outcome] || binding.last_outcome}` : ''}
            </span>
          )}
          {blocked.length > 0 && (
            <span className="inline-flex items-center gap-1 text-warn">
              <AlertTriangle size={11} />{blocked.length} 个端点当前不可调度
            </span>
          )}
          {binding.shared_models.length > 0 && (
            <span className="inline-flex items-center gap-1 text-warn" title={binding.shared_models.join('、')}>
              <AlertTriangle size={11} />与 {binding.shared_models.join('、')} 共用旧端点组，编辑后自动拆开
            </span>
          )}
        </div>
      )}
    </div>
  );
};

export default ModelBindingRow;
