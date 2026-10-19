// ============================================
// Settings 页面 - 系统设置管理（可编辑）
// v5.1.0 (2025-12-08)
// ============================================

import { useState, useEffect, useCallback, useMemo, useRef } from 'react';
import {
  Settings,
  RefreshCw,
  Save,
  AlertCircle,
  CheckCircle2,
  Server,
  HeartPulse,
  Clock,
  Lock,
  Hash,
  Archive,
  TerminalSquare,
  Monitor,
  KeyRound,
  Download
} from 'lucide-react';
import { LoadingSpinner, ErrorMessage } from '@components/ui';
import { PageToolbar } from '@components/layout/AppToolbar.jsx';
import { ToolbarButton, ToolbarSpacer } from '@components/layout/ToolbarButton.jsx';
import { AccessShortcutCard, SettingItem, SettingsSection, PortInfo } from './components';
import AppUpdatePanel from '@components/app-update/AppUpdatePanel.jsx';
import useConfirm from '@hooks/useConfirm.js';
import { SUBSCRIPTION_PROXY_CONFIG_CATEGORY } from '@components/codex-access/subscriptionProxyViewModel.js';
import { IMAGE_GENERATION_CATEGORY } from '@pages/codex/images/imageSettingsView.js';
import {
  isWailsEnvironment,
  getSettingCategories,
  getAllSettings,
  batchUpdateSettings,
  resetCategorySettings,
  getPortInfo,
  subscribeToEvent
} from '@utils/wailsApi.js';
import { resolveErrorMessage } from '@utils/errors.js';
import {
  fetchSettingCategories,
  fetchAllSettings,
  updateSettings as apiUpdateSettings,
  resetSettings as apiResetSettings,
  fetchPortInfo
} from '@utils/api.js';

// 左侧目录：分组锚点 id 与滚动联动参数
const ACCESS_SECTION_ID = 'settings-access';
const sectionIdOf = (categoryName) => `settings-${categoryName}`;
const SECTION_ACTIVATE_OFFSET = 32;
const SECTION_SCROLL_MARGIN = 8;

const MESSAGE_TONES = {
  success: 'border-success-line bg-success-soft text-success',
  error: 'border-danger-line bg-danger-soft text-danger',
  info: 'border-line bg-surface text-fg-body'
};

// 分类名称到 Lucide 图标的映射
const CATEGORY_ICONS = {
  server: Server,
  health: HeartPulse,
  request: Clock,
  auth: Lock,
  token_counting: Hash,
  retention: Archive,
  codex_identity: TerminalSquare,
  desktop: Monitor,
  app_update: Download
};

// ============================================
// Settings 页面主组件
// ============================================

