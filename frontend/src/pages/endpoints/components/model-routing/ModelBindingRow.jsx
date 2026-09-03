// ============================================
// 模型路由：单条模型映射行
// 2026-09-03
// ============================================

import { AlertTriangle, Pencil, Trash2 } from 'lucide-react';
import { CustomSelect } from '@components/ui';
import { useTimezone } from '@contexts/TimezoneContext.jsx';
import { ENDPOINT_STATUS_LABELS, ROUTE_MODES, endpointStatusTone } from '../../utils/modelRouting.js';
import ToggleSwitch from '../ToggleSwitch.jsx';

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

  return (
    <div className={`px-4 py-3 transition-colors ${binding.enabled ? '' : 'bg-surface-sub/50'}`}>
      <div className="flex flex-wrap items-start gap-x-3 gap-y-2">
        <div className="min-w-[180px] flex-1">
          <div className={`truncate font-mono text-sm ${binding.enabled ? 'text-fg-body' : 'text-fg-subtle line-through'}`} title={binding.model}>
            {binding.model}
          </div>
          {binding.note && (
            <div className="mt-0.5 truncate text-[11px] text-fg-subtle" title={binding.note}>{binding.note}</div>
          )}
        </div>

        <CustomSelect
          options={ROUTE_MODES}
          value={binding.route_mode}
          disabled={busy}
          size="sm"
          className="w-[104px] shrink-0"
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
          className="w-[150px] shrink-0"
          onChange={(routeTarget) => onPatch({ route_target: routeTarget })}
        />

        <div className="flex shrink-0 items-center gap-1">
          <ToggleSwitch
            enabled={binding.enabled}
            disabled={busy}
            title={binding.enabled ? '停用该映射' : '启用该映射'}
            onChange={() => onPatch({ enabled: !binding.enabled })}
          />
          <button
            type="button"
            title="编辑模型与端点"
            disabled={busy}
            onClick={onEdit}
            className="inline-flex h-7 w-7 items-center justify-center rounded-lg border border-line bg-surface text-fg-muted hover:border-accent-line hover:text-accent disabled:opacity-50"
          >
            <Pencil size={13} />
          </button>
          <button
            type="button"
            title="删除该模型映射"
            disabled={busy}
            onClick={onDelete}
            className="inline-flex h-7 w-7 items-center justify-center rounded-lg border border-line bg-surface text-fg-muted hover:border-danger hover:text-danger disabled:opacity-50"
          >
            <Trash2 size={13} />
          </button>
        </div>
      </div>

      <div className="mt-2 flex flex-wrap items-center gap-1">
        {binding.endpoints.length === 0 ? (
          <span className="tone-rose rounded-md border px-1.5 py-0.5 text-[11px]">未选择任何端点</span>
        ) : (
          binding.endpoints.map((endpoint) => (
            <span
              key={endpoint.name}
              title={endpoint.detail || ENDPOINT_STATUS_LABELS[endpoint.status] || endpoint.status}
              className={`${endpointStatusTone(endpoint.status)} max-w-full truncate rounded-md border px-1.5 py-0.5 text-[11px] ${
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
        <div className="mt-1.5 flex flex-wrap items-center gap-x-3 gap-y-1 text-[11px] text-fg-subtle">
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
