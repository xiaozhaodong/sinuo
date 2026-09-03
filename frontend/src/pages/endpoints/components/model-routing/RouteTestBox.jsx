// ============================================
// 模型路由：试算面板
// 2026-09-03
// ============================================

import { useState } from 'react';
import { FlaskConical, Play } from 'lucide-react';
import { ENDPOINT_STATUS_LABELS, endpointStatusTone, routeModeLabel } from '../../utils/modelRouting.js';

const unassignedReasons = {
  model_routing_unassigned: '该模型没有配置上游端点，开启状态下请求会返回 422',
  model_routing_missing_model: '请求未携带模型名',
  model_routing_load_failed: '配置加载失败，所有 Claude 请求都会返回 422'
};

const RouteTestBox = ({ onTest }) => {
  const [model, setModel] = useState('');
  const [result, setResult] = useState(null);
  const [testing, setTesting] = useState(false);
  const [error, setError] = useState('');

  const run = async () => {
    setTesting(true);
    setError('');
    try {
      setResult(await onTest(model.trim()));
    } catch (testError) {
      setResult(null);
      setError(testError?.message || '试算失败');
    } finally {
      setTesting(false);
    }
  };

  return (
    <div className="rounded-xl border border-line bg-surface-sub/40 p-3">
      <div className="flex items-center gap-2">
        <FlaskConical size={14} className="text-accent" />
        <h3 className="text-xs font-semibold text-fg-body">路由试算</h3>
        <span className="text-[11px] text-fg-subtle">按当前配置模拟一次归组，不发起真实请求</span>
      </div>

      <div className="mt-2 flex gap-2">
        <input
          className="min-w-0 flex-1 rounded-lg border border-line bg-surface px-2.5 py-1.5 font-mono text-xs text-fg-body outline-none focus:border-accent-line"
          value={model}
          onChange={(event) => setModel(event.target.value)}
          onKeyDown={(event) => { if (event.key === 'Enter' && model.trim()) run(); }}
          placeholder="输入请求携带的模型名"
        />
        <button
          type="button"
          disabled={testing || !model.trim()}
          onClick={run}
          className="inline-flex shrink-0 items-center gap-1 rounded-lg border border-line bg-surface px-2.5 py-1.5 text-xs text-fg-body hover:border-accent-line hover:text-accent disabled:opacity-40"
        >
          <Play size={12} />试算
        </button>
      </div>

      {error && <div className="tone-rose mt-2 rounded-lg border px-2.5 py-1.5 text-[11px]">{error}</div>}

      {result && !error && (
        <div className="mt-2 space-y-1.5 text-[11px]">
          {result.assigned ? (
            <>
              <div className="text-fg-body">
                命中 <span className="font-mono">{result.model}</span> 的映射 · {routeModeLabel(result.route_mode, result.route_target)}
              </div>
              <div className="flex flex-wrap gap-1">
                {(result.members || []).map((member) => (
                  <span
                    key={member.name}
                    title={member.detail || ''}
                    className={`${endpointStatusTone(member.status)} rounded-md border px-1.5 py-0.5`}
                  >
                    {member.name}
                    {member.status !== 'ok' && ` · ${ENDPOINT_STATUS_LABELS[member.status] || member.status}`}
                  </span>
                ))}
              </div>
            </>
          ) : (
            <div className="text-fg-muted">
              {result.scope_kind === 'global'
                ? '模型路由未启用，该请求按全局端点策略调度'
                : unassignedReasons[result.unassigned_reason] || `未分配 · ${result.unassigned_reason || 'unknown'}`}
            </div>
          )}
        </div>
      )}
    </div>
  );
};

export default RouteTestBox;
