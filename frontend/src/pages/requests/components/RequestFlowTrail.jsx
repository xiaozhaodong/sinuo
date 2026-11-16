import { ChevronRight } from 'lucide-react';
import { summarizeTerminalReason } from '../utils/lifecycle.js';
import { formatBytesCompact } from '../utils/requestFlow.js';

const DELIVERY = {
  delivered: { label: '已完整交付', dot: 'bg-success' },
  partial_or_failed: { label: '交付不完整', dot: 'bg-warn-solid' },
  aborted: { label: '交付已中止', dot: 'bg-warn-solid' }
};
const SUCCESS = new Set(['completed', 'success', 'ok']);
const hasTime = (ms) => Number.isFinite(ms) && ms >= 0;
// 轨迹相邻节点可能只差几毫秒，秒数也保留毫秒精度。
const formatTime = (ms) => ms < 1000 ? `${ms} ms` : `${(ms / 1000).toFixed(3)} s`;
const targetName = (target) => target?.upstream_name || target?.endpoint_name || target?.upstream_source_name || target?.upstream_type;
const failedAttempt = (contact) => contact?.http_status_code >= 400 || Boolean(contact?.outcome_code && !SUCCESS.has(contact.outcome_code));

function contactSummary(contacts, legsBySeq, legCount, contactCount) {
  if (legCount === 1 && contactCount === 1) return '';
  if (contactCount > 1 && contacts.length === contactCount) {
    const accounts = contacts.map(contact => legsBySeq.get(contact.leg_seq)?.target_snapshot);
    if (accounts.every(target => target?.upstream_type === 'account' && target.upstream_id > 0)) {
      const switches = accounts.slice(1).filter((target, i) => target.upstream_id !== accounts[i].upstream_id).length;
      return switches ? `换号 ${switches} 次 · 联系 ${contactCount} 次` : `同一账号 · 联系 ${contactCount} 次`;
    }
  }
  return `${legCount} 段选路 · ${contactCount} 次联系`;
}

function AttemptList({ contacts, legsBySeq }) {
  return (
    <div>
      <div className="mb-1 text-[11.5px] text-fg-muted">上游尝试</div>
      <ol className="space-y-1">
        {contacts.map((contact, index) => {
          const { contact_seq, leg_seq, outcome_code, http_status_code, start_offset_ms, end_offset_ms } = contact;
          const leg = legsBySeq.get(leg_seq);
          const previous = contacts[index - 1];
          const reason = leg?.reason_code;
          const outcome = SUCCESS.has(outcome_code) ? '完成' : summarizeTerminalReason(outcome_code) || outcome_code;
          const result = [http_status_code > 0 && `HTTP ${http_status_code}`, outcome, contact.transport_resend && '传输重发'].filter(Boolean).join(' · ');
          return (
            <li key={contact_seq} className="flex flex-wrap items-baseline gap-x-2 text-xs">
              <span className="font-mono text-fg-muted">#{contact_seq}</span>
              <span className="min-w-0 break-all font-medium text-fg-body">{targetName(leg?.target_snapshot) || `选路 #${leg_seq}`}</span>
              {result && <span className={failedAttempt(contact) ? 'text-warn' : 'text-fg-muted'}>{result}</span>}
              {hasTime(start_offset_ms) && hasTime(end_offset_ms) && end_offset_ms >= start_offset_ms &&
                <span className="ml-auto whitespace-nowrap font-mono text-fg-muted">{formatTime(end_offset_ms - start_offset_ms)}</span>}
              {reason && previous?.leg_seq !== leg_seq && reason !== previous?.outcome_code &&
                <span className="basis-full break-words pl-5 text-fg-muted">选路原因 · {summarizeTerminalReason(reason) || reason}</span>}
            </li>
          );
        })}
      </ol>
    </div>
  );
}

function Timeline({ points, title }) {
  return (
    <div>
      <div className="mb-1 flex flex-wrap justify-between gap-x-2 text-[11.5px] text-fg-muted">
        <span>{title}</span><span>距请求开始</span>
      </div>
      <ol>
        {points.map(([label, ms, dot]) => (
          <li key={label} className="relative grid min-h-6 grid-cols-[10px_minmax(0,1fr)_auto] items-baseline gap-x-2 text-xs before:absolute before:bottom-[-8px] before:left-[4px] before:top-2 before:w-px before:bg-line last:before:hidden">
            <span aria-hidden="true" className={`relative ml-0.5 size-1.5 self-start translate-y-[6px] rounded-full ${dot || 'border border-fg-muted bg-inspector'}`} />
            <span className="break-words text-fg-body">{label}</span>
            <span className="whitespace-nowrap font-mono tabular-nums text-fg-body">{formatTime(ms)}</span>
          </li>
        ))}
      </ol>
    </div>
  );
}

