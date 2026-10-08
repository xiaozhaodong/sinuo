// ============================================
// 隐私保护页面 - 出站请求隐私规则工作台
// 2026-06-11 (v6.1 新增)
// 2026-10-07 桌面化：子页签与扫描参数并入工具栏下方的范围条，规则表格贴边铺满，
//            运行状态收进底部状态栏；部分规则未激活时在范围条下方给一条警示
// ============================================

import { useEffect, useMemo, useState } from 'react';
import {
  AlertTriangle,
  Download,
  FlaskConical,
  PackagePlus,
  Plus,
  Save
} from 'lucide-react';
import { Button, CustomSelect, ErrorMessage, LoadingSpinner } from '@components/ui';
import { PageToolbar } from '@components/layout/AppToolbar.jsx';
import PageTabs from '@components/layout/PageTabs.jsx';
import { ToolbarButton, ToolbarSpacer } from '@components/layout/ToolbarButton.jsx';
import { StatusBar, StatusItem, StatusSpacer } from '@components/desktop/StatusBar.jsx';
import { fetchEndpoints, fetchUpstreamAccounts, exportPrivacyRules } from '@utils/api.js';
import usePrivacyProtection from './hooks/usePrivacyProtection.js';
import useConfirm from '@hooks/useConfirm.js';
import PrivacyRulesTable from './components/PrivacyRulesTable.jsx';
import PrivacyRulesToolbar from './components/PrivacyRulesToolbar.jsx';
import PrivacyRuleDrawer from './components/PrivacyRuleDrawer.jsx';
import PrivacyRuleTestDrawer from './components/PrivacyRuleTestDrawer.jsx';
import PrivacyPresetDialog from './components/PrivacyPresetDialog.jsx';
import PrivacyExactSecretsPanel from './components/PrivacyExactSecretsPanel.jsx';
import PrivacyBuiltinRulesPanel from './components/PrivacyBuiltinRulesPanel.jsx';
import {
  PRIVACY_MODE_OPTIONS,
  PRIVACY_ON_ERROR_OPTIONS,
  PRIVACY_OVER_LIMIT_OPTIONS,
  buildReorderPayload,
  createEmptyPrivacyRuleForm,
  duplicateRuleForm,
  filterPrivacyRules,
  formatScanBytes,
  moveRuleInList,
  ruleToForm
} from './utils/privacyRules.js';
import { useTimezone } from '@contexts/TimezoneContext.jsx';

// 模式分段控件（修改后立即保存热生效）
const ModeSegmentedControl = ({ mode, busy, onChange }) => (
  <div className="inline-flex shrink-0 rounded-[7px] bg-surface-mut p-0.5">
    {PRIVACY_MODE_OPTIONS.map((opt) => (
      <button
        key={opt.value}
        type="button"
        disabled={busy}
        onClick={() => onChange(opt.value)}
        className={`h-[22px] whitespace-nowrap rounded-[5px] px-2.5 text-xs font-medium transition-colors ${
          mode === opt.value
            ? opt.value === 'disabled'
              ? 'bg-inverted text-fg-inverted shadow-sm'
              : opt.value === 'detect'
                ? 'bg-warn-solid text-white shadow-sm'
                : 'bg-indigo-600 text-white shadow-sm'
            : 'text-fg-muted hover:text-fg'
        } ${busy ? 'opacity-60 cursor-not-allowed' : ''}`}
      >
        {opt.label}
      </button>
    ))}
  </div>
);

