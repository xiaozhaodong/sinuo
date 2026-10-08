// ============================================
// Account Pool 调度编排区块
// 2026-03-21
// ============================================

import { ArrowRight, GitBranch, ShieldAlert } from 'lucide-react';
import { Button } from '@components/ui';
import { FormBlock, FormRow, FormSection } from '@components/desktop/FormSection.jsx';
import GroupBoardCard from './GroupBoardCard.jsx';

const GROUP_ORDER = {
  primary: 0,
  backup: 1,
  cold: 2
};

const normalizeGroupKey = (key = '') => {
  const normalized = String(key || '').trim().toLowerCase();

  if (normalized === 'primary' || normalized.includes('main')) return 'primary';
  if (normalized === 'backup' || normalized.includes('secondary')) return 'backup';
  return 'cold';
};

const DEFAULT_GROUPS = [
  { key: 'primary', label: '主组', accounts: [], healthSummary: '主组当前没有候选账号', actions: [] },
  { key: 'backup', label: '备组', accounts: [], healthSummary: '备组等待承接降级流量', actions: [] },
  { key: 'cold', label: '冷备', accounts: [], healthSummary: '冷备保留为极端情况下的补位组', actions: [] }
];

const normalizeGroups = (groups = []) => {
  const mergedGroups = new Map(DEFAULT_GROUPS.map((group) => [group.key, group]));

  for (const group of Array.isArray(groups) ? groups : []) {
    const key = normalizeGroupKey(group?.key);
    mergedGroups.set(key, {
      ...mergedGroups.get(key),
      ...group,
      key,
      label: group?.label || mergedGroups.get(key)?.label || '调度组'
    });
  }

  return Array.from(mergedGroups.values()).sort((left, right) => GROUP_ORDER[left.key] - GROUP_ORDER[right.key]);
};

const AccountPoolSchedulerSection = ({
  scheduler = {},
  onSwapGroup,
  onSetActiveAccount,
  onPinAccountSelection,
  onViewInInventory,
  busyKey = '',
  onOpenSnapshot,
  embedded = false
}) => {
  const summary = scheduler?.summary || {};
  const latestSnapshot = scheduler?.latestSnapshot || {};
  const groups = normalizeGroups(scheduler?.groups);
  const degraded = Boolean(summary?.degraded || latestSnapshot?.degraded);

  const summaryCards = (
    <FormSection title="运行态">
      <FormRow label="当前运行组"><span className="text-xs font-medium text-fg-body">{summary?.currentGroupLabel || '待确认'}</span></FormRow>
      <FormRow label="当前活跃账号"><span className="text-xs font-medium text-fg-body">{summary?.activeAccountName || '暂无'}</span></FormRow>
      <FormRow label="最新结论">
        <span className="text-xs font-medium text-fg-body">{summary?.finalOutcomeLabel || '等待最近一次调度'}</span>
        <span className={"rounded border px-1.5 text-[11px] leading-[18px] " + (degraded ? 'tone-amber' : 'tone-emerald')}>{degraded ? '已降级' : '未降级'}</span>
      </FormRow>
    </FormSection>
  );

  const groupCards = (
    <div className={`grid ${embedded ? '' : 'xl:grid-cols-3 xl:gap-4'}`}>
      {groups.map((group) => (
        <GroupBoardCard
          key={group.key}
          group={group}
          onSwapGroup={onSwapGroup}
          onSetActiveAccount={onSetActiveAccount}
          onPinAccountSelection={onPinAccountSelection}
          onViewInInventory={onViewInInventory}
          busyKey={busyKey}
        />
      ))}
    </div>
  );

  if (embedded) {
    return (
      <div>
        {summaryCards}
        {groupCards}
      </div>
    );
  }

  return (
    <div>
      <FormSection title="调度编排" description="查看主组 / 备组 / 冷备的运行态与编排动作。">
        <FormBlock>{summaryCards}</FormBlock>
      </FormSection>
      {groupCards}
      <FormSection title="最近一次调度" description="最新调度摘要">
        <FormBlock>
          <div className="flex items-start gap-2 text-xs text-fg-body">
            {latestSnapshot?.finalError ? <ShieldAlert size={14} className="shrink-0 text-warn" /> : <GitBranch size={14} className="shrink-0 text-accent" />}
            <p>{latestSnapshot?.summary || '把完整调度结果收敛在底部，保留可观测性同时减少主视图占位。'}</p>
          </div>
          <div className="mt-2 flex flex-wrap items-center gap-2">
            <span className="rounded border px-1.5 text-[11px] leading-[18px] bg-surface text-fg-body border-line">{latestSnapshot?.requestPath || '/v1/responses'}</span>
            <span className="rounded border px-1.5 text-[11px] leading-[18px] tone-indigo">{latestSnapshot?.selectedAccountName || '待命中账号'}</span>
            <span className="rounded border px-1.5 text-[11px] leading-[18px] tone-slate">{Number.isFinite(latestSnapshot?.selectedPriority) ? `组内顺序 ${latestSnapshot.selectedPriority}` : '组内顺序待定'}</span>
          </div>
          <div className="mt-3 flex justify-end">
            <Button type="button" variant="secondary" size="sm" className="justify-between"
              onClick={() => onOpenSnapshot?.(latestSnapshot)} disabled={!onOpenSnapshot}>
              查看完整候选链路
              <ArrowRight size={14} className="ml-2" />
            </Button>
          </div>
        </FormBlock>
      </FormSection>
    </div>
  );
};

export default AccountPoolSchedulerSection;