export default function RequestFlowTrail({ flow, settled }) {
  const legs = Array.isArray(flow.legs) ? flow.legs : [];
  const contacts = Array.isArray(flow.contacts) ? flow.contacts : [];
  const legCount = Math.max(flow.total_legs || 0, legs.length);
  const contactCount = Math.max(flow.total_contacts || 0, contacts.length);
  const legsBySeq = new Map(legs.map(leg => [leg.leg_seq, leg]));
  const lastLeg = legs.at(-1);
  const lastContact = contacts.at(-1);
  // 最后一段可能在真正联系上游前就被本地拒绝，不能借用上一段联系的时刻。
  const contact = lastLeg && lastContact?.leg_seq !== lastLeg.leg_seq ? null : lastContact;
  const name = targetName(lastLeg?.target_snapshot);
  const summary = contactSummary(contacts, legsBySeq, legCount, contactCount);
  const showAttempts = contacts.length > 0 && (contactCount > 1 || failedAttempt(lastContact) || lastContact.http_status_code > 0 || lastContact.transport_resend);
  const delivery = DELIVERY[flow.downstream_delivery_outcome];
  const ws = flow.transport === 'ws';
  const bytes = formatBytesCompact(flow.downstream_bytes_written);
  const points = [
    ['选定上游', !contact && lastLeg?.selected_offset_ms],
    ['开始联系上游', !hasTime(contact?.write_done_offset_ms) && contact?.start_offset_ms],
    [contactCount > 1 && name ? `发往${name}` : '发往上游', contact?.write_done_offset_ms],
    [ws ? '收到上游首帧' : '收到上游首字节', contact?.first_upstream_byte_offset_ms],
    ['首次写给客户端', flow.first_client_write_offset_ms],
    ['收到上游终态', flow.upstream_terminal_offset_ms],
    ['下行交付结束', flow.downstream_delivery_done_offset_ms, delivery?.dot]
  ].filter(([, ms]) => hasTime(ms)).sort((a, b) => a[1] - b[1]);

  return (
    <div className="space-y-1">
      <div className="flex flex-wrap items-baseline gap-x-2 gap-y-1 text-fg-body">
        {flow.transport && <span className="rounded border border-line bg-surface-sub px-1.5 font-mono text-[10.5px]">{ws ? 'WS' : flow.transport.toUpperCase()}</span>}
        {name && <span className="min-w-0 break-all font-medium">{name}</span>}
        {flow.origin === 'server_successor' && <span className="rounded border border-accent-line bg-accent-soft px-1 text-[10.5px] text-accent-fg">服务端续推</span>}
        {delivery && <span className={`inline-flex items-center gap-1.5 ${flow.downstream_delivery_outcome === 'delivered' ? 'text-fg-muted' : 'text-warn'}`}>
          <span aria-hidden="true" className={`size-1.5 rounded-full ${delivery.dot}`} />{delivery.label}
        </span>}
        {bytes && <span className="whitespace-nowrap font-mono text-xs text-fg-muted" title="实际下行数据量"><span className="sr-only">下行 </span>{bytes}</span>}
        {flow.downstream_flush_error && <span className="text-warn">Flush 异常</span>}
      </div>
      {summary && <div className={contactCount > 1 ? 'text-warn' : 'text-fg-muted'}>{summary}</div>}
      <details className="group/trail">
        <summary className="flex w-fit cursor-pointer list-none items-center gap-1 rounded text-xs text-accent-fg hover:underline focus-visible:outline-accent [&::-webkit-details-marker]:hidden">
          <ChevronRight aria-hidden="true" className="size-3 group-open/trail:rotate-90" />
          <span className="group-open/trail:hidden">查看轨迹</span><span className="hidden group-open/trail:inline">收起轨迹</span>
        </summary>
        <div className="mt-2 space-y-2 border-t border-line pt-2">
          {showAttempts && <AttemptList contacts={contacts} legsBySeq={legsBySeq} />}
          {points.length > 0 &&
            <div className={showAttempts ? 'border-t border-line pt-2' : undefined}>
              <Timeline points={points} title={contactCount > 1 ? (settled ? '最终尝试与交付' : '最近尝试与交付') : '请求时间线'} />
            </div>}
          {!showAttempts && !points.length && <span className="text-xs text-fg-muted">暂无已记录的时间点</span>}
        </div>
      </details>
    </div>
  );
}
