import { AlertTriangle } from 'lucide-react';
import { createCodexModelRouteBinding, deleteCodexModelRouteBinding, fetchCodexModelRouteBindings, fetchModelRoutingSettings, fetchUpstreamAccounts, testCodexModelRouting, updateCodexModelRouteBinding, updateModelRoutingSettings } from '@utils/api.js';
import CodexBindingEditor, { accountId } from '../../account-pool/components/CodexBindingEditor.jsx';
import CodexRouteTestBox from '../../account-pool/components/CodexRouteTestBox.jsx';
import CodexRoutingTable from './CodexRoutingTable.jsx';
import CodexRoutingInspector from './CodexRoutingInspector.jsx';
import { codexBindingPayload } from './codexRoutingView.js';
import ModelRoutingWorkspace from '@components/model-routing/ModelRoutingWorkspace.jsx';
import { routingCounts } from '@components/model-routing/routingView.js';

const fetchRoutingData = async () => {
  const [settings, bindings, accounts] = await Promise.all([fetchModelRoutingSettings(), fetchCodexModelRouteBindings(), fetchUpstreamAccounts()]);
  return { settings, bindings, members: accounts };
};

// 已开启但没有启用中的映射：未绑定模型不会回落到全局账号池
const NoEnabledBindingNotice = ({ data }) => {
  if (!data.settings?.codex_enabled || routingCounts(data.bindings).enabled > 0) return null;
  return (
    <div role="alert" className="flex shrink-0 items-start gap-2 border-b border-warn-line bg-warn-soft px-4 py-2 text-xs text-warn">
      <AlertTriangle size={14} className="shrink-0" />
      <span>开启后未绑定模型不会回落到全局账号池。</span>
    </div>
  );
};

const enableConfirmation = () => ({
  title: '开启 Codex 模型路由',
  message: '当前没有启用中的 Codex 模型路由，开启后未绑定模型会返回 422。仍要开启吗？',
  confirmText: '仍要开启'
});

const deleteConfirmation = (binding) => ({
  title: '删除模型路由',
  message: `确定删除模型「${binding.model}」的路由吗？`
});

const config = {
  api: {
    fetchData: fetchRoutingData,
    createBinding: createCodexModelRouteBinding,
    updateBinding: updateCodexModelRouteBinding,
    deleteBinding: deleteCodexModelRouteBinding,
    updateSettings: updateModelRoutingSettings,
    testRouting: testCodexModelRouting
  },
  buildPatch: codexBindingPayload,
  bindingDisplayName: (binding) => binding.model || '',
  restoreAutoPatch: () => ({ route_mode: 'auto', route_target_account_id: 0 }),
  settingsField: 'codex_enabled',
  switchLabel: '启用 Codex 模型路由',
  enabledMessage: '已生效：Codex 请求按模型绑定选择账号。',
  disabledMessage: '关闭中：Codex 请求继续使用全局账号池调度。',
  listLabel: 'Codex 模型路由列表',
  testLabel: 'Codex 路由试算',
  emptyDescription: '点击工具栏的 + 添加模型与账号映射。',
  enableConfirmation,
  deleteConfirmation,
  memberProps: (members) => ({ accountById: new Map(members.map((account) => [accountId(account), account])) }),
  editorMembersProp: 'accounts',
  Table: CodexRoutingTable,
  Inspector: CodexRoutingInspector,
  TestBox: CodexRouteTestBox,
  Editor: CodexBindingEditor,
  ExtraNotices: NoEnabledBindingNotice
};

const CodexRoutingPage = ({ active = true, headerTarget = null }) => <ModelRoutingWorkspace active={active} headerTarget={headerTarget} config={config} />;

export default CodexRoutingPage;