// 扫描参数（点击保存生效，避免误触）：范围条右侧一组 24px 控件
// 注意：调用方通过 key 在 settings 变化时重挂载本组件，初始 state 直接取自 settings
const ScanSettingsBar = ({ settings, busy, onSave }) => {
  const [scanMaxBytes, setScanMaxBytes] = useState(settings.scan_max_bytes);
  const [overLimitAction, setOverLimitAction] = useState(settings.over_limit_action);
  const [onError, setOnError] = useState(settings.on_error);

  const dirty = Number(scanMaxBytes) !== settings.scan_max_bytes
    || overLimitAction !== settings.over_limit_action
    || onError !== settings.on_error;

  return (
    <div className="flex shrink-0 flex-wrap items-center gap-x-3 gap-y-1.5 text-xs text-fg-muted">
      <label className="flex items-center gap-1.5">
        扫描上限
        <input
          type="number"
          min="1024"
          value={scanMaxBytes}
          onChange={(e) => setScanMaxBytes(e.target.value)}
          className="desk-field w-24 text-xs tabular-nums"
        />
        字节
      </label>
      <span className="flex items-center gap-1.5">
        超限
        <CustomSelect size="xs" options={PRIVACY_OVER_LIMIT_OPTIONS} value={overLimitAction} onChange={setOverLimitAction} className="w-32" />
      </span>
      <span className="flex items-center gap-1.5">
        出错
        <CustomSelect size="xs" options={PRIVACY_ON_ERROR_OPTIONS} value={onError} onChange={setOnError} className="w-24" />
      </span>
      <Button
        size="sm"
        variant="secondary"
        icon={Save}
        disabled={!dirty || busy}
        onClick={() => onSave({
          scan_max_bytes: Number(scanMaxBytes),
          over_limit_action: overLimitAction,
          on_error: onError
        })}
      >
        保存
      </Button>
    </div>
  );
};

const PrivacyStatusBar = ({ settings, stats }) => {
  const { formatTimestamp } = useTimezone();
  const modeLabel = PRIVACY_MODE_OPTIONS.find((opt) => opt.value === settings.mode)?.label || settings.mode;

  return (
    <StatusBar>
      <StatusItem label="模式" value={modeLabel} />
      <StatusItem label="已启用规则" value={settings.enabled_rules} />
      <StatusItem label="快照" value={`v${settings.version}`} />
      <StatusItem label="扫描上限" value={formatScanBytes(settings.scan_max_bytes)} />
      {stats?.scan_count > 0 && <StatusItem label="累计扫描 / 命中" value={`${stats.scan_count} / ${stats.hit_count}`} />}
      {stats?.truncated_count > 0 && <StatusItem label="扫描截断" value={stats.truncated_count} valueClassName="text-warn" />}
      {stats?.blocked_count > 0 && <StatusItem label="已阻断" value={stats.blocked_count} valueClassName="text-danger" />}
      <StatusSpacer />
      <StatusItem label="最近更新" value={settings.updated_at ? formatTimestamp(settings.updated_at) : '-'} />
    </StatusBar>
  );
};

const PRIVACY_TABS = [
  { id: 'exact', label: '本地敏感值' },
  { id: 'builtin', label: '内置规则' },
  { id: 'advanced', label: '高级预设' }
];

