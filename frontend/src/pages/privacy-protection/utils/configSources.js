import { getWailsMethod, initWails, isWailsEnvironment } from '../../../utils/wailsApi.js';

async function invoke(name, ...args) {
  if (!isWailsEnvironment()) throw new Error('配置来源自动同步仅在 Sinuo 桌面端可用');
  if (!await initWails()) throw new Error('桌面连接尚未就绪，请重试');
  return getWailsMethod(name)(...args);
}

export const configSourcePayload = (form) => ({
  id: form.id ?? 0,
  name: String(form.name ?? '').trim(),
  path: String(form.path ?? '').trim(),
  kind: form.kind,
  patterns: form.kind === 'directory' ? String(form.patterns ?? '').trim() : '',
  enabled: form.enabled === true
});

export const fetchConfigSources = () => invoke('ListPrivacyConfigSources');
export const saveConfigSource = (form) => invoke('SavePrivacyConfigSource', configSourcePayload(form));
export const deleteConfigSource = (id) => invoke('DeletePrivacyConfigSource', id);
export const syncConfigSources = (id = 0) => invoke('SyncPrivacyConfigSources', id);

export const configSourceStatus = (source) => {
  if (!source.enabled) return { label: '已暂停', tone: 'text-fg-muted' };
  if (source.last_error) return { label: '同步失败', tone: 'text-danger' };
  if (!source.last_success_at) return { label: '等待同步', tone: 'text-warn' };
  // 待确认 / 未收录数量由「识别结果」列展示，状态列不再重复。
  return { label: '已同步', tone: 'text-success' };
};
