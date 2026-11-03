import { useEffect, useRef } from 'react';
import { ArrowUpRight, ChevronLeft, ChevronRight, X } from 'lucide-react';
import useListKeyboard from '@components/desktop/useListKeyboard.js';
import { useTimezone } from '@contexts/TimezoneContext.jsx';
import useRequestLifecycleDetail from '../hooks/useRequestLifecycleDetail.js';
import { formatSessionCost, formatSessionNumber, isInputSpike } from '../utils/sessionUsage.js';

const SMALL_BUTTON = 'inline-flex items-center gap-1 rounded border border-line px-2 py-0.5 text-xs text-fg-body hover:bg-surface-mut disabled:opacity-40';

export default function SessionDetailPane({ sessions, onClose }) {
  const { selected: row, trend, point, trendError } = sessions;
  const { formatListTimestamp } = useTimezone();
  const { lifecycle, lifecycleLoading } = useRequestLifecycleDetail(point?.request_id, Boolean(point));
  const pointID = point?.request_id;
  const containerRef = useRef(null);
  const points = trend?.points || [];
  const navigate = useListKeyboard({ ids: points.map((p) => p.request_id), selectedId: point?.request_id,
    onSelect: (id) => sessions.choosePoint(points.find((p) => p.request_id === id)), containerRef });
  useEffect(() => {
    if (!pointID) return;
    for (const attr of ['data-row-id', 'data-chart-point']) {
      const node = containerRef.current?.querySelector(`[${attr}="${CSS.escape(pointID)}"]`);
      const viewport = node?.closest('[data-session-scroll]');
      if (!node || !viewport) continue;
      const rect = node.getBoundingClientRect();
      const bounds = viewport.getBoundingClientRect();
      if (rect.top < bounds.top) viewport.scrollTop -= bounds.top - rect.top;
      if (rect.bottom > bounds.bottom) viewport.scrollTop += rect.bottom - bounds.bottom;
      if (rect.left < bounds.left) viewport.scrollLeft -= bounds.left - rect.left;
      if (rect.right > bounds.right) viewport.scrollLeft += rect.right - bounds.right;
    }
  }, [pointID, sessions.pointFocusRevision]);
  const ceiling = Math.max(1, ...points.map((p) => p.full_input || 0));
  const request = lifecycle?.request;
  const spike = isInputSpike(point);
  return (
    <section ref={containerRef} className="flex min-h-0 flex-1 flex-col" aria-label="会话用量详情">
      <div className="flex min-h-8 shrink-0 items-center gap-2 border-b border-line px-3 text-xs">
        <strong className="font-semibold">会话详情</strong>
        {row && <><span className="text-fg-muted">{row.request_family}</span><span className="truncate font-mono" title={row.session_id}>{row.session_id.slice(0, 12)}</span></>}
        <span className="flex-1" />
        <button type="button" disabled={!point || sessions.locating} onClick={() => sessions.navigate()} className={`${SMALL_BUTTON} shrink-0 text-accent`}>
          {sessions.locating ? '正在定位…' : '只看此会话'}<ArrowUpRight size={12} />
        </button>
        <button type="button" aria-label="关闭会话详情" onClick={onClose} className="text-fg-muted hover:text-fg"><X size={14} /></button>
      </div>
      {!row ? <div className="grid flex-1 place-items-center text-xs text-fg-subtle">选择会话查看完整输入走势</div>
        : trendError ? <div role="alert" className="p-4 text-xs text-danger">走势加载失败：{trendError}<button className="ml-2 text-accent" onClick={() => sessions.loadTrend()}>重试</button></div>
          : !trend ? <div className="p-4 text-xs text-fg-subtle">加载会话走势…</div>
            : trend.selected_missing || trend.total === 0 ? <div role="status" className="p-4 text-xs text-fg-muted">{trend.selected_missing ? '已选请求已不在当前筛选范围内。' : '此会话已不在当前筛选范围内。'}{trend.total > 0 && <button className="ml-2 text-accent" onClick={sessions.resetPoint}>重新选择范围内请求</button>}</div>
              : <div className="grid min-h-0 flex-1 grid-cols-[minmax(320px,1fr)_280px] overflow-hidden max-[900px]:grid-cols-1 max-[900px]:overflow-y-auto">
                <div className="flex min-h-0 min-w-0 flex-col border-r border-line max-[900px]:min-h-64">
                  <div className="flex h-7 shrink-0 items-center gap-2 px-3 text-[11px] text-fg-muted">
                    <strong className="font-medium text-fg-body">每次请求的完整输入 · Token</strong><span className="flex-1" />
                    <span className="inline-flex items-center gap-1 whitespace-nowrap"><span className="h-1.5 w-1.5 rounded-sm bg-accent-ring/45" />输入</span>
                    <span className="inline-flex items-center gap-1 whitespace-nowrap"><span className="h-1.5 w-1.5 rounded-sm bg-warn-solid" />突增</span>
                    <span className="inline-flex items-center gap-1 whitespace-nowrap"><span className="h-1.5 w-1.5 rounded-sm ring-1 ring-accent-ring" />选中</span>
                    <span className="inline-flex items-center gap-1 whitespace-nowrap"><span className="h-1.5 w-1.5 rounded-sm border border-dashed border-fg-subtle/60" />未记录</span>
                  </div>
                  <div data-session-scroll className="shrink-0 overflow-x-auto px-3" aria-label="输入走势">
                    <div className="flex h-[112px] items-end gap-1 border-b border-line pb-5 pt-4" style={{ minWidth: points.length * 14 }}>
                      {points.map((p, index) => {
                        const selected = point?.request_id === p.request_id;
                        const unknown = p.full_input === null;
                        const inputSpike = isInputSpike(p);
                        return <button key={p.request_id} type="button" data-chart-point={p.request_id}
                        aria-pressed={selected}
                        aria-label={`第 ${p.ordinal} 次请求，完整输入 ${formatSessionNumber(p.full_input)} Token${inputSpike ? '，输入突增' : ''}`}
                        title={`${p.request_id}\n完整输入 ${formatSessionNumber(p.full_input)} Token · 估算成本 ${formatSessionCost(p.cost)}`}
                        onClick={() => sessions.choosePoint(p)} className="group relative flex h-full min-w-[10px] flex-1 items-end justify-center rounded-sm focus-visible:outline-2 focus-visible:outline-accent-ring">
                        <span className={`block w-full max-w-12 rounded-t-sm transition-colors duration-150 motion-reduce:transition-none ${unknown ? 'border border-dashed border-fg-subtle/60 bg-transparent group-hover:border-accent-ring' : inputSpike ? 'bg-warn-solid group-hover:bg-warn-solid/80' : selected ? 'bg-accent-ring' : 'bg-accent-ring/45 group-hover:bg-accent-ring/65'} ${selected ? 'ring-2 ring-accent-ring ring-offset-1 ring-offset-surface' : ''}`}
                          style={{ height: unknown ? 8 : Math.max(2, p.full_input / ceiling * 100) + '%' }} />
                        {(points.length <= 32 || index % 10 === 0 || selected) && <span className={`absolute -bottom-4 text-[9px] font-mono ${selected ? 'text-accent' : 'text-fg-subtle'}`}>{p.ordinal}</span>}
                      </button>; })}
                    </div>
                  </div>
                  <div className="flex h-7 shrink-0 items-center gap-2 border-b border-line px-3 text-[11px] text-fg-muted">
                    <span>显示第 {points[0]?.ordinal}–{points.at(-1)?.ordinal} / {trend.total} 次</span><span className="flex-1" />
                    <button type="button" title="更早的请求" disabled={trend.page <= 1} onClick={() => sessions.loadTrend(trend.page - 1)} className="disabled:opacity-30"><ChevronLeft size={14} /></button>
                    <span>{trend.page} / {Math.max(1, Math.ceil(trend.total / trend.page_size))}</span>
                    <button type="button" title="更晚的请求" disabled={trend.page * trend.page_size >= trend.total} onClick={() => sessions.loadTrend(trend.page + 1)} className="disabled:opacity-30"><ChevronRight size={14} /></button>
                  </div>
                  <div data-session-scroll tabIndex={0} onKeyDown={navigate} aria-label="会话请求明细" className="min-h-0 flex-1 overflow-auto outline-none">
                    <table className="desk-table desk-dense text-[11px]"><thead><tr><th>#</th><th>时间 / 请求 ID</th><th className="text-right">完整输入</th><th className="text-right">估算成本</th></tr></thead>
                      <tbody>{points.map((p) => <tr key={p.request_id} data-row-id={p.request_id} aria-selected={point?.request_id === p.request_id} onClick={() => sessions.choosePoint(p)}>
                        <td className="font-mono text-fg-muted">{p.ordinal}</td><td title={p.request_id}><span className="text-fg-muted">{formatListTimestamp(p.start_time)}</span><span className="ml-2 font-mono">{p.request_id.slice(0, 14)}</span>
                          {(p.full_input === null || p.cache_read === null) && <span className="ml-2 text-[10px] text-fg-subtle" title={`${p.full_input === null ? '完整输入未记录；' : ''}${p.cache_read === null ? '缓存读取未记录；' : ''}未计入缓存读取占比`}>用量未记录</span>}
                        </td>
                        <td className="text-right font-mono">{formatSessionNumber(p.full_input)}</td><td className="text-right font-mono">{formatSessionCost(p.cost)}</td>
                      </tr>)}</tbody>
                    </table>
                  </div>
                </div>
                <div className="min-w-0 space-y-2 px-3 py-2 text-xs" aria-label="选中会话请求详情">
                  <div className="flex items-baseline justify-between gap-2"><strong className="font-semibold">选中请求</strong><span className="text-fg-muted">第 {point?.ordinal} / {trend.total} 次</span></div>
                  <div className="break-all font-mono text-[11px] text-accent">{point?.request_id}</div>
                  <dl className="grid grid-cols-[72px_minmax(0,1fr)] gap-y-1.5">
                    <dt className="text-fg-muted">完整输入</dt><dd className="font-mono">{formatSessionNumber(point?.full_input)} Token</dd>
                    <dt className="text-fg-muted">缓存读取</dt><dd className="font-mono">{formatSessionNumber(point?.cache_read)} Token</dd>
                    <dt className="text-fg-muted">估算成本</dt><dd className="font-mono">{formatSessionCost(point?.cost)}</dd>
                    <dt className="text-fg-muted">模型</dt><dd className="truncate" title={request?.model}>{request?.model || (lifecycleLoading ? '加载中…' : '—')}</dd>
                    <dt className="text-fg-muted">上游</dt><dd className="truncate">{request?.upstreamName || request?.endpoint || '—'}</dd>
                  </dl>
                  {spike && <div className="border-l-2 border-warn-solid pl-2 text-warn">较前次增加 {formatSessionNumber(point.full_input - point.previous)} Token（{(point.full_input / point.previous).toFixed(1)}×）</div>}
                  {!spike && <div className="text-fg-subtle">{point?.previous == null ? '范围内没有已知前值可比较' : '所选请求未达到输入突增条件'}</div>}
                  {lifecycle?.error && <div role="alert" className="text-danger">请求详情加载失败</div>}
                </div>
              </div>}
    </section>
  );
}
