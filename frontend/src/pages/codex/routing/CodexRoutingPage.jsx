import { AlertTriangle } from 'lucide-react';
import { createCodexModelRouteBinding, deleteCodexModelRouteBinding, fetchCodexModelRouteBindings, fetchModelRoutingSettings, fetchUpstreamAccounts, testCodexModelRouting, updateCodexModelRouteBinding, updateModelRoutingSettings } from '@utils/api.js';
import CodexBindingEditor, { accountId } from '../../account-pool/components/CodexBindingEditor.jsx';
import CodexRouteTestBox from '../../account-pool/components/CodexRouteTestBox.jsx';
import CodexRoutingTable from './CodexRoutingTable.jsx';
import CodexRoutingInspector from './CodexRoutingInspector.jsx';
import { codexBindingDisplayName, codexBindingPayload, codexModelBindings, isCodexSearchBinding, sortCodexBindings } from './codexRoutingView.js';
import ModelRoutingWorkspace from '@components/model-routing/ModelRoutingWorkspace.jsx';
import { routingCounts } from '@components/model-routing/routingView.js';

const fetchRoutingData = async () => {
  const [settings, bindings, accounts] = await Promise.all([fetchModelRoutingSettings(), fetchCodexModelRouteBindings(), fetchUpstreamAccounts()]);
  return { settings, bindings: sortCodexBindings(bindings), members: accounts };
};

// 已开启但没有启用中的映射：未绑定模型不会回落到全局账号池
const NoEnabledBindingNotice = ({ data }) => {
  if (!data.settings?.codex_enabled || routingCounts(codexModelBindings(data.bindings)).enabled > 0) return null;
  return (
    <div role="alert" className="flex shrink-0 items-start gap-2 border-b border-warn-line bg-warn-soft px-4 py-2 text-xs text-warn">
      <AlertTriangle size={14} className="shrink-0" />
      <span>开启后未绑定模型不会回落到全局账号池。</span>
    </div>
  );
};

const enableConfirmation = () => ({
  title: '开启 Codex 模型路由',
  message: '当前没有启用中的 Codex 模型路由，开启后未绑定模型会返回 400。仍要开启吗？',
  confirmText: '仍要开启'
});

const deleteConfirmation = (binding) => isCodexSearchBinding(binding) ? ({
  title: '删除联网搜索映射',
  message: '删除后联网搜索请求恢复按账号池编排调度（只用 ChatGPT OAuth 账号）。'
}) : ({
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
  bindingDisplayName: codexBindingDisplayName,
  enableGateBindings: codexModelBindings,
  restoreAutoPatch: () => ({ route_mode: 'auto', route_target_account_id: 0 }),
  settingsField: 'codex_enabled',
  switchLabel: '启用 Codex 模型路由',
  enabledMessage: '已生效：Codex 请求按模型映射选择账号；联网搜索映射不受此开关影响。',
  disabledMessage: '关闭中：模型映射未生效，Codex 请求按账号池编排；联网搜索映射仍然生效。',
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
