// ============================================
// 模型路由：试算面板
// 2026-09-03
// ============================================

import { useState } from 'react';
import { AlertCircle, Play } from 'lucide-react';
import { Button } from '@components/ui';
import { FormBlock, FormSection } from '@components/desktop/FormSection.jsx';
import { InspectorSection } from '@components/desktop/Inspector.jsx';
import { ENDPOINT_STATUS_LABELS, endpointStatusTone, routeModeLabel } from '../../utils/modelRouting.js';

const unassignedReasons = {
  model_routing_unassigned: '该模型没有配置上游端点，开启状态下请求会返回 422',
  model_routing_missing_model: '请求未携带模型名',
  model_routing_load_failed: '配置加载失败，所有 Claude 请求都会返回 422'
};

const TestSection = ({ presentation, children }) => presentation === 'inspector' ? (
  <InspectorSection title="路由试算">
    <p className="mb-2 text-[11.5px] text-fg-muted">按当前配置模拟一次归组，不发起真实请求</p>
    {children}
  </InspectorSection>
) : (
  <FormSection title="路由试算" description="按当前配置模拟一次归组，不发起真实请求"><FormBlock>{children}</FormBlock></FormSection>
);

const RouteTestBox = ({ onTest, presentation = 'form' }) => {
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
    <TestSection presentation={presentation}>
      <div className="flex gap-2">
        <input
          className="desk-field min-w-0 flex-1 font-mono"
          value={model}
          onChange={(event) => setModel(event.target.value)}
          onKeyDown={(event) => { if (event.key === 'Enter' && model.trim()) run(); }}
          placeholder="输入请求携带的模型名"
        />
        <Button
          type="button"
          variant="secondary"
          size="sm"
          disabled={testing || !model.trim()}
          onClick={run}
        >
          <Play size={12} />试算
        </Button>
      </div>

      {error && <div className="mt-3 flex items-start gap-2 rounded-lg border border-danger-line bg-danger-soft px-3 py-2 text-xs text-danger"><AlertCircle size={14} className="mt-0.5 shrink-0" />{error}</div>}

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
                    className={`${endpointStatusTone(member.status)} rounded border px-1.5 text-[11px] leading-[18px]`}
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
    </TestSection>
  );
};

export default RouteTestBox;
