// ============================================
// 调度编排抽屉
// 2026-03-22
// ============================================

import { Button } from '@components/ui';
import { FormRow, FormSection } from '@components/desktop/FormSection.jsx';
import SidePanel from '@components/desktop/SidePanel.jsx';
import AccountPoolSchedulerSection from './AccountPoolSchedulerSection.jsx';
import LatestScheduleSnapshotCard from './LatestScheduleSnapshotCard.jsx';

const SchedulerDrawer = ({
  open = false,
  onClose,
  scheduler = {},
  latestScheduleSnapshot = {},
  snapshotUnsupported = false,
  onSwapGroup,
  onSetActiveAccount,
  onPinAccountSelection,
  onEnableAutoSelection,
  onViewInInventory,
  busyKey = ''
}) => {
  if (!open) return null;

  const summary = scheduler?.summary || {};
  const degraded = Boolean(summary?.degraded);
  const isManualMode = summary?.selectionMode === 'manual';

  return (
    <SidePanel
      open={open}
      onClose={onClose}
      label="调度编排"
      title="调度编排"
      subtitle="主组 / 备组 / 冷备的运行态与编排动作"
      width={680}
      layer={45}
      closeOnOverlay={true}
      overlayLabel="关闭调度编排"
    >
      <FormSection title="当前模式">
        <FormRow label={summary?.selectionModeLabel || 'Auto 模式'} description={isManualMode
          ? '当前为手动模式，可在下方账号行内重新指定手动账号，或切回按编排自动调度。'
          : '当前为 Auto 模式，可在下方任一账号行内指定手动账号。'}
        >
          <Button
            type="button"
            variant={isManualMode ? 'secondary' : 'primary'}
            size="sm"
            disabled={busyKey === 'scheduler-enable-auto' || !isManualMode}
            onClick={() => onEnableAutoSelection?.()}
          >
            启用编排
          </Button>
        </FormRow>
        {isManualMode && summary?.pinnedAccountName ? (
          <FormRow label="全局手动">
            <span className="text-xs text-fg-body">{`全局手动：${summary.pinnedAccountName}`}</span>
          </FormRow>
        ) : null}
        <FormRow label="最近命中">
          <span className="tone-indigo rounded border px-1.5 text-[11px] leading-[18px]">
            {`最近命中：${summary?.activeAccountName || '暂无'}`}
          </span>
          <span className="rounded border border-line px-1.5 text-[11px] leading-[18px] text-fg-body">
            {summary?.currentGroupLabel || '待确认'}
          </span>
          <span className={`${degraded ? 'tone-amber' : 'tone-emerald'} rounded border px-1.5 text-[11px] leading-[18px]`}>
            {degraded ? '已降级' : '未降级'}
          </span>
        </FormRow>
      </FormSection>
      <AccountPoolSchedulerSection
        scheduler={scheduler}
        onSwapGroup={onSwapGroup}
        onSetActiveAccount={onSetActiveAccount}
        onPinAccountSelection={onPinAccountSelection}
        onViewInInventory={onViewInInventory}
        busyKey={busyKey}
        embedded
      />
      <LatestScheduleSnapshotCard snapshot={latestScheduleSnapshot} snapshotUnsupported={snapshotUnsupported} />
    </SidePanel>
  );
};

export default SchedulerDrawer;
