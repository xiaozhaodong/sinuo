import { AlertTriangle } from 'lucide-react';
import { testModelRouting, createModelRouteBinding, deleteModelRouteBinding, fetchEndpoints, fetchModelRouteBindings, fetchModelRoutingSettings, updateModelRouteBinding, updateModelRoutingSettings } from '@utils/api.js';
import { bindingDisplayName, isWebToolsBinding, patchBindingPayload } from '../../endpoints/utils/modelRouting.js';
import ModelBindingDialog from '../../endpoints/components/model-routing/ModelBindingDialog.jsx';
import RouteTestBox from '../../endpoints/components/model-routing/RouteTestBox.jsx';
import ClaudeRoutingTable from './ClaudeRoutingTable.jsx';
import ClaudeRoutingInspector from './ClaudeRoutingInspector.jsx';
import { claudeRouteModePatch } from './routingView.js';
import ModelRoutingWorkspace from '@components/model-routing/ModelRoutingWorkspace.jsx';

const fetchRoutingData = async () => {
  const [settings, bindings, endpointData] = await Promise.all([fetchModelRoutingSettings(), fetchModelRouteBindings(), fetchEndpoints()]);
  return { settings, bindings, members: endpointData.endpoints };
};

// 配置加载失败按 fail-closed 处理：开关打开时所有 Claude 请求返回 422
const LoadFailedNotice = ({ data }) => {
  if (data.settings?.status !== 'load_failed') return null;
  return (
    <div role="alert" className="selectable flex shrink-0 items-start gap-2 border-b border-danger-line bg-danger-soft px-4 py-2 text-xs text-danger">
      <AlertTriangle size={14} className="shrink-0" />
      <span>
        <b>配置加载失败：</b>
        {data.settings.claude_enabled
          ? '模型路由仍处于启用状态，为避免绕过模型隔离，所有 Claude 请求都会返回 422。请修复配置后重新加载。'
          : '模型路由当前关闭，请求按全局端点策略调度。修复数据库配置后重新加载。'}
      </span>
    </div>
  );
};

const enableConfirmation = (bindings) => ({
  title: '开启模型路由',
  message: bindings.length > 0
    ? `当前 ${bindings.length} 条映射全部处于停用状态，开启后所有 Claude 请求都会返回未分配错误。仍要开启吗？`
    : '还没有任何模型映射，开启后所有 Claude 请求都会返回未分配错误。仍要开启吗？',
  confirmText: '仍要开启'
});

const deleteConfirmation = (binding) => (isWebToolsBinding(binding)
  ? { title: '删除联网工具映射', message: '确定删除联网工具映射吗？删除后带 web_search / web_fetch 的请求会改按模型映射调度。' }
  : { title: '删除模型映射', message: `确定删除模型「${binding.model}」的映射吗？删除后该模型的请求会返回未分配错误。` });

const config = {
  api: {
    fetchData: fetchRoutingData,
    createBinding: createModelRouteBinding,
    updateBinding: updateModelRouteBinding,
    deleteBinding: deleteModelRouteBinding,
    updateSettings: updateModelRoutingSettings,
    testRouting: testModelRouting
  },
  buildPatch: patchBindingPayload,
  bindingDisplayName,
  restoreAutoPatch: (binding) => claudeRouteModePatch(binding, 'auto'),
  settingsField: 'claude_enabled',
  switchLabel: '启用 Claude 模型路由',
  enabledMessage: '已生效：Claude 请求按模型选择端点，端点页的全局手动路由暂不参与。',
  disabledMessage: '关闭中：Claude 请求按端点页的全局调度策略执行。',
  listLabel: 'Claude 模型路由列表',
  testLabel: 'Claude 路由试算',
  emptyDescription: '点击工具栏的 + 添加模型与上游端点映射。',
  enableConfirmation,
  deleteConfirmation,
  memberProps: (members) => ({ endpoints: members }),
  editorMembersProp: 'endpoints',
  Table: ClaudeRoutingTable,
  Inspector: ClaudeRoutingInspector,
  TestBox: RouteTestBox,
  Editor: ModelBindingDialog,
  ExtraNotices: LoadFailedNotice
};

const ClaudeRoutingPage = ({ active = true, headerTarget = null }) => <ModelRoutingWorkspace active={active} headerTarget={headerTarget} config={config} />;

export default ClaudeRoutingPage;
