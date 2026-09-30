// ============================================
// 隐私保护页面 - 出站请求隐私规则工作台
// 2026-06-11 (v6.1 新增)
// ============================================

import { useEffect, useMemo, useState } from 'react';
import {
  AlertTriangle,
  Download,
  FlaskConical,
  PackagePlus,
  Plus,
  Save,
  Shield
} from 'lucide-react';
import { Button, CustomSelect, ErrorMessage, Input, LoadingSpinner } from '@components/ui';
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
  <div className="inline-flex rounded-lg border border-line p-0.5 bg-surface shadow-sm">
    {PRIVACY_MODE_OPTIONS.map((opt) => (
      <button
        key={opt.value}
        type="button"
        disabled={busy}
        onClick={() => onChange(opt.value)}
        className={`px-3.5 py-1.5 rounded-md text-sm font-medium transition-colors ${
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

// 扫描参数（点击保存生效，避免误触）
// 注意：调用方通过 key 在 settings 变化时重挂载本组件，初始 state 直接取自 settings
const ScanSettingField = ({ label, className = '', children }) => (
  <div className={`space-y-1.5 ${className}`}>
    <label className="block h-5 text-sm font-semibold leading-5 text-fg-body">{label}</label>
    <div className="h-10">{children}</div>
  </div>
);

const ScanSettingsBar = ({ settings, busy, onSave }) => {
  const [scanMaxBytes, setScanMaxBytes] = useState(settings.scan_max_bytes);
  const [overLimitAction, setOverLimitAction] = useState(settings.over_limit_action);
  const [onError, setOnError] = useState(settings.on_error);

  const dirty = Number(scanMaxBytes) !== settings.scan_max_bytes
    || overLimitAction !== settings.over_limit_action
    || onError !== settings.on_error;

  return (
    <div className="grid grid-cols-1 gap-4 md:grid-cols-[260px_180px_164px_112px] md:items-end">
      <ScanSettingField label="扫描上限（字节）">
        <Input
          type="number"
          min="1024"
          value={scanMaxBytes}
          onChange={(e) => setScanMaxBytes(e.target.value)}
          className="h-10 font-semibold tabular-nums text-fg"
        />
      </ScanSettingField>
      <ScanSettingField label="超限策略">
        <CustomSelect
          size="md"
          options={PRIVACY_OVER_LIMIT_OPTIONS}
          value={overLimitAction}
          onChange={setOverLimitAction}
          className="w-full [&>button]:h-10 [&>button]:w-full"
        />
      </ScanSettingField>
      <ScanSettingField label="出错策略">
        <CustomSelect
          size="md"
          options={PRIVACY_ON_ERROR_OPTIONS}
          value={onError}
          onChange={setOnError}
          className="w-full [&>button]:h-10 [&>button]:w-full"
        />
      </ScanSettingField>
      <div className="space-y-1.5">
        <div className="h-5" aria-hidden="true" />
        <div className="h-10">
          <Button
            size="md"
            variant="secondary"
            icon={Save}
            disabled={!dirty || busy}
            className="h-10 w-full"
            onClick={() => onSave({
              scan_max_bytes: Number(scanMaxBytes),
              over_limit_action: overLimitAction,
              on_error: onError
            })}
          >
            保存
          </Button>
        </div>
      </div>
    </div>
  );
};

const StatusBand = ({ settings, stats }) => {
  const { formatTimestamp } = useTimezone();
  const modeLabel = PRIVACY_MODE_OPTIONS.find((opt) => opt.value === settings.mode)?.label || settings.mode;
  const items = [
    { label: '当前模式', value: modeLabel },
    { label: '已启用规则', value: settings.enabled_rules },
    { label: '快照版本', value: `v${settings.version}` },
    { label: '扫描上限', value: formatScanBytes(settings.scan_max_bytes) },
    { label: '最近更新', value: settings.updated_at ? formatTimestamp(settings.updated_at) : '-' }
  ];
  if (stats && stats.scan_count > 0) {
    items.push({ label: '累计扫描/命中', value: `${stats.scan_count} / ${stats.hit_count}` });
  }
  if (stats && stats.truncated_count > 0) {
    items.push({ label: '扫描截断', value: stats.truncated_count, tone: 'warning' });
  }
  if (stats && stats.blocked_count > 0) {
    items.push({ label: '已阻断', value: stats.blocked_count, tone: 'danger' });
  }

  const valueClassName = (tone) => {
    if (tone === 'warning') return 'text-warn bg-warn-soft px-1.5 py-0.5 rounded-md';
    if (tone === 'danger') return 'text-danger bg-danger-soft px-1.5 py-0.5 rounded-md';
    return 'text-fg-body';
  };

  return (
    <div className="flex flex-wrap items-center gap-x-6 gap-y-2 px-4 py-3 bg-surface border border-line rounded-xl">
      {items.map((item) => (
        <div key={item.label} className="flex items-baseline gap-1.5">
          <span className="text-xs text-fg-subtle">{item.label}</span>
          <span className={`text-sm font-medium tabular-nums ${valueClassName(item.tone)}`}>{item.value}</span>
        </div>
      ))}
      {settings.status === 'degraded' && (
        <span
          className="flex items-center gap-1 text-xs text-warn bg-warn-soft px-2 py-1 rounded-lg"
          title={settings.compile_error}
        >
          <AlertTriangle size={13} />
          部分规则未激活
        </span>
      )}
    </div>
  );
};

const PRIVACY_TABS = [
  { id: 'exact', label: '本地敏感值' },
  { id: 'builtin', label: '内置规则' },
  { id: 'advanced', label: '高级预设' }
];

const PrivacyTabs = ({ activeTab, onChange }) => (
  <div className="inline-flex rounded-lg border border-line bg-surface p-0.5 shadow-sm">
    {PRIVACY_TABS.map((tab) => (
      <button
        key={tab.id}
        type="button"
        onClick={() => onChange(tab.id)}
        className={`rounded-md px-3 py-1.5 text-sm font-medium transition-colors ${
          activeTab === tab.id
            ? 'bg-inverted text-fg-inverted shadow-sm'
            : 'text-fg-muted hover:text-fg'
        }`}
      >
        {tab.label}
      </button>
    ))}
  </div>
);

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
    return <ErrorMessage title="隐私保护加载失败" message={error || '服务未启用'} onRetry={reloadAll} />;
  }

  return (
    <div className="space-y-4 motion-page">
      {/* 顶部工具条 */}
      <div className="flex flex-wrap items-center gap-3">
        <h2 className="text-xl font-bold text-fg flex items-center gap-2">
          <Shield size={20} className="text-accent" />
          隐私保护
        </h2>
        <span className="text-xs text-fg-subtle">出站请求保护：转发前按规则检测/脱敏请求内容</span>
        <div className="ml-auto flex items-center gap-2">
          <ModeSegmentedControl mode={settings.mode} busy={busy} onChange={handleModeChange} />
          <Button size="sm" variant="secondary" icon={FlaskConical} onClick={() => setTestDrawerOpen(true)}>
            规则测试
          </Button>
          <Button size="sm" variant="secondary" icon={Download} onClick={handleExport} disabled={busy}>
            导出
          </Button>
          {activeTab === 'advanced' && (
            <>
              <Button size="sm" variant="secondary" icon={PackagePlus} onClick={() => setPresetOpen(true)} disabled={busy}>
                导入预设
              </Button>
              <Button size="sm" icon={Plus} onClick={() => openDrawer(createEmptyPrivacyRuleForm())} disabled={busy}>
                新增规则
              </Button>
            </>
          )}
        </div>
      </div>

      {/* 状态带 */}
      <StatusBand settings={settings} stats={stats} />

      {/* 扫描参数 */}
      <div className="px-4 py-3 bg-surface border border-line rounded-xl">
        <ScanSettingsBar
          key={`${settings.scan_max_bytes}|${settings.over_limit_action}|${settings.on_error}|${settings.version}`}
          settings={settings}
          busy={busy}
          onSave={handleScanSettingsSave}
        />
      </div>

      {actionError && (
        <p className="text-sm text-danger break-all">{actionError}</p>
      )}

      {/* 主体：规则配置与管理 */}
      <div className="space-y-3">
        <PrivacyTabs activeTab={activeTab} onChange={setActiveTab} />
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
          <PrivacyBuiltinRulesPanel
            rules={builtinRules}
            busy={busy}
            onToggle={handleToggle}
            onEdit={(rule) => openDrawer(ruleToForm(rule))}
          />
        )}
        {activeTab === 'advanced' && (
          <>
            <div className="rounded-lg border border-warn-line bg-warn-soft px-3 py-2 text-xs leading-5 text-warn">
              这些规则可能误伤代码、日志、测试数据和工具输出。建议先使用“仅检测”，确认命中质量后再开启“脱敏转发”。
            </div>
            <PrivacyRulesToolbar
              filters={filters}
              onChange={setFilters}
              total={advancedRules.length}
              filtered={filteredRules.length}
            />
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
          </>
        )}
      </div>

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
