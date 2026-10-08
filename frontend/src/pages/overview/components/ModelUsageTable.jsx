// ============================================
// 模型调用情况表（概览页主网格右侧）
// 2026-09-09 概览改版：按模型聚合请求数 / 成功率 / 平均耗时 / Token / 成本
// 数据来自 GetModelUsageStats，口径与请求追踪页一致，不在前端二次聚合
// 2026-10-07 桌面化：面板样式，表头去掉全大写小字
// ============================================

import { ExternalLink, Filter } from 'lucide-react';
import Panel from '@components/desktop/Panel.jsx';
import SectionError from './SectionError.jsx';
import { buildModelRows, formatCount, formatCostUSD, isUnresolvedModelName } from '../utils/overviewViewModel.js';

const FAMILY_PILLS = [
  { key: 'claude', label: 'Claude', dotClass: 'bg-accent' },
  { key: 'codex', label: 'Codex', dotClass: 'bg-violet-500' }
];

const SUCCESS_TONE_CLASSES = {
  success: 'text-success',
  danger: 'text-danger',
  neutral: 'text-fg-body'
};

const ModelUsageTable = ({ modelUsage, periodCaption = '', loading, error, onRetry, onOpenRequests }) => {
  const rows = buildModelRows(modelUsage);
  const unresolvedRequestCount = (Array.isArray(modelUsage?.rows) ? modelUsage.rows : [])
    .filter((row) => isUnresolvedModelName(row?.model_name))
    .reduce((total, row) => total + (Number(row?.request_count) || 0), 0);
  const familyCounts = {
    claude: Number(modelUsage?.claude_count) || 0,
    codex: Number(modelUsage?.codex_count) || 0
  };
  const hasSummary = familyCounts.claude > 0 || familyCounts.codex > 0;
  const canOpen = !loading && !error && Boolean(modelUsage?.start_time && modelUsage?.end_time);

  const openRequests = (filter) => {
    if (canOpen && onOpenRequests) onOpenRequests(filter);
  };

  return (
    <Panel
      title="模型调用"
      subtitle={`${periodCaption ? `${periodCaption} · ` : ''}次数仅计生成，用量含独立压缩`}
      actions={(
        <button
          type="button"
          onClick={() => openRequests({})}
          disabled={!canOpen}
          className="inline-flex items-center gap-1 whitespace-nowrap text-xs text-accent hover:text-accent-fg"
        >
          查看请求明细
          <ExternalLink size={12} />
        </button>
      )}
      bodyClassName="flex flex-col"
    >
      {hasSummary && !loading && !error && (
        <div className="flex flex-wrap items-center gap-2 px-3 pb-1 pt-2.5">
          {/* 家族计数是生成口径：跳转收窄为「仅生成」，列表行数才能与数字对齐 */}
          {FAMILY_PILLS.map((pill) => (
            <button
              key={pill.key}
              type="button"
              onClick={() => openRequests({ requestFamily: pill.key, includeCompaction: false })}
              className="inline-flex h-6 items-center gap-1.5 rounded-md border border-line bg-surface px-2 text-[11.5px] font-medium text-fg-body shadow-xs hover:border-accent-line hover:text-accent"
              title={`只看 ${pill.label} 请求`}
            >
              <span className={`h-1.5 w-1.5 rounded-full ${pill.dotClass}`} />
              {pill.label}
              <span className="font-mono text-fg">{formatCount(familyCounts[pill.key])}</span>
            </button>
          ))}
          {modelUsage?.total_cost_usd > 0 && (
            <span className="ml-auto text-[11px] text-fg-subtle font-mono">
              合计 {formatCostUSD(modelUsage.total_cost_usd)}
            </span>
          )}
        </div>
      )}

      <div className="flex-1 min-h-[220px]">
        {loading ? (
          <div className="space-y-2 p-3">
            {[0, 1, 2, 3].map((index) => (
              <div key={index} className="h-9 animate-pulse rounded-lg bg-surface-mut" />
            ))}
          </div>
        ) : error ? (
          <SectionError error={error} onRetry={onRetry} />
        ) : rows.length === 0 ? (
          <div className="flex h-full min-h-[220px] items-center justify-center text-sm text-fg-subtle">
            {unresolvedRequestCount > 0 ? `${formatCount(unresolvedRequestCount)} 个请求未解析模型` : '暂无模型调用数据'}
          </div>
        ) : (
          <div className="max-h-[340px] overflow-auto">
            {/* table-fixed：模型列固定占 38%，剩余 5 列等分，避免 auto 布局把
                多余宽度摊给数字列、把模型名挤到只剩省略号 */}
            <table className="w-full min-w-[400px] table-fixed border-collapse">
              <thead className="sticky top-0 z-10 bg-surface">
                <tr>
                  {['模型', '请求数', '成功率', '平均耗时', 'Token', '成本'].map((label, index) => (
                    <th
                      key={label}
                      className={`h-7 whitespace-nowrap border-b border-line px-2 text-xs font-medium text-fg-muted first:pl-3 last:pr-3 ${index === 0 ? 'w-[38%] text-left' : 'text-right'}`}
                    >
                      {label}
                    </th>
                  ))}
                </tr>
              </thead>
              <tbody>
                {rows.map((row) => (
                  <tr key={row.key} className="border-b border-line-soft last:border-b-0 hover:bg-surface-sub">
                    <td className="py-2 pl-3 pr-2 text-left">
                      <button
                        type="button"
                        onClick={() => openRequests({ model: row.rawModelName, requestFamily: row.requestFamily, sourceView: row.upstreamType })}
                        disabled={!canOpen || !row.rawModelName}
                        className="block w-full truncate text-left text-xs font-semibold text-fg enabled:hover:text-accent"
                        title={row.rawModelName ? row.modelName : '记录缺少模型名，无法按具体模型筛选'}
                      >
                        {row.modelName}
                      </button>
                      <span className="mt-0.5 block truncate text-[10px] text-fg-muted">{row.sourceLabel}</span>
                    </td>
                    <td className="whitespace-nowrap px-2 py-2 text-right font-mono text-[11px] text-fg-body">{row.requestCountText}</td>
                    <td className={`whitespace-nowrap px-2 py-2 text-right font-mono text-[11px] ${SUCCESS_TONE_CLASSES[row.successRateTone] || SUCCESS_TONE_CLASSES.neutral}`}>{row.successRateText}</td>
                    <td className="whitespace-nowrap px-2 py-2 text-right font-mono text-[11px] text-fg-body">{row.durationText}</td>
                    <td className="whitespace-nowrap px-2 py-2 text-right font-mono text-[11px] text-fg-body">{row.tokensText}</td>
                    <td className="whitespace-nowrap py-2 pl-2 pr-3 text-right font-mono text-[11px] font-semibold text-fg">{row.costText}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </div>

      {!loading && !error && (rows.length > 0 || unresolvedRequestCount > 0) && (
        <div className="flex flex-wrap items-center justify-between gap-2 border-t border-line-soft px-3 py-2">
          <span className="text-[11px] text-fg-subtle">
            {modelUsage?.truncated ? `Top ${rows.length} · 按请求数排序` : `共 ${rows.length} 个模型`}
            {unresolvedRequestCount > 0 ? ` · ${formatCount(unresolvedRequestCount)} 个请求未解析模型` : ''}
          </span>
          <button
            type="button"
            onClick={() => openRequests({})}
            className="inline-flex items-center gap-1 text-[11px] font-medium text-accent hover:text-accent-fg"
          >
            <Filter size={12} />
            按模型 / 来源筛选
          </button>
        </div>
      )}
    </Panel>
  );
};

export default ModelUsageTable;
