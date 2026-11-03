import { useMemo, useRef } from 'react';
import useListKeyboard from '@components/desktop/useListKeyboard.js';
import { useTimezone } from '@contexts/TimezoneContext.jsx';
import { formatSessionCost, formatSessionRatio, isKnown, sessionKey } from '../utils/sessionUsage.js';
import { getRequestFamilyMeta } from '../utils/requestSource.js';

const COLUMNS = [
  { key: 'count', label: '请求数', sortable: true },
  { key: 'cost', label: '估算成本', sortable: true },
  { key: 'missing_costs', label: '费用缺失' },
  { key: 'cache', label: '缓存读取占比', sortable: true },
  { key: 'missing_cache_usage', label: '用量缺失' },
  { key: 'recent', label: '最近活动', sortable: true }
];

export default function SessionTable({ sessions }) {
  const { formatListTimestamp } = useTimezone();
  const containerRef = useRef(null);
  const ids = useMemo(() => sessions.rows.map(sessionKey), [sessions.rows]);
  const navigate = useListKeyboard({ ids, selectedId: sessions.selectedKey,
    onSelect: (id) => sessions.select(sessions.rows.find((row) => sessionKey(row) === id)), containerRef });
  return (
    <div ref={containerRef} tabIndex={0} onKeyDown={navigate} aria-label="会话排行" className="min-h-full outline-none">
      <table className="desk-table desk-dense" aria-label="会话排行表">
        <thead><tr>
          <th className="w-12">#</th><th>请求族</th><th>会话 ID</th>
          {COLUMNS.map(({key, label, sortable}) => <th key={key} className="text-right" aria-sort={sortable ? sessions.sortBy === key ? sessions.sortOrder === 'desc' ? 'descending' : 'ascending' : 'none' : undefined}>
            {sortable ? <button type="button" onClick={() => sessions.sort(key)} className={sessions.sortBy === key ? 'text-accent' : ''}>{label}{sessions.sortBy === key ? sessions.sortOrder === 'desc' ? ' ↓' : ' ↑' : ''}</button> : <span title={`未记录${key === 'missing_costs' ? '成本' : '完整输入或缓存读取'}的请求数`}>{label}</span>}
          </th>)}
        </tr></thead>
        <tbody>
          {sessions.loading ? <tr><td colSpan={9} className="h-24 text-center text-fg-subtle">加载会话排行…</td></tr>
            : sessions.rows.length === 0 ? <tr><td colSpan={9} className="h-24 text-center text-fg-subtle">当前范围没有带会话 ID 的请求</td></tr>
              : sessions.rows.map((row, index) => {
                const badge = getRequestFamilyMeta(row.request_family);
                return <tr key={sessionKey(row)} data-row-id={sessionKey(row)}
                aria-selected={sessions.selectedKey === sessionKey(row)}
                onClick={() => sessions.select(row)} onDoubleClick={() => sessions.openDetail?.()}>
                <td className="font-mono text-fg-subtle">{(sessions.page - 1) * sessions.size + index + 1}</td>
                <td><span className={`inline-flex items-center rounded-md border px-1.5 py-[3px] text-xs leading-4 font-semibold ${badge.className}`}><span>{badge.label}</span></span></td>
                <td title={row.session_id} className="font-mono">{row.session_id.slice(0, 8)}{row.session_id.length > 8 ? '…' : ''}</td>
                <td className="text-right font-mono">{row.request_count.toLocaleString()}</td>
                <td className={`text-right font-mono ${isKnown(row.total_cost_usd) ? 'text-warn font-medium' : 'text-fg-subtle'}`} title={`已记录成本合计，已知 ${row.request_count - row.missing_costs} / ${row.request_count} 条；不重新计价`}>
                  {formatSessionCost(row.total_cost_usd)}
                </td>
                <td className={`text-right font-mono ${row.missing_costs > 0 ? 'text-warn' : 'text-fg-subtle'}`} title="未记录成本的请求数">{row.missing_costs}</td>
                <td className={`text-right font-mono ${isKnown(row.cache_ratio) ? 'text-success font-medium' : 'text-fg-subtle'}`} title={`缓存用量已知 ${row.request_count - row.missing_cache_usage} / ${row.request_count} 条；仅对完整输入与缓存读取均已知的请求求和后相除`}>
                  {formatSessionRatio(row.cache_ratio)}
                </td>
                <td className={`text-right font-mono ${row.missing_cache_usage > 0 ? 'text-warn' : 'text-fg-subtle'}`} title="未记录完整输入或缓存读取的请求数">{row.missing_cache_usage}</td>
                <td className="text-right font-mono text-fg-muted">{formatListTimestamp(row.last_activity)}</td>
              </tr>; })}
        </tbody>
      </table>
    </div>
  );
}
