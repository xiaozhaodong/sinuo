import { useTimezone } from '@contexts/TimezoneContext.jsx';
import { resolveReasonLabel, runtimeLabels } from './scheduleSnapshotView.js';

const BADGE = 'rounded border px-1.5 text-[11px] leading-[18px]';

const DecisionRow = ({ decision, index, formatListTimestamp }) => {
  const candidate = decision.decision === 'candidate';
  const runtime = decision.runtimeOutcome;
  const runtimeTone = runtime === 'success' ? 'tone-emerald' : runtime === 'attempting' ? 'tone-blue' : 'tone-amber';
  return (
    <tr>
      <td className="tabular-nums text-fg-muted">{index + 1}</td>
      <td className="font-medium text-fg">{decision.name}</td>
      <td><span className={`${candidate ? 'tone-indigo' : 'tone-slate'} ${BADGE}`}>{candidate ? '候选' : '跳过'}</span></td>
      <td>{runtime ? <span className={`${runtimeTone} ${BADGE}`}>{runtimeLabels[runtime] || runtime}</span> : <span className="text-fg-subtle">—</span>}</td>
      <td className="text-xs text-fg-body">
        {resolveReasonLabel(decision.reason)}
        {decision.runtimeError && <div className="selectable text-[11.5px] text-danger">{decision.runtimeError}</div>}
      </td>
      <td className={`text-xs tabular-nums ${decision.availableAt ? 'text-warn' : 'text-fg-subtle'}`}>
        {decision.availableAt ? formatListTimestamp(decision.availableAt) : '—'}
      </td>
    </tr>
  );
};

const EndpointDecisionsTable = ({ decisions }) => {
  const { formatListTimestamp } = useTimezone();
  return (
    <table className="desk-table desk-dense" aria-label="端点调度决策">
      <thead>
        <tr>
          <th className="w-9">#</th><th>端点</th><th>决策</th><th>运行结果</th><th>原因</th><th>预计恢复</th>
        </tr>
      </thead>
      <tbody>
        {decisions.map((decision, index) => <DecisionRow key={`${decision.name}-${index}`} decision={decision} index={index} formatListTimestamp={formatListTimestamp} />)}
      </tbody>
    </table>
  );
};

export default EndpointDecisionsTable;
