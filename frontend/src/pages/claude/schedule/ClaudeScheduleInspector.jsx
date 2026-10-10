import { Activity } from 'lucide-react';
import { useTimezone } from '@contexts/TimezoneContext.jsx';
import {
  Inspector, InspectorBody, InspectorEmpty, InspectorHeader, InspectorSection, Property, PropertyList
} from '@components/desktop/Inspector.jsx';
import { resolveEffectiveFailover, resolveRouteScopeLabel, resolveSnapshotOutcome, routeModeLabels } from './scheduleSnapshotView.js';

const RequestSection = ({ snapshot, formatTimestamp }) => (
  <InspectorSection title="请求">
    <PropertyList labelWidth={96}>
      <Property label="请求" title={snapshot.requestPath}><span className="font-mono">{snapshot.requestPath || '-'}</span></Property>
      {snapshot.requestModel && <Property label="模型" title={snapshot.requestModel}><span className="font-mono">{snapshot.requestModel}</span></Property>}
      <Property label="路由范围">{resolveRouteScopeLabel(snapshot)}</Property>
      <Property label="捕获时间" title={snapshot.capturedAt ? formatTimestamp(snapshot.capturedAt) : undefined}>
        {snapshot.capturedAt ? formatTimestamp(snapshot.capturedAt) : '-'}
      </Property>
    </PropertyList>
  </InspectorSection>
);

const ResultSection = ({ snapshot }) => {
  const failover = resolveEffectiveFailover(snapshot);
  const { isAbnormal } = resolveSnapshotOutcome(snapshot);
  return (
    <InspectorSection title="结果">
      <p className={`mb-2 text-[12.5px] leading-[18px] ${isAbnormal ? 'text-warn' : 'text-fg-body'}`}>
        {snapshot.summary || '端点调度已完成'}
      </p>
      <PropertyList labelWidth={96}>
        <Property label="实际命中" title={snapshot.selectedEndpoint}><span className="font-medium text-fg">{snapshot.selectedEndpoint || '-'}</span></Property>
        <Property label="路由模式">{routeModeLabels[snapshot.routeMode] || snapshot.routeMode || '自动调度'}</Property>
        <Property label="路由目标" title={snapshot.routeEndpointName}>{snapshot.routeEndpointName || '按自动策略选择'}</Property>
        <Property label="请求内故障转移"><span className={failover.enabled ? 'text-success' : 'text-fg-muted'}>{failover.label}</span></Property>
      </PropertyList>
    </InspectorSection>
  );
};

const ClaudeScheduleInspector = ({ snapshot, unsupported }) => {
  const { formatTimestamp } = useTimezone();
  const outcome = resolveSnapshotOutcome(snapshot);
  return (
    <Inspector label="最近一次调度">
      {unsupported || !snapshot.hasSnapshot ? (
        <InspectorEmpty icon={Activity}>{unsupported ? '端点调度快照暂不可用' : '暂无端点调度记录'}</InspectorEmpty>
      ) : (
        <>
          <InspectorHeader
            title="最近一次调度"
            badges={<span className={`${outcome.className} shrink-0 rounded border px-1.5 text-[11px] leading-[18px]`}>{outcome.label}</span>}
            subtitle={snapshot.requestId ? <span className="selectable">{snapshot.requestId}</span> : undefined}
          />
          <InspectorBody>
            <RequestSection snapshot={snapshot} formatTimestamp={formatTimestamp} />
            <ResultSection snapshot={snapshot} />
            {snapshot.finalError && (
              <InspectorSection title="最终信息">
                <div className="selectable rounded-md border border-warn-line bg-warn-soft px-2.5 py-1.5 text-xs text-warn">{snapshot.finalError}</div>
              </InspectorSection>
            )}
          </InspectorBody>
        </>
      )}
    </Inspector>
  );
};

export default ClaudeScheduleInspector;
