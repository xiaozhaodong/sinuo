// Codex 模型绑定编辑器
import { useMemo, useRef, useState } from 'react';
import { AlertTriangle, Check, ChevronDown, ChevronUp } from 'lucide-react';
import { Button, CustomSelect } from '@components/ui';
import Sheet from '@components/desktop/Sheet.jsx';
import Switch from '@components/desktop/Switch.jsx';
import { FormFieldRow, FormRow, FormSection } from '@components/desktop/FormSection.jsx';
import { CODEX_SEARCH_KIND, isCodexOAuthAccount, isCodexSearchBinding } from '../../codex/routing/codexRoutingView.js';

const BINDING_KINDS = [
  { value: 'model', label: '模型名', hint: '按请求 model 精确匹配，大小写不敏感' },
  { value: 'search', label: '联网搜索', hint: 'Codex 联网搜索请求，不看模型名，不受模型路由开关影响' }
];

const MODES = [
  { value: 'auto', label: '自动', hint: '按账号顺序尝试所有可用账号' },
  { value: 'manual_preferred', label: '优选', hint: '优先使用目标账号，失败后继续尝试其他成员' },
  { value: 'manual_fixed', label: '固定', hint: '只使用目标账号，失败后直接结束，不切换账号' }
];
const accountId = (account) => Number(account?.id ?? account?.ID ?? account?.account_id ?? account?.accountId);
const accountName = (account) => account?.account_name || account?.accountName || account?.name || `账号 ${accountId(account)}`;
const accountProvider = (account) => String(account?.provider_type || account?.providerType || 'unknown').replaceAll('_', ' ');
const accountState = (account, isSearch = false) => {
  if (!account) return { label: '账号不存在', tone: 'tone-rose' };
  if (isSearch && !isCodexOAuthAccount(account)) return { label: '不支持联网搜索', tone: 'tone-rose' };
  if (account.enabled === false || account.state === 'disabled_auth') return { label: '已停用', tone: 'tone-rose' };
  if (!account.has_credential && !account.credential_raw && !account.credentialRaw) return { label: '无凭据', tone: 'tone-amber' };
  const until = account.cooldown_until || account.cooldownUntil;
  if (until && new Date(until).getTime() > Date.now()) return { label: '冷却中', tone: 'tone-amber' };
  return { label: '可参与', tone: 'tone-emerald' };
};

