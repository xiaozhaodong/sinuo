import { useState } from 'react';
import { AlertCircle, Play } from 'lucide-react';
import { Button } from '@components/ui';
import { FormBlock, FormSection } from '@components/desktop/FormSection.jsx';
import { InspectorSection } from '@components/desktop/Inspector.jsx';

const statusLabels = {
  eligible: '可参与',
  skipped: '已跳过'
};

const TestSection = ({ presentation, children }) => presentation === 'inspector' ? (
  <InspectorSection title="配置试算">
    <p className="mb-2 text-[11.5px] text-fg-muted">按当前配置模拟一次账号选择，不发起真实请求</p>
    {children}
  </InspectorSection>
) : (
  <FormSection title="配置试算" description="按当前配置模拟一次账号选择，不发起真实请求"><FormBlock>{children}</FormBlock></FormSection>
);

const CodexRouteTestBox = ({ onTest, presentation = 'form' }) => {
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
                命中 <span className="font-mono">{result.model}</span> 的账号组 · {result.route_mode === 'manual_fixed' ? '固定' : result.route_mode === 'manual_preferred' ? '优选' : '自动'}
              </div>
              <div className="flex flex-wrap gap-1">
                {(result.accounts || []).map((account) => (
                  <span
                    key={account.account_id}
                    title={account.detail || account.reason || ''}
                    className={`${account.status === 'eligible' ? 'tone-emerald' : 'tone-amber'} rounded border px-1.5 text-[11px] leading-[18px]`}
                  >
                    {account.account_name || `账号 ${account.account_id}`} · {statusLabels[account.status] || account.status}
                  </span>
                ))}
              </div>
            </>
          ) : (
            <div className="text-fg-muted">
              {result.scope_kind === 'global'
                ? 'Codex 模型路由未启用，该请求按全局账号池调度'
                : result.unassigned_reason === 'model_routing_missing_model'
                  ? '请求未携带模型名'
                  : result.unassigned_reason === 'model_routing_load_failed'
                    ? '配置加载失败，启用状态下请求会返回 400'
                    : '该模型没有配置账号组，启用状态下请求会返回 400'}
            </div>
          )}
        </div>
      )}
    </TestSection>
  );
};

export default CodexRouteTestBox;
