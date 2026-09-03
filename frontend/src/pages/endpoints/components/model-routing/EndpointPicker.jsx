// ============================================
// 模型路由：端点多选器
// 2026-09-03
// ============================================

import { useMemo, useState } from 'react';
import { Check, Search } from 'lucide-react';
import { filterEndpoints } from '../../utils/modelRouting.js';

// 选择器只描述端点的配置资格与最近一次检测，不预测真实调度结果——
// 真实候选还要看请求级失败与冷却，那些在映射行的端点徽章上呈现。
const endpointHint = (endpoint) => {
  if (!endpoint.availabilityEnabled) return { label: '硬关闭', tone: 'tone-rose' };
  if (!endpoint.failoverEnabled) return { label: '不参与自动调度', tone: 'tone-amber' };
  if (endpoint.neverChecked || !endpoint.lastCheck) return { label: '未检测', tone: 'tone-slate' };
  if (!endpoint.healthy) return { label: '不可达', tone: 'tone-rose' };
  return { label: '可达', tone: 'tone-emerald' };
};

const EndpointPicker = ({ endpoints = [], selected = [], disabled = false, onToggle, onReplace }) => {
  const [keyword, setKeyword] = useState('');
  const visible = useMemo(() => filterEndpoints(endpoints, keyword), [endpoints, keyword]);
  const visibleNames = useMemo(() => visible.map((endpoint) => endpoint.name), [visible]);
  const allVisibleSelected = visibleNames.length > 0 && visibleNames.every((name) => selected.includes(name));

  // 全选只作用于当前搜索结果：搜完再全选是筛选一批端点最快的路径。
  const selectAllVisible = () => onReplace?.([...new Set([...selected, ...visibleNames])]);
  const clearVisible = () => onReplace?.(selected.filter((name) => !visibleNames.includes(name)));

  return (
    <div className="rounded-xl border border-line bg-surface-sub/40">
      <div className="flex items-center gap-2 border-b border-line-soft px-3 py-2">
        <Search size={14} className="shrink-0 text-fg-subtle" />
        <input
          className="min-w-0 flex-1 bg-transparent text-xs text-fg-body outline-none placeholder:text-fg-subtle"
          value={keyword}
          disabled={disabled}
          onChange={(event) => setKeyword(event.target.value)}
          placeholder="搜索端点名称或 URL"
        />
        <button
          type="button"
          disabled={disabled || visibleNames.length === 0}
          onClick={allVisibleSelected ? clearVisible : selectAllVisible}
          className="shrink-0 rounded-md px-2 py-1 text-[11px] text-accent hover:bg-accent-soft/60 disabled:opacity-40"
        >
          {allVisibleSelected ? '清空' : '全选'}
        </button>
      </div>

      <div className="max-h-[240px] overflow-y-auto p-1.5">
        {visible.length === 0 ? (
          <div className="px-3 py-8 text-center text-xs text-fg-subtle">
            {endpoints.length === 0 ? '还没有可用端点，请先在端点页创建' : '没有匹配的端点'}
          </div>
        ) : (
          visible.map((endpoint) => {
            const checked = selected.includes(endpoint.name);
            const hint = endpointHint(endpoint);
            return (
              <button
                type="button"
                key={endpoint.name}
                disabled={disabled}
                onClick={() => onToggle?.(endpoint.name)}
                className={`flex w-full items-center gap-2.5 rounded-lg px-2.5 py-2 text-left transition-colors disabled:opacity-50 ${
                  checked ? 'bg-accent-soft/60' : 'hover:bg-surface-mut/60'
                }`}
              >
                {/* 整行可点，所以勾选框是自绘的；规格对齐 index.css 的 .app-checkbox */}
                <span
                  className={`flex h-4 w-4 shrink-0 items-center justify-center rounded-[5px] border transition-colors ${
                    checked ? 'border-accent-ring bg-accent-ring text-white' : 'border-line-strong bg-surface'
                  }`}
                >
                  {checked && <Check size={11} strokeWidth={2.5} />}
                </span>
                <span className="min-w-0 flex-1">
                  <span className="block truncate text-xs font-medium text-fg-body">{endpoint.name}</span>
                  <span className="block truncate font-mono text-[10px] text-fg-subtle">{endpoint.url}</span>
                </span>
                <span className="shrink-0 rounded-full bg-surface-mut px-1.5 py-0.5 font-mono text-[10px] text-fg-muted">
                  P{endpoint.priority ?? 1}
                </span>
                <span className={`${hint.tone} shrink-0 rounded-md border px-1.5 py-0.5 text-[10px]`}>{hint.label}</span>
              </button>
            );
          })
        )}
      </div>

      <div className="border-t border-line-soft px-3 py-1.5 text-right text-[11px] text-fg-subtle">
        已选 {selected.length} 个
      </div>
    </div>
  );
};

export default EndpointPicker;