const CodexBindingEditor = ({ binding, accounts, bindings, busy, onSave, onClose }) => {
  const inputRef = useRef(null);
  const noteInputRef = useRef(null);

  const [draft, setDraft] = useState(() => ({
    assignmentId: binding?.assignment_id || null,
    kind: binding?.kind || 'model',
    model: binding?.model || '',
    accountIds: (binding?.account_ids || []).map(Number),
    memberEnabled: Object.fromEntries((binding?.account_ids || []).map(id => [Number(id), binding?.member_enabled?.[id] !== false])),
    routeMode: binding?.route_mode || 'auto',
    targetId: Number(binding?.route_target_account_id || 0),
    enabled: binding?.enabled !== false,
    note: binding?.note || ''
  }));

  const [query, setQuery] = useState('');

  const [error, setError] = useState('');
  const isSearch = draft.kind === CODEX_SEARCH_KIND;
  const searchTaken = bindings.some((item) => isCodexSearchBinding(item) && item.assignment_id !== draft.assignmentId);
  const kindOptions = BINDING_KINDS.filter((kind) => kind.value !== CODEX_SEARCH_KIND || isSearch || !searchTaken);
  const title = isSearch
    ? (draft.assignmentId ? '编辑联网搜索映射' : '新建联网搜索映射')
    : (draft.assignmentId ? '编辑 Codex 模型路由' : '新建 Codex 模型路由');

  const byId = useMemo(() => new Map(accounts.map(account => [accountId(account), account])), [accounts]);

  const visible = useMemo(() => {
    const needle = query.trim().toLowerCase();
    return accounts.filter(account => (!isSearch || isCodexOAuthAccount(account) || draft.accountIds.includes(accountId(account)))
      && (!needle || `${accountName(account)} ${accountProvider(account)} ${accountId(account)}`.toLowerCase().includes(needle)));
  }, [accounts, query, isSearch, draft.accountIds]);

  const targetOptions = draft.accountIds.map(id => ({
    value: id,
    label: `${accountName(byId.get(id))} (#${id})`
  }));

  const patch = next => {
    setDraft(next);
    setError('');
  };

  const toggleAccount = id => {
    const selected = draft.accountIds.includes(id);
    const next = selected ? draft.accountIds.filter(item => item !== id) : [...draft.accountIds, id];
    const memberEnabled = {
      ...draft.memberEnabled
    };
    if (selected) delete memberEnabled[id];else memberEnabled[id] = true;
    const targetId = next.includes(draft.targetId) ? draft.targetId : next.length === 1 ? next[0] : 0;
    patch({
      ...draft,
      accountIds: next,
      memberEnabled,
      targetId,
      routeMode: next.length === 0 ? 'auto' : draft.routeMode
    });
  };

  const toggleMember = id => patch({
    ...draft,
    memberEnabled: {
      ...draft.memberEnabled,
      [id]: draft.memberEnabled[id] === false
    }
  });

  const move = (id, delta) => {
    const index = draft.accountIds.indexOf(id);
    const nextIndex = index + delta;
    if (index < 0 || nextIndex < 0 || nextIndex >= draft.accountIds.length) return;
    const next = [...draft.accountIds];
    [next[index], next[nextIndex]] = [next[nextIndex], next[index]];
    patch({
      ...draft,
      accountIds: next
    });
  };

  const submit = async () => {
    const model = isSearch ? '' : draft.model.trim();
    if (!isSearch && !model) return setError('请输入模型名');
    if (isSearch && searchTaken) return setError('联网搜索映射已经存在，只能配置一条');
    if (!draft.accountIds.length) return setError('至少选择一个账号');
    if (isSearch && draft.accountIds.some((id) => {
      const account = byId.get(id);
      return !account || !isCodexOAuthAccount(account);
    })) return setError('联网搜索只能使用 ChatGPT OAuth 账号，请取消勾选 api_key 账号');
    if (draft.routeMode !== 'auto' && !draft.targetId) return setError('优选 / 固定模式需要选择目标账号');
    if (draft.routeMode !== 'auto' && !draft.accountIds.includes(draft.targetId)) return setError('目标账号必须在成员列表中');
    if (!isSearch && bindings.some(item => item.assignment_id !== draft.assignmentId && !isCodexSearchBinding(item) && item.model.trim().toLowerCase() === model.toLowerCase())) return setError(`模型「${model}」已经配置过路由`);
    await onSave(draft.assignmentId, {
      kind: draft.kind,
      model,
      account_ids: draft.accountIds,
      member_enabled: draft.memberEnabled,
      route_mode: draft.routeMode,
      route_target_account_id: draft.routeMode === 'auto' ? 0 : draft.targetId,
      enabled: draft.enabled,
      note: draft.note.trim()
    });
  };

  return (
    <Sheet
      title={title}
      label={title}
      width={640}
      closeLabel="关闭 Codex 绑定编辑"
      subtitle={isSearch ? '联网搜索只会在这里选中的 OAuth 账号之间调度，不受模型路由开关影响。' : '模型只会在这里选中的账号之间调度。'}
      onClose={onClose}
      initialFocusRef={isSearch ? noteInputRef : inputRef}
      footerStart={error && (
        <span role="alert" className="flex items-start gap-2 text-danger">
          <AlertTriangle size={14} className="shrink-0" />
          {error}
        </span>
      )}
      footer={(
        <>
          <Button variant="ghost" size="sm" onClick={onClose} disabled={busy}>取消</Button>
          <Button size="sm" onClick={submit} loading={busy}>保存</Button>
        </>
      )}
    >
      <FormSection title="基本信息">
        <FormFieldRow label="匹配方式" hint={kindOptions.find((kind) => kind.value === draft.kind)?.hint}>
          <CustomSelect options={kindOptions} value={draft.kind} disabled={busy || Boolean(draft.assignmentId)}
            size="sm" className="w-full" onChange={(kind) => patch({ ...draft, kind })} />
        </FormFieldRow>
        <FormFieldRow label={isSearch ? '请求路径' : '模型名'} htmlFor="codex-model-name" hint={isSearch ? '不按模型名匹配' : '精确匹配，大小写不敏感'}>
          <input
            id="codex-model-name"
            ref={inputRef}
            className="desk-field w-full font-mono"
            value={isSearch ? '/v1/alpha/search' : draft.model}
            readOnly={isSearch}
            disabled={busy}
            onChange={(event) => patch({ ...draft, model: event.target.value })}
            placeholder="gpt-5.5"
          />
        </FormFieldRow>
        <FormFieldRow label="调度策略" hint={MODES.find((mode) => mode.value === draft.routeMode)?.hint}>
          <CustomSelect
            options={MODES}
            value={draft.routeMode}
            disabled={busy}
            size="sm"
            className="w-full"
            onChange={(routeMode) => patch({
              ...draft,
              routeMode,
              targetId: routeMode === 'auto' ? 0 : (draft.targetId || (draft.accountIds.length === 1 ? draft.accountIds[0] : 0))
            })}
          />
        </FormFieldRow>
      </FormSection>
      <FormSection title="账号成员与顺序" description="成员顺序就是自动模式的尝试顺序">
        {isSearch && <p className="px-3.5 py-2 text-xs text-fg-muted">只列出 ChatGPT OAuth 账号，api_key 账号不支持联网搜索</p>}
        <div className="flex items-center gap-2 border-b border-line-soft px-3.5 py-2">
          <input
            className="desk-field min-w-0 flex-1"
            value={query}
            disabled={busy}
            onChange={(event) => setQuery(event.target.value)}
            placeholder="搜索账号名称、类型或 ID"
          />
          <span className="text-[11.5px] text-fg-muted">已选 {draft.accountIds.length}</span>
        </div>
        <div className="max-h-[250px] divide-y divide-line-soft overflow-y-auto">
          {visible.map((account) => {
            const id = accountId(account);
            const selected = draft.accountIds.includes(id);
            const state = accountState(account, isSearch);
            return (
              <button
                key={id}
                type="button"
                disabled={busy}
                aria-pressed={selected}
                onClick={() => toggleAccount(id)}
                className={`flex w-full items-center gap-2 px-3.5 py-2 text-left disabled:opacity-50 ${selected ? 'bg-accent-soft/60' : 'hover:bg-surface-mut/60'}`}
              >
                <span
                  aria-hidden="true"
                  className={`flex h-4 w-4 shrink-0 items-center justify-center rounded-[5px] border ${selected ? 'border-accent-ring bg-accent-ring text-white' : 'border-line-strong bg-surface'}`}
                >
                  {selected && <Check size={11} strokeWidth={2.5} />}
                </span>
                <span className="min-w-0 flex-1">
                  <span className="block truncate text-xs font-medium text-fg-body">
                    {accountName(account)} <span className="font-mono text-[11px] text-fg-muted">#{id}</span>
                  </span>
                  <span className="block truncate text-[11.5px] text-fg-muted">{accountProvider(account)}</span>
                </span>
                <span className={`${state.tone} rounded border px-1.5 text-[11px] leading-[18px]`}>{state.label}</span>
              </button>
            );
          })}
        </div>
      </FormSection>
      {draft.accountIds.length > 0 && (
        <FormSection title="当前顺序">
          {draft.accountIds.map((id, index) => (
            <div key={id} className="flex min-h-8 items-center gap-2 px-3.5 py-1.5">
              <span className="w-5 text-center font-mono text-[11px] text-fg-muted">{index + 1}</span>
              <span className="min-w-0 flex-1 truncate text-xs text-fg-body">{accountName(byId.get(id))}</span>
              {draft.routeMode !== 'auto' && id === draft.targetId && <span className="text-[11px] text-accent">目标</span>}
              <button
                type="button"
                disabled={busy || index === 0}
                onClick={() => move(id, -1)}
                className="inline-flex h-6 w-6 items-center justify-center rounded text-fg-muted hover:bg-surface-mut hover:text-fg disabled:opacity-30"
              >
                <ChevronUp size={14} />
              </button>
              <button
                type="button"
                disabled={busy || index === draft.accountIds.length - 1}
                onClick={() => move(id, 1)}
                className="inline-flex h-6 w-6 items-center justify-center rounded text-fg-muted hover:bg-surface-mut hover:text-fg disabled:opacity-30"
              >
                <ChevronDown size={14} />
              </button>
            </div>
          ))}
        </FormSection>
      )}
      <FormSection title="参与本模型组调度">
        {draft.accountIds.map((id) => (
          <label key={id} className="flex min-h-8 items-center gap-2 px-3.5 py-1.5 text-xs">
            <input
              type="checkbox"
              className="app-checkbox"
              checked={draft.memberEnabled[id] !== false}
              onChange={() => toggleMember(id)}
            />
            {accountName(byId.get(id))}
          </label>
        ))}
      </FormSection>
      <FormSection title="目标与备注">
        <FormFieldRow label="目标账号" hint="仅优选 / 固定模式使用">
          <CustomSelect
            options={targetOptions}
            value={draft.targetId || ''}
            disabled={busy || draft.routeMode === 'auto'}
            size="sm"
            className="w-full"
            placeholder={draft.routeMode === 'auto' ? '自动模式无需目标' : '选择目标账号'}
            onChange={(value) => patch({ ...draft, targetId: Number(value) })}
          />
        </FormFieldRow>
        <FormFieldRow label="备注（可选）" htmlFor="codex-route-note">
          <input
            id="codex-route-note"
            ref={noteInputRef}
            className="desk-field w-full"
            value={draft.note}
            disabled={busy}
            onChange={(event) => patch({ ...draft, note: event.target.value })}
            placeholder="例如：高性能账号"
          />
        </FormFieldRow>
        <FormRow label="启用该路由">
          <Switch
            label="启用该路由"
            checked={draft.enabled}
            disabled={busy}
            onChange={(enabled) => patch({ ...draft, enabled })}
          />
        </FormRow>
      </FormSection>
    </Sheet>
  );
};

// 编辑器与抽屉共用原有的选项和账号展示函数，避免维护第二份规则。
// eslint-disable-next-line react-refresh/only-export-components
export { MODES, accountId, accountName };
export default CodexBindingEditor;
