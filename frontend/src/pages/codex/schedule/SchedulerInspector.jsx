import { Button } from '@components/ui';
import { Inspector, InspectorHeader, InspectorBody, InspectorSection, PropertyList, Property } from '@components/desktop/Inspector.jsx';
import { useTimezone } from '@contexts/TimezoneContext.jsx';
import { SCHEDULE_OUTCOME_STYLE, normalizePriorityValue, toScheduleOutcomeLabel } from '@pages/account-pool/utils.js';

const ModeSection = ({ summary, busyKey, enableAutoSelection }) => {
  const manual = summary.selectionMode === 'manual';
  return (
    <InspectorSection title="调度模式">
      <PropertyList labelWidth={96}>
        <Property label="当前模式">{manual ? '手动' : 'Auto'}</Property>
        {manual && <Property label="全局固定" title={summary.pinnedAccountName}>{summary.pinnedAccountName || '—'}</Property>}
      </PropertyList>
      {manual && (
        <div className="mt-2.5 flex justify-end">
          <Button type="button" variant="primary" size="sm" disabled={busyKey === 'scheduler-enable-auto'} onClick={() => enableAutoSelection()}>启用编排</Button>
        </div>
      )}
      <p className="mt-2 text-[11.5px] leading-[17px] text-fg-muted">
        {manual
          ? `全局固定「${summary.pinnedAccountName}」，严格不可用前不切走。启用编排后清除全局固定，按 主组 → 备组 → 冷备 自动调度；组内首选不受影响。`
          : '按 主组 → 备组 → 冷备 自动调度。悬停账号行点「固定」可切到手动。'}
      </p>
    </InspectorSection>
  );
};

const RuntimeSection = ({ summary }) => (
  <InspectorSection title="运行态">
    <PropertyList labelWidth={96}>
      <Property label="当前运行组">{summary.currentGroupLabel}</Property>
      <Property label="活跃账号" title={summary.activeAccountName}>{summary.activeAccountName}</Property>
      <Property label="降级"><span className={summary.degraded ? 'text-warn' : 'text-success'}>{summary.degraded ? '已降级' : '未降级'}</span></Property>
    </PropertyList>
  </InspectorSection>
);

const LatestSection = ({ snapshot, snapshotUnsupported, view, onViewCandidates }) => {
  const { formatTimestamp } = useTimezone();
  const hasSnapshot = snapshot?.hasSnapshot === true || snapshot?.has_snapshot === true;
  const outcome = String(snapshot?.finalOutcome || snapshot?.final_outcome || '').trim().toLowerCase() || 'pending';
  const priority = normalizePriorityValue(snapshot?.selectedPriority ?? snapshot?.selected_priority);
  const hit = `${snapshot?.selectedAccountName || snapshot?.selected_account_name || '-'} · ${snapshot?.selectedTierLabel || snapshot?.selected_tier_label || '-'}${Number.isFinite(priority) ? ` · 顺序 ${priority}` : ''}`;
  const time = snapshot?.updatedAt || snapshot?.updated_at || snapshot?.capturedAt || snapshot?.captured_at;
  const finalError = snapshot?.finalError || snapshot?.final_error;
  return (
    <InspectorSection title="最近一次调度">
      {snapshotUnsupported || !hasSnapshot ? (
        <p className="text-xs text-fg-muted">{snapshotUnsupported ? snapshot?.message || '当前后端版本暂未提供最近一次调度结果接口。' : '暂无最近一次调度结果'}</p>
      ) : (
        <>
          <PropertyList labelWidth={96}>
            <Property label="结论"><span className={`rounded border px-1.5 text-[11px] leading-[18px] ${SCHEDULE_OUTCOME_STYLE[outcome] || SCHEDULE_OUTCOME_STYLE.pending}`}>{toScheduleOutcomeLabel(outcome)}</span></Property>
            <Property label="命中" title={hit}>{hit}</Property>
            <Property label="请求" title={snapshot?.requestPath || snapshot?.request_path || '/v1/responses'}><span className="font-mono">{snapshot?.requestPath || snapshot?.request_path || '/v1/responses'}</span></Property>
            <Property label="时间">{time ? formatTimestamp(time) : '—'}</Property>
          </PropertyList>
          {finalError && <p className="selectable mt-2.5 rounded-md border border-warn-line bg-warn-soft px-2.5 py-1.5 text-xs text-warn">{finalError}</p>}
          {view === 'arrange' && <button type="button" className="mt-2.5 text-xs text-accent" onClick={onViewCandidates}>查看候选链路 →</button>}
        </>
      )}
    </InspectorSection>
  );
};

const SchedulerInspector = ({ summary, snapshot, snapshotUnsupported, busyKey, enableAutoSelection, view, onViewCandidates }) => {
  const manual = summary.selectionMode === 'manual';
  return (
    <Inspector label="调度编排检查器">
      <InspectorHeader title="调度编排" badges={<span className={`rounded border px-1.5 text-[11px] leading-[18px] ${manual ? 'tone-rose' : 'tone-emerald'}`}>{manual ? '手动' : 'Auto'}</span>} />
      <InspectorBody>
        <ModeSection summary={summary} busyKey={busyKey} enableAutoSelection={enableAutoSelection} />
        <RuntimeSection summary={summary} />
        <LatestSection snapshot={snapshot} snapshotUnsupported={snapshotUnsupported} view={view} onViewCandidates={onViewCandidates} />
      </InspectorBody>
    </Inspector>
  );
};

export default SchedulerInspector;
