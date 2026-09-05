import { useState } from 'react';
import { FlaskConical, Play } from 'lucide-react';

const statusLabels = {
  eligible: '可参与',
  skipped: '已跳过'
};

const CodexRouteTestBox = ({ onTest }) => {
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
        <h3 className="text-xs font-semibold text-fg-body">配置试算</h3>
        <span className="text-[11px] text-fg-subtle">按当前配置模拟一次账号选择，不发起真实请求</span>
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
                命中 <span className="font-mono">{result.model}</span> 的账号组 · {result.route_mode === 'manual_fixed' ? '固定' : result.route_mode === 'manual_preferred' ? '优选' : '自动'}
              </div>
              <div className="flex flex-wrap gap-1">
                {(result.accounts || []).map((account) => (
                  <span
                    key={account.account_id}
                    title={account.detail || account.reason || ''}
                    className={`${account.status === 'eligible' ? 'tone-emerald' : 'tone-amber'} rounded-md border px-1.5 py-0.5`}
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
                    ? '配置加载失败，启用状态下请求会返回 422'
                    : '该模型没有配置账号组，启用状态下请求会返回 422'}
            </div>
          )}
        </div>
      )}
    </div>
  );
};

export default CodexRouteTestBox;
