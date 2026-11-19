import { Fragment } from 'react';
import { ArrowDown, ArrowRight, Check, ChevronDown, CircleAlert, Clock, X } from 'lucide-react';
import { Property, PropertyList } from '@components/desktop/Inspector.jsx';
import { summarizeTerminalReason } from '../utils/lifecycle.js';
import { formatBytesCompact } from '../utils/requestFlow.js';
import { formatTimingBadge } from '../utils/timing.js';

const NODE_STYLE = {
  neutral: { marker: 'border-line-strong bg-inspector', text: 'text-fg-muted' },
  active: { marker: 'border-accent-line bg-accent-soft', text: 'text-accent-fg' },
  warning: { marker: 'border-warn-line bg-warn-soft', text: 'text-warn' },
  failure: { marker: 'border-danger-line bg-danger-soft', text: 'text-danger' },
  success: { marker: 'border-success-line bg-success-soft', text: 'text-success' }
};

const DELIVERY = {
  delivered: { label: '已完整交付', tone: 'success', icon: Check },
  partial_or_failed: { label: '交付不完整', tone: 'warning', icon: CircleAlert },
  aborted: { label: '交付已中止', tone: 'warning', icon: CircleAlert }
};

const SUCCESS = new Set(['completed', 'success', 'ok']);
const hasTime = (ms) => Number.isFinite(ms) && ms >= 0;
// 分节点的明细保留毫秒精度，便于区分只差几毫秒的事件。
const formatTime = (ms) => ms < 1000 ? `${ms} ms` : `${(ms / 1000).toFixed(3)} s`;
const offsetBadge = (ms) => hasTime(ms) ? `+${formatTimingBadge(ms)}` : null;
const targetName = (target) => target?.upstream_name || target?.endpoint_name || target?.upstream_source_name || target?.upstream_type || '上游';

function timingFacts(points) {
  return points
    .filter(([, ms]) => hasTime(ms))
    .sort(([, a], [, b]) => a - b)
    .map(([label, ms]) => [label, `+${formatTime(ms)}`]);
}

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

function targetIdentity(target) {
  if (target?.upstream_type === 'endpoint') {
    const name = target.endpoint_name || target.upstream_name;
    return name ? `endpoint:${name}` : null;
  }
  if (target?.upstream_type && target.upstream_id > 0) return `${target.upstream_type}:${target.upstream_id}`;
  return null;
}

function contactRelation(contact, previous, target, previousTarget) {
  if (contact.transport_resend) return '传输重发';
  // 裁剪留下的首尾记录之间可能还有别的上游。
  if (previous?.contact_seq !== contact.contact_seq - 1) return null;
  const identity = targetIdentity(target);
  const previousIdentity = targetIdentity(previousTarget);
  const type = target?.upstream_type === 'endpoint' ? '端点' : target?.upstream_type === 'account' ? '账号' : '上游';
  if ((contact.leg_seq > 0 && contact.leg_seq === previous.leg_seq) || (identity && identity === previousIdentity)) {
    return `同${type}重试`;
  }
  return identity && previousIdentity ? `切换${type}` : null;
}

function contactState(contact, settled) {
  const { http_status_code: status, outcome_code: outcome, end_offset_ms: end } = contact;
  const failureCode = outcome && !SUCCESS.has(outcome) ? outcome : null;
  if (status >= 400 || failureCode) {
    const reason = failureCode || `HTTP ${status}`;
    if (status === 429) return { label: '限流', tone: 'warning', icon: CircleAlert, reason };
    return { label: summarizeTerminalReason(failureCode) || '失败', tone: 'failure', icon: X, reason };
  }
  if (SUCCESS.has(outcome)) return { label: '响应完成', tone: 'success', icon: Check };
  if (hasTime(end)) return { label: '已结束', tone: 'neutral', icon: Clock };
  if (settled) return { label: '结果未记录', tone: 'neutral', icon: Clock };
  return { label: '联系中', tone: 'active', icon: Clock };
}

function TraceNode({ icon: Icon, tone = 'neutral', children }) {
  const style = NODE_STYLE[tone];
  return (
    <li className="relative grid grid-cols-[20px_minmax(0,1fr)] items-start gap-2 after:pointer-events-none after:absolute after:-bottom-2.5 after:left-[9px] after:top-6 after:w-px after:bg-line-strong last:after:hidden">
      <span className={`relative z-1 mt-1 grid size-5 place-items-center rounded-full border ${style.marker} ${style.text}`}>
        <Icon size={12} strokeWidth={2} aria-hidden="true" />
      </span>
      {children}
    </li>
  );
}