const PrivacyProtectionPage = () => {
  const {
    settings, rules, exactSecrets, presets, stats, loading, error,
    reloadAll, reloadStats, saveSettings, saveRule, removeRule,
    saveExactSecret, removeExactSecret, clearExactSecrets,
    loadImportCandidates, importSecretCandidate,
    toggleRule, reorderRules, importPreset, runTest
  } = usePrivacyProtection();

  const [activeTab, setActiveTab] = useState('exact');
  const [filters, setFilters] = useState({});
  const [drawerOpen, setDrawerOpen] = useState(false);
  const [drawerRule, setDrawerRule] = useState(null);
  const [drawerSession, setDrawerSession] = useState(0);
  const [presetOpen, setPresetOpen] = useState(false);
  const { confirm, confirmDialog } = useConfirm();
  const [testDrawerOpen, setTestDrawerOpen] = useState(false);
  const [busy, setBusy] = useState(false);
  const [actionError, setActionError] = useState('');
  const [endpointOptions, setEndpointOptions] = useState([]);
  const [accountOptions, setAccountOptions] = useState([]);

  // 作用域可选项：端点列表与账号池（失败时留空，不阻塞页面）
  useEffect(() => {
    fetchEndpoints()
      .then((data) => {
        const list = data?.endpoints || [];
        setEndpointOptions(list.map((ep) => ({ value: ep.name, label: ep.name })));
      })
      .catch(() => setEndpointOptions([]));
    fetchUpstreamAccounts()
      .then((list) => {
        setAccountOptions((list || []).map((acc) => ({
          value: acc.id,
          label: `${acc.account_name || acc.accountName || `账号 ${acc.id}`} (#${acc.id})`
        })));
      })
      .catch(() => setAccountOptions([]));
  }, []);

  const builtinRules = useMemo(() => rules.filter((rule) => rule.source === 'builtin'), [rules]);
  const advancedRules = useMemo(() => rules.filter((rule) => rule.source !== 'builtin'), [rules]);
  const filteredRules = useMemo(() => filterPrivacyRules(advancedRules, filters), [advancedRules, filters]);
  // 未筛选时才允许调序，避免对部分列表重排造成误解
  const reorderEnabled = filteredRules.length === advancedRules.length;

  const runAction = async (action) => {
    setBusy(true);
    setActionError('');
    try {
      await action();
    } catch (err) {
      setActionError(err.message || String(err));
    } finally {
      setBusy(false);
    }
  };

  const handleModeChange = (mode) => runAction(() => saveSettings({ ...settings, mode }));
  const handleScanSettingsSave = (patch) => runAction(() => saveSettings({ ...settings, ...patch }));
  const handleToggle = (rule, enabled) => runAction(() => toggleRule(rule, enabled));
  const handleMove = (rule, direction) => runAction(async () => {
    const next = moveRuleInList(advancedRules, rule.id, direction);
    if (next !== advancedRules) {
      await reorderRules(buildReorderPayload(next));
    }
  });
  const handleDelete = async (rule) => {
    if (!(await confirm({ title: '删除隐私规则', message: `确定删除规则「${rule.name}」？删除后立即生效。`, confirmText: '确认删除', danger: true }))) return;
    runAction(() => removeRule(rule.id));
  };

  const openDrawer = (rule) => {
    setDrawerRule(rule);
    setDrawerSession((session) => session + 1);
    setDrawerOpen(true);
  };

  const handleDrawerSave = async (form) => {
    await saveRule(form, {
      enabled: form.enabled,
      name: form.name,
      description: form.description,
      priority: Number(form.priority),
      match_type: form.match_type,
      pattern: form.pattern,
      placeholder: form.placeholder,
      action: form.action,
      scope: form.scope
    });
    setDrawerOpen(false);
    reloadStats();
  };

  const handleExport = () => runAction(async () => {
    const data = await exportPrivacyRules();
    const blob = new Blob([JSON.stringify(data, null, 2)], { type: 'application/json' });
    const url = URL.createObjectURL(blob);
    const link = document.createElement('a');
    link.href = url;
    link.download = `privacy-rules-${new Date().toISOString().slice(0, 10)}.json`;
    link.click();
    URL.revokeObjectURL(url);
  });

  if (loading) {
    return <LoadingSpinner text="加载隐私保护配置..." />;
  }
  if (error || !settings) {
    return <div className="p-5"><ErrorMessage title="隐私保护加载失败" message={error || '服务未启用'} onRetry={reloadAll} /></div>;
  }

  return (
    <div className="motion-page flex min-h-0 flex-1 flex-col bg-surface">
      <PageToolbar subtitle="出站请求保护：转发前按规则检测 / 脱敏请求内容">
        <ModeSegmentedControl mode={settings.mode} busy={busy} onChange={handleModeChange} />
        <ToolbarSpacer />
        <ToolbarButton variant="bordered" icon={FlaskConical} label="规则测试" onClick={() => setTestDrawerOpen(true)} />
        <ToolbarButton icon={Download} title="导出规则" onClick={handleExport} disabled={busy} />
        {activeTab === 'advanced' && (
          <>
            <ToolbarButton icon={PackagePlus} title="导入预设" onClick={() => setPresetOpen(true)} disabled={busy} />
            <ToolbarButton icon={Plus} title="新增规则" onClick={() => openDrawer(createEmptyPrivacyRuleForm())} disabled={busy} />
          </>
        )}
      </PageToolbar>

      {/* 范围条：规则分类页签 + 扫描参数 */}
      <div className="flex shrink-0 flex-wrap items-center gap-x-4 gap-y-1.5 border-b border-line bg-inspector px-3 py-1.5">
        <PageTabs tabs={PRIVACY_TABS} activeTab={activeTab} onChange={setActiveTab} idPrefix="privacy" ariaLabel="隐私规则分类" />
        <span className="flex-1" aria-hidden="true" />
        <ScanSettingsBar
          key={`${settings.scan_max_bytes}|${settings.over_limit_action}|${settings.on_error}|${settings.version}`}
          settings={settings}
          busy={busy}
          onSave={handleScanSettingsSave}
        />
      </div>

      {settings.status === 'degraded' && (
        <div role="alert" className="selectable flex shrink-0 items-start gap-2 border-b border-warn-line bg-warn-soft px-4 py-1.5 text-xs text-warn">
          <AlertTriangle size={13} className="mt-0.5 shrink-0" />
          <span className="break-all">部分规则未激活{settings.compile_error ? `：${settings.compile_error}` : ''}</span>
        </div>
      )}
      {actionError && (
        <div role="alert" className="selectable shrink-0 break-all border-b border-danger-line bg-danger-soft px-4 py-1.5 text-xs text-danger">{actionError}</div>
      )}

      {/* 主体：当前分类的规则 */}
      <div
        id={`privacy-panel-${activeTab}`}
        role="tabpanel"
        aria-labelledby={`privacy-tab-${activeTab}`}
        className="flex min-h-0 flex-1 flex-col"
      >
        {activeTab === 'exact' && (
          <PrivacyExactSecretsPanel
            secrets={exactSecrets}
            busy={busy}
            onSave={async (form) => {
              setBusy(true);
              try {
                await saveExactSecret(form);
                reloadStats();
              } finally {
                setBusy(false);
              }
            }}
            onDelete={removeExactSecret}
            onClear={clearExactSecrets}
            onLoadCandidates={loadImportCandidates}
            onImportCandidate={async (input) => {
              await importSecretCandidate(input);
              reloadStats();
            }}
          />
        )}
        {activeTab === 'builtin' && (
          <div className="min-h-0 flex-1 overflow-auto">
            <PrivacyBuiltinRulesPanel
              rules={builtinRules}
              busy={busy}
              onToggle={handleToggle}
              onEdit={(rule) => openDrawer(ruleToForm(rule))}
            />
          </div>
        )}
        {activeTab === 'advanced' && (
          <>
            <div className="shrink-0 border-b border-warn-line bg-warn-soft px-4 py-1.5 text-xs leading-5 text-warn">
              这些规则可能误伤代码、日志、测试数据和工具输出。建议先使用“仅检测”，确认命中质量后再开启“脱敏转发”。
            </div>
            <PrivacyRulesToolbar
              filters={filters}
              onChange={setFilters}
              total={advancedRules.length}
              filtered={filteredRules.length}
            />
            <div className="min-h-0 flex-1 overflow-auto">
              <PrivacyRulesTable
                rules={filteredRules}
                busy={busy}
                reorderEnabled={reorderEnabled}
                onToggle={handleToggle}
                onEdit={(rule) => openDrawer(ruleToForm(rule))}
                onDuplicate={(rule) => openDrawer(duplicateRuleForm(rule))}
                onDelete={handleDelete}
                onMove={handleMove}
              />
            </div>
          </>
        )}
      </div>

      <PrivacyStatusBar settings={settings} stats={stats} />

      <PrivacyRuleDrawer
        key={drawerSession}
        open={drawerOpen}
        rule={drawerRule}
        saving={busy}
        onSave={async (form) => {
          // 错误向上抛给抽屉展示（保留用户输入），不进入页面级 actionError
          setBusy(true);
          try {
            await handleDrawerSave(form);
          } finally {
            setBusy(false);
          }
        }}
        onClose={() => setDrawerOpen(false)}
        endpointOptions={endpointOptions}
        accountOptions={accountOptions}
      />

      <PrivacyPresetDialog
        open={presetOpen}
        presets={presets}
        onImport={importPreset}
        onClose={() => setPresetOpen(false)}
      />

      <PrivacyRuleTestDrawer
        open={testDrawerOpen}
        onTest={runTest}
        onClose={() => setTestDrawerOpen(false)}
      />
      {confirmDialog}
    </div>
  );
};

export default PrivacyProtectionPage;
