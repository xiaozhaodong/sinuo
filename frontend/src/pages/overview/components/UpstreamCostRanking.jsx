// ============================================
// 上游成本排行（概览页次级网格左侧）
// 2025-11-28
// 2026-09-09 概览改版：从柱线图改为排行条列表，按上游名称汇总今日 Token 与成本；
// 数据由概览快照统一加载，组件内不再有定时器与 chartUpdate 监听
// ============================================

import { DollarSign } from 'lucide-react';
import SectionError from './SectionError.jsx';
import { buildCostRanking } from '../utils/overviewViewModel.js';

const UpstreamCostRanking = ({ data = [], loading, error, onRetry }) => {
  const rows = buildCostRanking(data);

  return (
    <div className="bg-surface p-6 rounded-2xl border border-line/60 shadow-sm flex flex-col">
      <div className="flex items-start justify-between mb-1">
        <div className="flex items-center space-x-2">
          <div className="p-1.5 bg-danger-soft text-danger-solid rounded-md">
            <DollarSign size={16} />
          </div>
          <h3 className="font-semibold text-fg">上游成本排行</h3>
        </div>
        <span className="text-[11px] text-fg-subtle">今日</span>
      </div>
      <p className="text-xs text-fg-muted mb-4">按真实上游汇总 Token 使用量与预估成本</p>

      <div className="flex-1 min-h-[180px]">
        {loading ? (
          <div className="space-y-3">
            {[0, 1, 2, 3].map((index) => (
              <div key={index} className="h-6 animate-pulse rounded bg-surface-mut" />
            ))}
          </div>
        ) : error ? (
          <SectionError error={error} onRetry={onRetry} compact />
        ) : rows.length === 0 ? (
          <div className="flex h-full min-h-[180px] items-center justify-center text-sm text-fg-subtle">
            今日暂无上游成本数据
          </div>
        ) : (
          <div className="space-y-3.5">
            {rows.map((row) => (
              <div key={row.name} className="grid grid-cols-[96px_1fr_64px] items-center gap-3">
                <span className="truncate text-xs text-fg-body" title={row.name}>{row.name}</span>
                <span className="h-2 overflow-hidden rounded-full bg-surface-mut">
                  <span
                    className="block h-full rounded-full bg-gradient-to-r from-accent-ring to-accent"
                    style={{ width: `${row.percent}%` }}
                  />
                </span>
                <span className="text-right font-mono text-[11px] text-fg-body" title={`Token ${row.tokensText}`}>
                  {row.costText}
                </span>
              </div>
            ))}
          </div>
        )}
      </div>
    </div>
  );
};

export default UpstreamCostRanking;
