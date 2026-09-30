// ============================================
// Settings 页面 - 系统设置管理（可编辑）
// v5.1.0 (2025-12-08)
// ============================================

import { useState, useEffect, useCallback, useMemo } from 'react';
import {
  Settings,
  RefreshCw,
  Save,
  AlertCircle,
  CheckCircle2,
  Server,
  Shuffle,
  RotateCcw,
  HeartPulse,
  Clock,
  Lock,
  Hash,
  Archive,
  ImageIcon,
  TerminalSquare
} from 'lucide-react';
import { Button, LoadingSpinner, ErrorMessage } from '@components/ui';
import { AccessShortcutCard, SettingItem, SettingsSection, PortInfo } from './components';
import useConfirm from '@hooks/useConfirm.js';
import { SUBSCRIPTION_PROXY_CONFIG_CATEGORY } from '@components/codex-access/subscriptionProxyViewModel.js';
import {
  isWailsEnvironment,
  getSettingCategories,
  getAllSettings,
  batchUpdateSettings,
  resetCategorySettings,
  getPortInfo
} from '@utils/wailsApi.js';
import {
  fetchSettingCategories,
  fetchAllSettings,
  updateSettings as apiUpdateSettings,
  resetSettings as apiResetSettings,
  fetchPortInfo
} from '@utils/api.js';

// 分类名称到 Lucide 图标的映射
const CATEGORY_ICONS = {
  server: Server,
  strategy: Shuffle,
  retry: RotateCcw,
  health: HeartPulse,
  request: Clock,
  auth: Lock,
  token_counting: Hash,
  retention: Archive,
  image_generation: ImageIcon,
  codex_identity: TerminalSquare
};

// ============================================
// Settings 页面主组件
// ============================================