function TraceFacts({ rows }) {
  return (
    <PropertyList labelWidth={72} align="baseline">
      {rows.map(([label, value]) => (
        <Property key={label} label={label} truncate={false}>
          <span className="font-mono text-[11px] text-fg-body [overflow-wrap:anywhere]">{value}</span>
        </Property>
      ))}
    </PropertyList>
  );
}

function TraceCard({ title, meta, time, timeLabel = '距请求开始', times, facts = [] }) {
  const timedFacts = timingFacts(times);
  const knownFacts = facts.filter(([, value]) => value);
  const expandable = knownFacts.length > 0 || timedFacts.length > 0;
  const cardClass = 'min-w-0 rounded-md border border-line bg-surface';
  const headerClass = 'flex min-w-0 items-start gap-2 rounded-md px-2.5 py-1.5';
  const heading = (
    <>
      <span className="min-w-0 flex-1">
        <span className="block text-[12px] font-semibold leading-4.5 text-fg [overflow-wrap:anywhere]">{title}</span>
        {meta && <span className="flex flex-wrap items-center gap-x-1.5 text-[11px] leading-4 text-fg-muted [overflow-wrap:anywhere]">{meta}</span>}
      </span>
      <span className="flex shrink-0 items-center gap-1 pt-px">
        {time && <span title={`${timeLabel} ${time}`} className="font-mono text-[11px] tabular-nums text-fg-body">
          <span className="sr-only">{timeLabel} </span>{time}
        </span>}
        {expandable && <ChevronDown size={13} aria-hidden="true" className="text-fg-muted group-open/node:rotate-180 motion-safe:transition-transform" />}
      </span>
    </>
  );
  if (!expandable) return <div className={cardClass}><div className={headerClass}>{heading}</div></div>;

  return (
    <details className={`group/node open:border-line-strong ${cardClass}`}>
      <summary className={`${headerClass} cursor-pointer list-none hover:bg-surface-sub focus-visible:outline-2 focus-visible:-outline-offset-2 focus-visible:outline-accent [&::-webkit-details-marker]:hidden`}>
        {heading}
      </summary>
      <div className="space-y-2 border-t border-line-soft px-2.5 py-2 [&_dl]:gap-y-1">
        {knownFacts.length > 0 && <TraceFacts rows={knownFacts} />}
        {timedFacts.length > 0 && (
          <div>
            <p className="mb-1.5 text-[11px] text-fg-muted">距请求开始</p>
            <TraceFacts rows={timedFacts} />
          </div>
        )}
      </div>
    </details>
  );
}

function ContactNode({ contact, target, reason, relation, settled, ws }) {
  const state = contactState(contact, settled);
  const { start_offset_ms: start, end_offset_ms: end } = contact;
  const duration = hasTime(start) && hasTime(end) && end >= start ? end - start : null;
  const httpStatus = contact.http_status_code > 0 ? `HTTP ${contact.http_status_code}` : null;
  const title = !target && contact.leg_seq > 0 ? `选路 #${contact.leg_seq}` : targetName(target);
  return (
    <TraceNode icon={state.icon} tone={state.tone}>
      <TraceCard
        title={title}
        time={duration === null ? null : formatTimingBadge(duration)}
        timeLabel="本次尝试耗时"
        meta={<>
          <span className={`font-medium ${NODE_STYLE[state.tone].text}`}>{state.label}</span>
          {httpStatus && <span className="font-mono whitespace-nowrap">{httpStatus}</span>}
          {relation && <span className="border-l border-line pl-1.5">{relation}</span>}
        </>}
        times={[
          ['开始联系', start],
          ['连接就绪', contact.conn_ready_offset_ms],
          ['开始写出', contact.write_start_offset_ms],
          ['写完请求', contact.write_done_offset_ms],
          ['收到响应头', contact.response_headers_offset_ms],
          [ws ? '上游首帧' : '上游首字节', contact.first_upstream_byte_offset_ms],
          ['联系结束', end]
        ]}
        facts={[
          ['失败原因', state.reason],
          ['本次耗时', duration === null ? null : formatTime(duration)],
          ['选路原因', reason && (summarizeTerminalReason(reason) || reason)]
        ]}
      />
    </TraceNode>
  );
}