const SettingsPage = ({ onNavigateToCodexAccess, onNavigateToClaudeAccess, focusSection, onFocusSectionHandled, appUpdate }) => {
  const [categories, setCategories] = useState([]);
  const [settings, setSettings] = useState([]);
  const { confirm, confirmDialog } = useConfirm();
  const [portInfo, setPortInfo] = useState(null);
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState(null);
  const [changes, setChanges] = useState({}); // 跟踪变更: { "category.key": newValue }
  const [saveMessage, setSaveMessage] = useState(null);

  const isWails = isWailsEnvironment();
  const scrollRef = useRef(null);
  const [activeSection, setActiveSection] = useState(ACCESS_SECTION_ID);

  // 滚动联动：顶部已越过的最后一个分组即当前分组
  const handleScroll = useCallback(() => {
    const container = scrollRef.current;
    if (!container) return;
    let current = ACCESS_SECTION_ID;
    for (const section of container.querySelectorAll('section[id]')) {
      if (section.offsetTop - container.scrollTop <= SECTION_ACTIVATE_OFFSET) current = section.id;
    }
    setActiveSection(current);
  }, []);

  const scrollToSection = useCallback((id) => {
    const container = scrollRef.current;
    const section = container?.querySelector(`#${CSS.escape(id)}`);
    if (!section) return;
    setActiveSection(id);
    container.scrollTo({ top: section.offsetTop - SECTION_SCROLL_MARGIN, behavior: 'smooth' });
  }, []);

  // 加载设置数据
  const loadSettings = useCallback(async () => {
    try {
      setLoading(true);
      setError(null);
      setSaveMessage(null);

      let categoriesData, settingsData, portData;

      if (isWails) {
        [categoriesData, settingsData, portData] = await Promise.all([
          getSettingCategories(),
          getAllSettings(),
          getPortInfo()
        ]);
      } else {
        [categoriesData, settingsData, portData] = await Promise.all([
          fetchSettingCategories(),
          fetchAllSettings(),
          fetchPortInfo()
        ]);
      }

      setCategories((categoriesData || []).filter((category) => category.name !== IMAGE_GENERATION_CATEGORY));
      // Codex 订阅代理是受保护分类（revision CAS + 一次性密钥），只走专用卡片，
      // 不能进入通用表单，否则会被当成普通设置项展示或随批量保存下发。
      // 图像生成在 Codex 管理 → 图像生成页签维护。
      setSettings((settingsData || []).filter((item) => item.category !== SUBSCRIPTION_PROXY_CONFIG_CATEGORY)
        .filter((item) => item.category !== IMAGE_GENERATION_CATEGORY));
      setPortInfo(portData);
      setChanges({});
    } catch (err) {
      setError(resolveErrorMessage(err, '加载设置失败'));
    } finally {
      setLoading(false);
    }
  }, [isWails]);

  useEffect(() => {
    loadSettings();
  }, [loadSettings]);

  useEffect(() => {
    if (loading || !focusSection || !categories.some((category) => category.name === focusSection)) return undefined;
    const frame = window.requestAnimationFrame(() => {
      scrollToSection(sectionIdOf(focusSection));
      onFocusSectionHandled?.();
    });
    return () => window.cancelAnimationFrame(frame);
  }, [loading, categories, focusSection, scrollToSection, onFocusSectionHandled]);

  useEffect(() => {
    if (!isWails) return undefined;
    let cancelled = false;
    const unsubscribe = subscribeToEvent('desktop:settings-update', () => {
      getAllSettings().then((updated) => {
        if (cancelled) return;
        // 只同步托盘设置，保留设置页尚未保存的其他编辑。
        setSettings((current) => current.map((item) => item.category === 'desktop'
          ? (updated.find((next) => next.category === item.category && next.key === item.key) || item)
          : item));
      }).catch((error) => { if (!cancelled) setSaveMessage({ type: 'error', text: resolveErrorMessage(error) }); });
    });
    return () => { cancelled = true; unsubscribe(); };
  }, [isWails]);

  // 按分类组织设置
  const settingsByCategory = useMemo(() => {
    const grouped = {};
    for (const setting of settings) {
      if (!grouped[setting.category]) {
        grouped[setting.category] = [];
      }
      grouped[setting.category].push(setting);
    }
    // 按 display_order 排序
    for (const category in grouped) {
      grouped[category].sort((a, b) => a.display_order - b.display_order);
    }
    return grouped;
  }, [settings]);

  // 处理设置变更
  const handleSettingChange = useCallback((category, key, value) => {
    const changeKey = `${category}.${key}`;
    setChanges(prev => ({
      ...prev,
      [changeKey]: value
    }));
  }, []);

  // 获取当前值（优先使用变更值）
  const getCurrentValue = useCallback((setting) => {
    const changeKey = `${setting.category}.${setting.key}`;
    if (changeKey in changes) {
      return changes[changeKey];
    }
    return setting.value;
  }, [changes]);

  // 保存所有变更
  const handleSave = useCallback(async () => {
    if (Object.keys(changes).length === 0) {
      setSaveMessage({ type: 'info', text: '没有需要保存的变更' });
      return;
    }

    try {
      setSaving(true);
      setSaveMessage(null);

      // 构建更新数据
      const updateData = Object.entries(changes).map(([key, value]) => {
        const [category, settingKey] = key.split('.');
        return { category, key: settingKey, value: String(value) };
      });

      if (isWails) {
        await batchUpdateSettings({ settings: updateData });
      } else {
        await apiUpdateSettings(updateData);
      }

      // 检查是否有需要重启的设置
      const requiresRestart = updateData.some(item => {
        const setting = settings.find(s => s.category === item.category && s.key === item.key);
        return setting?.requires_restart;
      });

      // 先重新加载再写提示：loadSettings 开头会清空提示，顺序反过来提示永远显示不出来
      await loadSettings();
      setSaveMessage({
        type: 'success',
        text: requiresRestart
          ? '设置已保存。部分设置需要重启应用后生效。'
          : '设置已保存并生效'
      });
    } catch (err) {
      setSaveMessage({ type: 'error', text: resolveErrorMessage(err, '保存失败') });
    } finally {
      setSaving(false);
    }
  }, [changes, isWails, settings, loadSettings]);

  // 重置分类设置
  const handleResetCategory = useCallback(async (category, label = category) => {
    if (!(await confirm({ title: '恢复默认设置', message: `确定要将「${label}」分组的设置恢复为默认值吗？`, confirmText: '恢复默认', danger: true }))) {
      return;
    }

    try {
      setSaving(true);
      setSaveMessage(null);

      if (isWails) {
        await resetCategorySettings(category);
      } else {
        await apiResetSettings(category);
      }

      await loadSettings();
      setSaveMessage({ type: 'success', text: `「${label}」已恢复默认值` });
    } catch (err) {
      setSaveMessage({ type: 'error', text: resolveErrorMessage(err, '重置失败') });
    } finally {
      setSaving(false);
    }
  }, [confirm, isWails, loadSettings]);

  // 判断是否有未保存的变更
  const hasChanges = Object.keys(changes).length > 0;
  const changeCount = Object.keys(changes).length;

  // 左侧目录与右侧分组同序：客户端接入 → 服务端口 → 其余分类
  const serverCategory = categories.find((category) => category.name === 'server');
  const otherCategories = categories.filter((category) => category.name !== 'server');
  const sections = [
    { id: ACCESS_SECTION_ID, label: '客户端接入', icon: KeyRound },
    ...(serverCategory ? [serverCategory] : []).concat(otherCategories).map((category) => ({
      id: sectionIdOf(category.name),
      label: category.label,
      icon: CATEGORY_ICONS[category.name] || Settings
    }))
  ];

  const renderItems = (categoryName) => settingsByCategory[categoryName]?.map((setting) => (
    <SettingItem
      key={`${setting.category}.${setting.key}`}
      setting={setting}
      value={getCurrentValue(setting)}
      onChange={handleSettingChange}
      disabled={saving}
    />
  ));

  return (
    <div className="motion-page flex min-h-0 flex-1 bg-pane">
      <PageToolbar subtitle={hasChanges ? `${changeCount} 项更改未保存` : '大部分设置保存后即时生效'}>
        <ToolbarSpacer />
        <ToolbarButton icon={RefreshCw} title="重新载入" onClick={loadSettings} disabled={saving || loading} />
        <ToolbarButton
          variant="primary"
          icon={Save}
          label={hasChanges ? `保存 (${changeCount})` : '保存'}
          onClick={handleSave}
          disabled={saving || loading || !hasChanges}
          loading={saving}
        />
      </PageToolbar>

      {/* 分组目录：点击滚到对应分组，滚动时高亮当前分组 */}
      <nav aria-label="设置分组" className="w-[172px] shrink-0 overflow-y-auto border-r border-line px-2 py-3">
        {sections.map(({ id, label, icon: Icon }) => (
          <button
            key={id}
            type="button"
            onClick={() => scrollToSection(id)}
            aria-current={activeSection === id ? 'true' : undefined}
            className={`flex h-7 w-full items-center gap-2 rounded-md px-2 text-left text-[12.5px] ${
              activeSection === id ? 'bg-sidebar-sel font-medium text-fg' : 'text-fg-body'
            }`}
          >
            <Icon size={14} strokeWidth={1.75} className="sidebar-icon shrink-0" />
            <span className="truncate">{label}</span>
          </button>
        ))}
      </nav>

      <div ref={scrollRef} onScroll={handleScroll} className="relative min-w-0 flex-1 overflow-y-auto px-7 pb-12">
        <div className="mx-auto max-w-[640px]">
          {saveMessage && (
            <div
              role={saveMessage.type === 'error' ? 'alert' : 'status'}
              className={`selectable mt-4 flex items-center gap-2 rounded-lg border px-3 py-2 text-xs ${MESSAGE_TONES[saveMessage.type]}`}
            >
              {saveMessage.type === 'success' ? <CheckCircle2 size={14} /> : <AlertCircle size={14} />}
              {saveMessage.text}
            </div>
          )}

          <SettingsSection id={ACCESS_SECTION_ID} title="客户端接入" description="入口的启停、端口与接入模式在各自的管理页">
            <AccessShortcutCard
              title="Codex 客户端接入"
              description="订阅代理的启停、模式、监听地址与一次性接入密钥"
              onNavigate={onNavigateToCodexAccess}
            />
            <AccessShortcutCard
              title="Claude Code 客户端接入"
              description="入口的启停、端口与接入模式（订阅直通 / 端点）"
              onNavigate={onNavigateToClaudeAccess}
            />
          </SettingsSection>

          {error
            ? <div className="mt-5"><ErrorMessage title="设置加载失败" message={error} onRetry={loadSettings} /></div>
            : loading
              ? <LoadingSpinner text="加载设置..." />
              : (
                <>
                  {serverCategory && (
                    <SettingsSection
                      id={sectionIdOf('server')}
                      title={serverCategory.label}
                      description={serverCategory.description}
                      onReset={() => handleResetCategory('server', serverCategory.label)}
                      resetDisabled={saving}
                    >
                      <PortInfo portInfo={portInfo} loading={loading} />
                      {renderItems('server')}
                    </SettingsSection>
                  )}

                  {otherCategories.map((category) => (
                    <SettingsSection
                      key={category.name}
                      id={sectionIdOf(category.name)}
                      title={category.label}
                      description={category.description}
                      onReset={() => handleResetCategory(category.name, category.label)}
                      resetDisabled={saving}
                    >
                      {category.name === 'app_update' && appUpdate && <AppUpdatePanel update={appUpdate} />}
                      {renderItems(category.name)}
                      {(!settingsByCategory[category.name] || settingsByCategory[category.name].length === 0) && (
                        <div className="px-3.5 py-3 text-center text-xs text-fg-subtle">暂无设置项</div>
                      )}
                    </SettingsSection>
                  ))}
                </>
              )}
        </div>
      </div>
      {confirmDialog}
    </div>
  );
};

export default SettingsPage;
