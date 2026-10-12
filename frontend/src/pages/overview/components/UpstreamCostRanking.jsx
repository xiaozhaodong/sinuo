// ============================================
// 上游成本排行（概览页次级网格左侧）
// 2025-11-28
// 2026-09-09 概览改版：从柱线图改为排行条列表，按上游名称汇总今日 Token 与成本；
// 数据由概览快照统一加载，组件内不再有定时器与 chartUpdate 监听
// 2026-10-07 桌面化：面板样式，排行条改为纯色细条
// ============================================

import Panel from '@components/desktop/Panel.jsx';
import SectionError from './SectionError.jsx';
import { buildCostRanking } from '../utils/overviewViewModel.js';

const UpstreamCostRanking = ({ data = [], loading, error, onRetry }) => {
  const rows = buildCostRanking(data);

  return (
    <Panel title="上游成本排行" subtitle="今日" bodyClassName="px-3 py-3">
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
          <div className="space-y-2.5">
            {rows.map((row) => (
              <div key={row.key} className="grid grid-cols-[160px_1fr_64px] items-center gap-3">
                <span className="flex min-w-0 truncate text-[12.5px] text-fg-body" title={row.typeLabel ? `${row.name} · ${row.typeLabel}` : row.name}>
                  <span className="truncate">{row.name}</span>
                  {row.typeLabel ? <span className="ml-1.5 shrink-0 text-[11px] text-fg-subtle">{row.typeLabel}</span> : null}
                </span>
                <span className="h-1.5 overflow-hidden rounded-full bg-surface-mut">
                  <span
                    className="block h-full rounded-full bg-accent-ring/80"
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
    </Panel>
  );
};

export default UpstreamCostRanking;
