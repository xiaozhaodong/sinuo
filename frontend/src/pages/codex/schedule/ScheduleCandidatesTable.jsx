import { Info } from 'lucide-react';
import ListEmpty from '@components/desktop/ListEmpty.jsx';
import {
  QUOTA_STATUS_STYLE, SCHEDULE_DECISION_STYLE, SCHEDULE_OUTCOME_STYLE,
  toQuotaStatusLabel, toScheduleDecisionLabel, toScheduleOutcomeLabel, toScheduleReasonLabel
} from '@pages/account-pool/utils.js';

const BADGE = 'rounded border px-1.5 text-[11px] leading-[18px]';

const CandidateRow = ({ row, index }) => (
  <tr>
    <td className="tabular-nums text-fg-muted">{index + 1}</td>
    <td className="font-medium text-fg">{row.accountName}</td>
    <td className="text-xs text-fg-muted">{row.groupLabel}{Number.isFinite(row.priority) ? ` · 组内顺序 ${row.priority}` : ''}</td>
    <td><span className={`${BADGE} ${SCHEDULE_DECISION_STYLE[row.decision] || SCHEDULE_DECISION_STYLE.skipped}`}>{toScheduleDecisionLabel(row.decision)}</span></td>
    <td>{row.runtimeOutcome
      ? <span className={`${BADGE} ${SCHEDULE_OUTCOME_STYLE[row.runtimeOutcome] || SCHEDULE_OUTCOME_STYLE.pending}`}>{toScheduleOutcomeLabel(row.runtimeOutcome)}</span>
      : <span className="text-xs text-fg-subtle">—</span>}</td>
    <td className="text-xs text-fg-body">
      {toScheduleReasonLabel(row.reason)}
      {row.reasonDetail && <p className="text-[11.5px] text-fg-muted">{row.reasonDetail}</p>}
      {row.runtimeError && <p className="selectable text-[11.5px] text-danger">{row.runtimeError}</p>}
    </td>
    <td>
      <span className={`${BADGE} ${QUOTA_STATUS_STYLE[row.quotaStatus] || QUOTA_STATUS_STYLE.pending}`}>{toQuotaStatusLabel(row.quotaStatus)}</span>
      {Number.isFinite(row.effectiveQuotaRemaining) && <span className="ml-1.5 text-xs tabular-nums text-fg-body">{row.effectiveQuotaRemaining.toFixed(0)}%</span>}
    </td>
    <td className={`text-xs tabular-nums ${row.failCount !== 0 ? 'text-warn' : 'text-fg-muted'}`}>{row.failCount}</td>
  </tr>
);

const ScheduleCandidatesTable = ({ snapshot, snapshotUnsupported, rows }) => {
  if (snapshotUnsupported) return <ListEmpty title={snapshot?.message || '当前后端版本暂未提供最近一次调度结果接口。'} />;
  if (snapshot?.hasSnapshot !== true && snapshot?.has_snapshot !== true) {
    return <ListEmpty icon={Info} title="暂无最近一次调度结果" description="发起一次 `/v1/responses` 请求后，这里会显示命中账号、命中组别、组内顺序、是否降级以及候选账号的跳过原因。" />;
  }
  if (rows.length === 0) return <ListEmpty title="当前快照没有候选账号决策明细。" />;
  return (
    <table className="desk-table desk-dense">
      <thead><tr>
        <th className="w-[36px]">#</th><th>账号</th><th>组</th><th>决策</th><th>运行结果</th><th>原因</th><th>额度</th><th>失败</th>
      </tr></thead>
      <tbody>{rows.map((row, index) => <CandidateRow key={`${row.id}-${index}`} row={row} index={index} />)}</tbody>
    </table>
  );
};

export default ScheduleCandidatesTable;