const SettingsPage = ({ onNavigateToCodexAccess, onNavigateToClaudeAccess }) => {
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

      setCategories(categoriesData || []);
      // Codex 订阅代理是受保护分类（revision CAS + 一次性密钥），只走专用卡片，
      // 不能进入通用表单，否则会被当成普通设置项展示或随批量保存下发。
      setSettings((settingsData || []).filter((item) => item.category !== SUBSCRIPTION_PROXY_CONFIG_CATEGORY));
      setPortInfo(portData);
      setChanges({});
    } catch (err) {
      setError(err.message || '加载设置失败');
    } finally {
      setLoading(false);
    }
  }, [isWails]);

  useEffect(() => {
    loadSettings();
  }, [loadSettings]);

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

      setSaveMessage({
        type: 'success',
        text: requiresRestart
          ? '设置已保存。部分设置需要重启应用后生效。'
          : '设置已保存并生效'
      });

      // 重新加载设置
      await loadSettings();
    } catch (err) {
      setSaveMessage({ type: 'error', text: err.message || '保存失败' });
    } finally {
      setSaving(false);
    }
  }, [changes, isWails, settings, loadSettings]);

  // 重置分类设置
  const handleResetCategory = useCallback(async (category) => {
    if (!(await confirm({ title: '重置分类设置', message: `确定要将「${category}」分类的设置重置为默认值吗？`, confirmText: '确认重置', danger: true }))) {
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

      setSaveMessage({ type: 'success', text: `${category} 设置已重置为默认值` });
      await loadSettings();
    } catch (err) {
      setSaveMessage({ type: 'error', text: err.message || '重置失败' });
    } finally {
      setSaving(false);
    }
  }, [confirm, isWails, loadSettings]);

  // 判断是否有未保存的变更
  const hasChanges = Object.keys(changes).length > 0;

  return (
    <div className="space-y-8 motion-page">
      {/* 页面标题 */}
      <div className="flex justify-between items-end">
        <div>
          <h1 className="text-2xl font-bold text-fg flex items-center gap-3">
            <div className="p-2 bg-inverted rounded-lg text-fg-inverted shadow-lg">
              <Settings size={20} />
            </div>
            <div>
              <div>系统设置</div>
              <p className="text-sm text-fg-muted font-normal mt-1">
                配置系统运行参数，大部分设置即时生效
              </p>
            </div>
          </h1>
        </div>
        <div className="flex items-center gap-3">
          <Button
            icon={RefreshCw}
            variant="secondary"
            onClick={loadSettings}
            disabled={saving || loading}
          >
            刷新
          </Button>
          <Button
            icon={Save}
            variant="primary"
            onClick={handleSave}
            disabled={saving || loading || !hasChanges}
            loading={saving}
          >
            {hasChanges ? `保存 (${Object.keys(changes).length})` : '保存'}
          </Button>
        </div>
      </div>

      {/* 保存消息提示 */}
      {saveMessage && (
        <div className={`
          flex items-center gap-2 px-4 py-3 rounded-lg text-sm
          ${saveMessage.type === 'success' ? 'bg-success-soft text-success border border-success-line' : ''}
          ${saveMessage.type === 'error' ? 'bg-danger-soft text-danger border border-danger-line' : ''}
          ${saveMessage.type === 'info' ? 'bg-surface-sub text-fg-body border border-line' : ''}
        `}>
          {saveMessage.type === 'success' && <CheckCircle2 size={16} />}
          {saveMessage.type === 'error' && <AlertCircle size={16} />}
          {saveMessage.text}
        </div>
      )}

      {/* 未保存变更提示 */}
      {hasChanges && (
        <div className="flex items-center gap-2 px-4 py-3 rounded-lg text-sm bg-warn-soft text-warn border border-warn-line">
          <AlertCircle size={16} />
          您有 {Object.keys(changes).length} 项未保存的变更
        </div>
      )}

      <div className="grid grid-cols-1 gap-4 lg:grid-cols-2">
        <AccessShortcutCard
          title="Codex 客户端接入"
          description="订阅代理的启停、模式、监听地址与一次性接入密钥已移至 Codex 管理。"
          onNavigate={onNavigateToCodexAccess}
        />
        <AccessShortcutCard
          title="Claude Code 客户端接入"
          description="入口的启停、端口与接入模式（订阅直通 / 端点）已移至 Claude 管理。"
          onNavigate={onNavigateToClaudeAccess}
        />
      </div>

      {error
        ? <ErrorMessage title="设置加载失败" message={error} onRetry={loadSettings} />
        : loading
          ? <LoadingSpinner text="加载设置..." />
          : (
            <>
              {/* 端口信息 (服务端口分类的特殊处理) */}
              {categories.find(c => c.name === 'server') && (
                <SettingsSection
                  title="服务端口"
                  icon={Server}
                  description="API 服务端口配置"
                  onReset={() => handleResetCategory('server')}
                  resetDisabled={saving}
                >
                  <PortInfo portInfo={portInfo} loading={loading} />
                  <div className="mt-4 pt-4 border-t border-line-soft">
                    {settingsByCategory['server']?.map(setting => (
                      <SettingItem
                        key={`${setting.category}.${setting.key}`}
                        setting={setting}
                        value={getCurrentValue(setting)}
                        onChange={handleSettingChange}
                        disabled={saving}
                      />
                    ))}
                  </div>
                </SettingsSection>
              )}

              {/* 其他设置分类 (两列布局) */}
              <div className="grid grid-cols-1 lg:grid-cols-2 gap-6">
                {categories
                  .filter(c => c.name !== 'server')
                  .map(category => (
                    <SettingsSection
                      key={category.name}
                      title={category.label}
                      icon={CATEGORY_ICONS[category.name] || Settings}
                      description={category.description}
                      onReset={() => handleResetCategory(category.name)}
                      resetDisabled={saving}
                    >
                      {settingsByCategory[category.name]?.map(setting => (
                        <SettingItem
                          key={`${setting.category}.${setting.key}`}
                          setting={setting}
                          value={getCurrentValue(setting)}
                          onChange={handleSettingChange}
                          disabled={saving}
                        />
                      ))}
                      {(!settingsByCategory[category.name] || settingsByCategory[category.name].length === 0) && (
                        <div className="text-sm text-fg-subtle text-center py-4">
                          暂无设置项
                        </div>
                      )}
                    </SettingsSection>
                  ))}
              </div>
            </>
          )}
      {confirmDialog}
    </div>
  );
};

export default SettingsPage;
