// ============================================
// 模型调用情况表（概览页主网格右侧）
// 2026-09-09 概览改版：按模型聚合请求数 / 成功率 / 平均耗时 / Token / 成本
// 数据来自 GetModelUsageStats，口径与请求追踪页一致，不在前端二次聚合
// 2026-10-07 桌面化：面板样式，表头去掉全大写小字
// 2026-10-24 时段统计分区：面板高度由所在行决定，表格填满剩余空间并在表内滚动
// ============================================

import { ExternalLink } from 'lucide-react';
import Panel from '@components/desktop/Panel.jsx';
import ModelTag from '@pages/requests/components/ModelTag.jsx';
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
  const footerText = [
    modelUsage?.truncated ? `Top ${rows.length} · 按请求数排序` : '',
    unresolvedRequestCount > 0 ? `${formatCount(unresolvedRequestCount)} 个请求未解析模型` : ''
  ].filter(Boolean).join(' · ');

  const openRequests = (filter) => {
    if (canOpen && onOpenRequests) onOpenRequests(filter);
  };

  return (
    <Panel
      title="模型调用"
      subtitle={periodCaption}
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

      <div className="flex min-h-0 flex-1 flex-col">
        {loading ? (
          <div className="space-y-2 p-3">
            {[0, 1, 2, 3].map((index) => (
              <div key={index} className="h-9 animate-pulse rounded-lg bg-surface-mut" />
            ))}
          </div>
        ) : error ? (
          <div className="min-h-0 flex-1 overflow-auto">
            <SectionError error={error} onRetry={onRetry} />
          </div>
        ) : rows.length === 0 ? (
          <div className="flex flex-1 items-center justify-center text-sm text-fg-subtle">
            {unresolvedRequestCount > 0 ? `${formatCount(unresolvedRequestCount)} 个请求未解析模型` : '暂无模型调用数据'}
          </div>
        ) : (
          <div className="@container min-h-0 flex-1 overflow-auto">
            <table className="desk-table desk-dense table-fixed">
              <colgroup>
                <col />
                <col className="w-[52px]" />
                <col className="w-[64px]" />
                <col className="w-[56px]" />
                <col className="w-[60px]" />
                <col className="@max-[440px]:hidden w-[88px]" />
              </colgroup>
              <thead>
                <tr>
                  <th>模型</th>
                  <th className="text-right">请求</th>
                  <th className="text-right">成功率</th>
                  <th className="text-right">Token</th>
                  <th className="text-right">成本</th>
                  <th className="@max-[440px]:hidden text-right" title="占全部生成请求的比例">占比</th>
                </tr>
              </thead>
              <tbody>
                {rows.map((row) => (
                  <tr key={row.key} title={row.hoverText} className="hover:bg-surface-sub">
                    <td className="min-w-0">
                      <button
                        type="button"
                        onClick={() => openRequests({ model: row.rawModelName, requestFamily: row.requestFamily })}
                        disabled={!canOpen || !row.rawModelName}
                        className="flex min-w-0 w-full items-center gap-1.5 text-left text-xs text-fg enabled:hover:text-accent"
                      >
                        <ModelTag model={row.rawModelName} compact />
                        {row.showFamilyLabel && <span className="shrink-0 text-[11px] text-fg-subtle">{row.familyText}</span>}
                      </button>
                    </td>
                    <td className="text-right font-mono tabular-nums text-xs text-fg-body">{row.requestCountText}</td>
                    <td className={`text-right font-mono tabular-nums text-xs ${SUCCESS_TONE_CLASSES[row.successRateTone] || SUCCESS_TONE_CLASSES.neutral}`}>{row.successRateText}</td>
                    <td className="text-right font-mono tabular-nums text-xs text-fg-body">{row.tokensText}</td>
                    <td className="text-right font-mono tabular-nums text-xs text-fg-body">{row.costText}</td>
                    <td className="@max-[440px]:hidden text-right">
                      <span className="flex items-center justify-end gap-1.5 whitespace-nowrap">
                        <span className="h-1 w-[36px] overflow-hidden rounded-full bg-surface-mut">
                          <span className="block h-full rounded-full bg-accent-ring/80" style={{ width: `${row.sharePercent}%` }} />
                        </span>
                        <span className="font-mono tabular-nums text-xs text-fg-muted">{row.shareText}</span>
                      </span>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </div>

      {!loading && !error && footerText && (
        <div className="flex flex-wrap items-center justify-between gap-2 border-t border-line-soft px-3 py-2">
          <span className="text-[11px] text-fg-subtle">{footerText}</span>
        </div>
      )}
    </Panel>
  );
};

export default ModelUsageTable;
