import { getWailsMethod, initWails, isWailsEnvironment } from '../../../utils/wailsApi.js';

export const CONFIG_IMPORT_MAX_FILES = 32;
export const CONFIG_IMPORT_MAX_FILE_BYTES = 2 * 1024 * 1024;
export const CONFIG_IMPORT_MAX_TOTAL_BYTES = 8 * 1024 * 1024;

async function invoke(name, ...args) {
  if (!isWailsEnvironment()) throw new Error('配置文件导入仅在 Sinuo 桌面端可用');
  if (!await initWails()) throw new Error('桌面连接尚未就绪，请重试');
  return getWailsMethod(name)(...args);
}

export const previewConfigImport = (files) => invoke('PreviewPrivacyConfigImport', files);
export const previewConfigSourceImport = (sourceID) => invoke('PreviewPrivacyConfigSourceImport', sourceID);
export const commitConfigImport = (id, selected) => invoke('CommitPrivacyConfigImport', id, selected);
export const cancelConfigImport = (id) => invoke('CancelPrivacyConfigImport', id);

// 文件内容只留在这次调用中；不进入 React state、浏览器存储或错误消息。
export async function readConfigImportFiles(fileList) {
  const files = Array.from(fileList || []);
  if (files.length === 0 || files.length > CONFIG_IMPORT_MAX_FILES) throw new Error('请选择 1–32 个配置文件');
  if (files.some((file) => file.size > CONFIG_IMPORT_MAX_FILE_BYTES)) throw new Error('单个配置文件不能超过 2 MiB');
  if (files.reduce((total, file) => total + file.size, 0) > CONFIG_IMPORT_MAX_TOTAL_BYTES) throw new Error('配置文件合计不能超过 8 MiB');
  const result = [];
  for (const file of files) {
    const bytes = await file.arrayBuffer();
    let content;
    try { content = new TextDecoder('utf-8', { fatal: true }).decode(bytes); }
    catch { throw new Error('配置文件必须使用 UTF-8 编码；请转换编码后重新导入'); }
    result.push({ name: file.name, content });
  }
  return result;
}

export const defaultConfigSelection = (candidates = []) => new Set(candidates.filter((c) => c.selectable && c.selected).map((c) => c.id));

export function selectedConfigIDs(candidates, selected) {
  return candidates.filter((c) => c.selectable && selected.has(c.id)).map((c) => c.id);
}

// 处理窗口在文件读取或预览返回前卸载的情况，防止留下无人持有的预览。
export function createConfigImportSession(api = { preview: previewConfigImport, previewSource: previewConfigSourceImport, commit: commitConfigImport, cancel: cancelConfigImport }, sourceID = null) {
  let id = null;
  let disposed = false;
  return {
    async preview(files) {
      if (disposed) return null;
      if (id) { await api.cancel(id); id = null; }
      const result = sourceID === null ? await api.preview(files) : await api.previewSource(sourceID);
      if (disposed) { await api.cancel(result.id); return null; }
      id = result.id;
      return result;
    },
    async commit(selected) {
      if (!id || disposed) throw new Error('导入预览已失效，请重新生成预览');
      const result = await api.commit(id, selected);
      id = null;
      return result;
    },
    async dispose() {
      disposed = true;
      if (id) { await api.cancel(id); id = null; }
    }
  };
}