function DeliveryNode({ flow, settled }) {
  const bytes = formatBytesCompact(flow.downstream_bytes_written);
  const end = flow.downstream_delivery_done_offset_ms;
  const started = hasTime(flow.first_client_write_offset_ms) || Boolean(bytes);
  const state = DELIVERY[flow.downstream_delivery_outcome] || {
    label: settled ? '交付结果未记录' : started ? '交付中' : '等待下行交付',
    tone: !settled && started ? 'active' : 'neutral',
    icon: Clock
  };
  const detail = [bytes && `下行 ${bytes}`, flow.downstream_flush_error && 'Flush 异常'].filter(Boolean).join(' · ');
  return (
    <TraceNode icon={state.icon} tone={state.tone}>
      <TraceCard
        title={state.label}
        time={offsetBadge(end)}
        meta={detail}
        times={[
          ['首次下行', flow.first_client_write_offset_ms],
          ['上游终态', flow.upstream_terminal_offset_ms],
          ['交付结束', end]
        ]}
      />
    </TraceNode>
  );
}

function ContactGap({ count }) {
  return (
    <TraceNode icon={CircleAlert}>
      <p className="py-1.5 text-xs text-fg-muted">{count} 次联系未展示</p>
    </TraceNode>
  );
}

export default function RequestFlowTrail({ flow, settled }) {
  const legs = Array.isArray(flow.legs) ? flow.legs : [];
  const contacts = Array.isArray(flow.contacts) ? flow.contacts : [];
  const legsBySeq = new Map(legs.map(leg => [leg.leg_seq, leg]));
  const lastLeg = legs.at(-1);
  const lastContact = contacts.at(-1);
  const lastContactSeq = Math.max(lastContact?.contact_seq || 0, contacts.length);
  const contactCount = Math.max(flow.total_contacts || 0, contacts.length + (flow.omitted_contacts || 0), lastContactSeq);
  const legCount = Math.max(flow.total_legs || 0, legs.length + (flow.omitted_legs || 0));
  const summary = contactSummary(contacts, legsBySeq, legCount, contactCount);
  // 最后一段可能在联系上游前被本地拒绝，只展示这段自身的选路记录。
  const lastSelection = lastLeg && lastLeg.leg_seq > (lastContact?.leg_seq ?? 0) ? lastLeg : null;

  return (
    <ol aria-label="请求执行顺序" className="mt-0.5 space-y-1.5">
      <TraceNode icon={ArrowDown}>
        <div className="flex flex-wrap items-center gap-x-1.5 gap-y-0.5 py-1.5 text-xs">
          <span className="font-medium text-fg-body">{flow.origin === 'server_successor' ? '服务端续推' : '收到请求'}</span>
          {flow.transport && <span className="rounded border border-line bg-surface-sub px-1 font-mono text-[10px] text-fg-muted">{flow.transport.toUpperCase()}</span>}
          {summary && <span className="basis-full text-[11px] text-fg-muted">{summary}</span>}
        </div>
      </TraceNode>
      {contacts.map((contact, index) => {
        const leg = legsBySeq.get(contact.leg_seq);
        const previous = contacts[index - 1];
        const target = leg?.target_snapshot;
        const previousTarget = legsBySeq.get(previous?.leg_seq)?.target_snapshot;
        const omitted = contact.contact_seq - (previous?.contact_seq ?? 0) - 1;
        return (
          <Fragment key={contact.contact_seq}>
            {omitted > 0 && <ContactGap count={omitted} />}
            <ContactNode
              contact={contact}
              target={target}
              reason={previous?.leg_seq !== contact.leg_seq ? leg?.reason_code : null}
              relation={contactRelation(contact, previous, target, previousTarget)}
              settled={settled}
              ws={flow.transport === 'ws'}
            />
          </Fragment>
        );
      })}
      {contactCount > lastContactSeq && <ContactGap count={contactCount - lastContactSeq} />}
      {lastSelection && (
        <TraceNode icon={ArrowRight}>
          <TraceCard
            title={targetName(lastSelection.target_snapshot)}
            meta="已选定上游"
            time={offsetBadge(lastSelection.selected_offset_ms)}
            times={[['选定上游', lastSelection.selected_offset_ms]]}
            facts={[['选路原因', summarizeTerminalReason(lastSelection.reason_code) || lastSelection.reason_code]]}
          />
        </TraceNode>
      )}
      <DeliveryNode flow={flow} settled={settled} />
    </ol>
  );
}
