import { useCallback, useEffect, useState } from 'react';
import { createPortal } from 'react-dom';
import { AlertCircle, RefreshCw, RotateCcw, Save } from 'lucide-react';
import { FormSection } from '@components/desktop/FormSection.jsx';
import { ToolbarButton } from '@components/layout/ToolbarButton.jsx';
import { ErrorMessage, LoadingSpinner } from '@components/ui';
import useConfirm from '@hooks/useConfirm.js';
import { useNotice } from '@pages/account-pool/hooks';
import SettingItem from '@pages/settings/components/SettingItem.jsx';
import { resolveErrorMessage } from '@utils/errors.js';
import {
  batchUpdateSettings, getSettingsByCategory, isWailsEnvironment, resetCategorySettings
} from '@utils/wailsApi.js';
import {
  applySettingChange, buildImageSettingUpdates, groupImageSettings, IMAGE_GENERATION_CATEGORY
} from './imageSettingsView.js';

const CodexImagesPage = ({ active = true, headerTarget = null }) => {
  const isWails = isWailsEnvironment();
  const [settings, setSettings] = useState([]);
  const [changes, setChanges] = useState({});
  const [loading, setLoading] = useState(isWails);
  const [saving, setSaving] = useState(false);
  const [loadError, setLoadError] = useState(null);
  const [saveError, setSaveError] = useState(null);
  const { confirm, confirmDialog } = useConfirm();
  const { showNotice } = useNotice({ enabled: active });

  const loadSettings = useCallback(async ({ silent = false } = {}) => {
    if (!isWails) return false;
    if (!silent) setLoading(true);
    setLoadError(null);
    try {
      setSettings(await getSettingsByCategory(IMAGE_GENERATION_CATEGORY));
      setChanges({});
      setSaveError(null);
      return true;
    } catch (err) {
      setLoadError(resolveErrorMessage(err, '加载图像生成配置失败'));
      return false;
    } finally {
      setLoading(false);
    }
  }, [isWails]);

  useEffect(() => {
    loadSettings();
  }, [loadSettings]);

  const changeCount = Object.keys(changes).length;
  const handleChange = (category, key, value) => {
    const setting = settings.find((item) => item.category === category && item.key === key);
    if (!setting) return;
    setChanges((current) => applySettingChange(current, setting, value));
    setSaveError(null);
  };
  const currentValue = (setting) => Object.hasOwn(changes, setting.key) ? changes[setting.key] : setting.value;

  const handleReload = async () => {
    if (changeCount > 0 && !(await confirm({
      title: '放弃未保存的修改', message: '重新载入会丢弃当前未保存的修改。', confirmText: '放弃并重新载入', danger: true
    }))) return;
    await loadSettings();
  };

  const handleReset = async () => {
    if (!(await confirm({
      title: '恢复默认设置', message: '图像生成将被关闭，API Key 会被清除，其余各项恢复默认值。', confirmText: '恢复默认', danger: true
    }))) return;
    setSaving(true);
    setSaveError(null);
    try {
      await resetCategorySettings(IMAGE_GENERATION_CATEGORY);
      if (await loadSettings({ silent: true })) showNotice('success', '图像生成已恢复默认值');
    } catch (err) {
      setSaveError(resolveErrorMessage(err, '恢复默认失败'));
    } finally {
      setSaving(false);
    }
  };

  const handleSave = async () => {
    if (changeCount === 0) return;
    setSaving(true);
    setSaveError(null);
    try {
      await batchUpdateSettings({ settings: buildImageSettingUpdates(changes) });
      if (await loadSettings({ silent: true })) showNotice('success', '图像生成设置已保存并生效');
    } catch (err) {
      setSaveError(resolveErrorMessage(err, '保存失败'));
    } finally {
      setSaving(false);
    }
  };

  if (!isWails) return <p className="mt-5 text-xs text-fg-muted">图像生成配置仅在桌面端可用</p>;

  const toolbar = (
    <div className="flex items-center gap-1">
      <ToolbarButton icon={RefreshCw} title="重新载入" onClick={handleReload} disabled={loading || saving} />
      <ToolbarButton icon={RotateCcw} label="恢复默认" onClick={handleReset} disabled={loading || saving} />
      <ToolbarButton
        variant="primary"
        icon={Save}
        label={changeCount > 0 ? `保存 (${changeCount})` : '保存'}
        loading={saving}
        disabled={loading || saving || changeCount === 0}
        onClick={handleSave}
      />
    </div>
  );

  return (
    <>
      {headerTarget && active && createPortal(toolbar, headerTarget)}
      {!headerTarget && <div className="mt-4 flex justify-end">{toolbar}</div>}
      {saveError && (
        <div role="alert" className="selectable mt-4 flex items-center gap-2 rounded-lg border border-danger-line bg-danger-soft px-3 py-2 text-xs text-danger">
          <AlertCircle size={14} />{saveError}
        </div>
      )}
      {loadError
        ? <ErrorMessage title="图像生成配置加载失败" message={loadError} onRetry={loadSettings} />
        : loading
          ? <LoadingSpinner text="加载图像生成配置..." />
          : groupImageSettings(settings).map((group) => (
            <FormSection key={group.id} title={group.title} description={group.description}>
              {group.items.map((setting) => (
                <SettingItem key={setting.key} setting={setting} value={currentValue(setting)} onChange={handleChange} disabled={saving} />
              ))}
            </FormSection>
          ))}
      {confirmDialog}
    </>
  );
};

export default CodexImagesPage;
