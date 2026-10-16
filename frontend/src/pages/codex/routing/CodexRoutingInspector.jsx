import { Pencil, Trash2, X } from 'lucide-react';
import { Button } from '@components/ui';
import { ToolbarButton } from '@components/layout/ToolbarButton.jsx';
import { Inspector, InspectorBody, InspectorFooter, InspectorHeader, InspectorSection, Property, PropertyList } from '@components/desktop/Inspector.jsx';
import { useTimezone } from '@contexts/TimezoneContext.jsx';
import { MODES, accountName } from '../../account-pool/components/CodexBindingEditor.jsx';
import { toAccountAuthLabel, toQuotaStatusLabel } from '../../account-pool/utils/accountPool.js';
import { codexBindingDisplayName, codexMemberState, codexOutcomeLabel, isCodexSearchBinding } from './codexRoutingView.js';

const AccountDetails = ({ binding, accountById, formatTimestamp }) => (
  <InspectorSection title={`成员账号 · ${binding.account_ids.length}`}>
    <div className="space-y-3">
      {binding.account_ids.map((id, index) => {
        const account = accountById.get(Number(id));
        const state = codexMemberState(binding, id, account);
        const target = binding.route_mode !== 'auto' && Number(id) === Number(binding.route_target_account_id);
        return (
          <div key={id} className="min-w-0 text-[11.5px]">
            <div className="flex flex-wrap items-center gap-1.5">
              <span className="selectable break-all font-medium text-fg">{index + 1}. {account ? accountName(account) : `账号 ${id}`} <span className="text-fg-muted">#{id}</span></span>
              {target && <span className="text-accent">目标</span>}
              <span className={`${state.tone} rounded border px-1.5`}>{state.label}</span>
            </div>
            {state.detail && <p className="mt-1 text-warn">{state.detail}</p>}
            {binding.member_enabled?.[id] === false && !state.detail && <p className="mt-1 text-warn">已暂停自动和优选调度；手动固定仍需账号可用。</p>}
            {account && <div className="mt-1 text-fg-muted">{toAccountAuthLabel(account.provider_type)} · 额度 {toQuotaStatusLabel(account.quota_status)}</div>}
            {account?.cooldown_until && <div className="mt-1 text-fg-muted">冷却至 {formatTimestamp(account.cooldown_until)}</div>}
            {!account && <p className="mt-1 text-danger">账号已不存在，编辑保存前请取消选择。</p>}
          </div>
        );
      })}
      {binding.account_ids.length === 0 && <p className="text-xs text-danger">未选择任何账号</p>}
    </div>
  </InspectorSection>
);

const CodexRoutingInspector = ({ binding, accountById, busy, onEdit, onDelete, onClearSelection }) => {
  const { formatTimestamp } = useTimezone();
  const lastAccount = accountById.get(Number(binding.last_account_id));
  const target = accountById.get(Number(binding.route_target_account_id));
  return (
    <Inspector label="Codex 模型路由详情">
      <InspectorHeader title={codexBindingDisplayName(binding)} subtitle="模型路由"
        badges={<button type="button" onClick={onClearSelection} aria-label="清除选择，查看路由试算" title="路由试算" className="ml-auto rounded p-1 text-fg-muted hover:bg-surface-mut"><X size={14} /></button>} />
      <InspectorBody>
        <InspectorSection title="映射">
          <PropertyList labelWidth={60} align="start">
            <Property label={isCodexSearchBinding(binding) ? '匹配' : '模型'} truncate={false}><span className="selectable break-all">{codexBindingDisplayName(binding)}</span></Property>
            <Property label="状态">{binding.enabled ? '已启用' : '已停用'}</Property>
            <Property label="策略">{MODES.find((mode) => mode.value === binding.route_mode)?.label || binding.route_mode}</Property>
            {binding.route_mode !== 'auto' && <Property label="目标" truncate={false}>{target ? accountName(target) : `账号 ${binding.route_target_account_id}`}</Property>}
            {binding.note && <Property label="备注" truncate={false}><span className="selectable break-words">{binding.note}</span></Property>}
          </PropertyList>
        </InspectorSection>
        {isCodexSearchBinding(binding) && <p className="px-4 py-2 text-xs text-fg-muted">处理 Codex 联网搜索（/v1/alpha/search），不受模型路由开关影响；只能选择 ChatGPT OAuth 账号。</p>}
        <AccountDetails binding={binding} accountById={accountById} formatTimestamp={formatTimestamp} />
        <InspectorSection title="最近命中">
          <PropertyList labelWidth={60}>
            <Property label="账号" truncate={false}>{binding.last_account_id ? (lastAccount ? accountName(lastAccount) : `账号 ${binding.last_account_id}`) : '—'}</Property>
            <Property label="时间" title={formatTimestamp(binding.last_decision_at)}>{binding.last_decision_at ? formatTimestamp(binding.last_decision_at) : '—'}</Property>
            <Property label="结果" truncate={false}>{codexOutcomeLabel(binding.last_outcome)}</Property>
          </PropertyList>
        </InspectorSection>
      </InspectorBody>
      <InspectorFooter>
        <Button variant="secondary" icon={Pencil} disabled={busy} onClick={() => onEdit(binding)}>编辑…</Button>
        <span className="flex-1" />
        <ToolbarButton icon={Trash2} title="删除映射" disabled={busy} onClick={() => onDelete(binding)} />
      </InspectorFooter>
    </Inspector>
  );
};

export default CodexRoutingInspector;
