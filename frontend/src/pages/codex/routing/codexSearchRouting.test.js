import test from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { isCodexOAuthAccount } from './codexRoutingView.js';

const source = (path) => readFileSync(new URL(path, import.meta.url), 'utf8');

test('Codex 编辑器提供联网搜索类型且已有映射锁定类型', () => {
  const editor = source('../../account-pool/components/CodexBindingEditor.jsx');
  assert.match(editor, /value: 'search', label: '联网搜索'/);
  assert.match(editor, /disabled=\{busy \|\| Boolean\(draft\.assignmentId\)\}/);
  assert.match(editor, /kindOptions = BINDING_KINDS\.filter/);
  assert.match(editor, /isSearch \? '' : draft\.model\.trim\(\)/);
  assert.match(editor, /kind: draft\.kind/);
  assert.match(editor, /readOnly=\{isSearch\}/);
  assert.match(editor, /initialFocusRef=\{isSearch \? noteInputRef : inputRef\}/);
});

test('Codex 搜索成员只列 OAuth 并保留已选历史成员以便取消', () => {
  const editor = source('../../account-pool/components/CodexBindingEditor.jsx');
  assert.match(editor, /isCodexOAuthAccount\(account\) \|\| draft\.accountIds\.includes\(accountId\(account\)\)/);
  assert.match(editor, /只列出 ChatGPT OAuth 账号，api_key 账号不支持联网搜索/);
  assert.match(editor, /isSearch && searchTaken/);
  assert.match(editor, /!isSearch && bindings\.some/);
});

test('只有搜索映射时工作区仍需确认开启模型路由', () => {
  const workspace = source('../../../components/model-routing/useRoutingWorkspace.js');
  const page = source('./CodexRoutingPage.jsx');
  assert.match(workspace, /routingCounts\(config\.enableGateBindings \? config\.enableGateBindings\(data\.bindings\) : data\.bindings\)/);
  assert.match(page, /enableGateBindings: codexModelBindings/);
  assert.match(page, /routingCounts\(codexModelBindings\(data\.bindings\)\)/);
  assert.match(page, /联网搜索映射仍然生效/);
  assert.match(page, /删除后联网搜索请求恢复按账号池编排调度/);
});

test('请求页搜索使用地球行且模型计数不包含搜索', () => {
  const access = source('../../requests/components/CodexAccessSwitcher.jsx');
  const tree = source('../../requests/components/route-switcher/BindingTree.jsx');
  const switcher = source('../../requests/components/CodexModelRoutingSwitcher.jsx');
  assert.equal((access.match(/modelRouteBindingCount: codexModelBindings\(bindings\)\.length/g) || []).length, 2);
  assert.match(tree, /webToolsLabel = '联网工具'/);
  assert.match(tree, /webTools \? webToolsLabel : binding\.model/);
  assert.match(switcher, /isWebTools=\{isCodexSearchBinding\} webToolsLabel="联网搜索"/);
  assert.match(switcher, /sortCodexBindings\(/);
});

test('请求页写回保留搜索 kind，表格和检查器使用业务显示名', () => {
  const requests = source('../../requests/index.jsx');
  const table = source('./CodexRoutingTable.jsx');
  const inspector = source('./CodexRoutingInspector.jsx');
  assert.match(requests, /kind: next\.kind \|\| 'model'/);
  assert.match(requests, /resolveErrorMessage\(err, '切换 Codex 模型路由策略失败'\)/);
  assert.match(table, /Globe size=/);
  assert.match(table, /title="不受模型路由开关影响">始终生效/);
  assert.match(inspector, /title=\{codexBindingDisplayName\(binding\)\}/);
  assert.match(inspector, /只能选择 ChatGPT OAuth 账号/);
});

test('搜索编辑器标识非 OAuth 成员并在保存前拦截，已删除账号不崩溃', async () => {
  const editor = source('../../account-pool/components/CodexBindingEditor.jsx');
  const stateSource = editor.slice(editor.indexOf('const accountState = ') + 'const accountState = '.length, editor.indexOf('\n\nconst CodexBindingEditor'));
  const accountState = new Function('isCodexOAuthAccount', `return ${stateSource}`)(isCodexOAuthAccount);
  const apiKey = { id: 2, provider_type: 'api_key', has_credential: true };
  const oauth = { id: 1, provider_type: 'oauth', has_credential: true };
  assert.deepEqual(accountState(apiKey, true), { label: '不支持联网搜索', tone: 'tone-rose' });
  assert.equal(accountState(apiKey, false).label, '可参与');
  assert.equal(accountState(undefined, true).label, '账号不存在');
  assert.match(editor, /accountState\(account, isSearch\)/);

  const submitSource = editor.slice(editor.indexOf('const submit = ') + 'const submit = '.length, editor.indexOf('\n\n  return ('));
  for (const [isSearch, selectedAccount, rejected] of [[true, apiKey, true], [true, undefined, true], [true, oauth, false], [false, apiKey, false]]) {
    let saved = false;
    let message = '';
    const draft = { kind: isSearch ? 'search' : 'model', model: 'gpt', accountIds: [2], routeMode: 'auto', note: '' };
    const submit = new Function('isSearch', 'draft', 'byId', 'isCodexOAuthAccount', 'searchTaken', 'bindings', 'setError', 'onSave', `return ${submitSource}`)(
      isSearch, draft, new Map([[2, selectedAccount]]), isCodexOAuthAccount, false, [],
      (error) => { message = error; }, async () => { saved = true; }
    );
    await submit();
    assert.equal(saved, !rejected);
    assert.equal(message, rejected ? '联网搜索只能使用 ChatGPT OAuth 账号，请取消勾选 api_key 账号' : '');
  }
});

test('搜索检查器使用匹配属性名，普通模型仍使用模型', () => {
  const inspector = source('./CodexRoutingInspector.jsx');
  assert.match(inspector, /<Property label=\{isCodexSearchBinding\(binding\) \? '匹配' : '模型'\}/);
});

test('请求页只统计模型绑定，只有搜索时同时显示空模型提示和搜索列表', () => {
  const switcher = source('../../requests/components/CodexModelRoutingSwitcher.jsx');
  assert.match(switcher, /const modelCount = codexModelBindings\(bindings\)\.length/);
  assert.match(switcher, /title=\{modelCount > 0 \? `模型路由 · \$\{modelCount\} 个模型绑定` : '模型路由'\}/);
  assert.match(switcher, /\{modelCount === 0 && <PopoverNote>尚无模型绑定/);
  assert.match(switcher, /\{sorted\.length > 0 && <BindingTree bindings=\{sorted\}/);
  assert.ok(switcher.indexOf('{modelCount === 0 && <PopoverNote>') < switcher.indexOf('{sorted.length > 0 && <BindingTree'));
});
