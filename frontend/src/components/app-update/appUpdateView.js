export const emptyAppUpdateStatus = () => ({
  current_version: '', commit: '', build_time: '', local_build: false,
  state: 'idle', update_available: false, latest_version: '', release_name: '', notes: '',
  published_at: '', release_url: '', checked_at: '', error: '',
  skipped: false, auto_check: false, revision: 0,
  install_supported: false, install_blocked_reason: '', progress_written: 0,
  progress_total: 0, ready_version: '', active_requests: 0, error_stage: ''
});

export const updateProgressPercent = (status) => status?.progress_total > 0
  ? Math.max(0, Math.min(100, Math.floor(100 * (status.progress_written || 0) / status.progress_total))) : null;

export const resolveUpdateBadge = (status) => {
  if (!status?.update_available || status.skipped || !status.latest_version) return null;
  switch (status.state) {
    case 'downloading': {
      const percent = updateProgressPercent(status);
      return percent === null ? '下载中' : `下载中 ${percent}%`;
    }
    case 'installing': return '正在安装';
    case 'ready': return '重启更新';
    case 'waiting_idle': return `等待 ${status.active_requests || 0} 个请求`;
    case 'restarting': return '正在重启';
    default: return `${status.latest_version} 可更新`;
  }
};

// 面板与后端保持同一操作边界，ready 后不再检查以免覆盖待安装版本。
export const resolveUpdateActions = (status) => {
  const busy = ['downloading', 'installing', 'ready', 'waiting_idle', 'restarting'].includes(status.state);
  let primary = null;
  if (['downloading', 'installing'].includes(status.state)) primary = 'cancelDownload';
  else if (status.state === 'ready') primary = 'restart';
  else if (status.state === 'waiting_idle') primary = 'cancelRestart';
  else if (status.state !== 'restarting' && status.state !== 'checking') {
    if (status.update_available && status.install_supported) primary = 'download';
    else if (status.release_url) primary = 'external';
  }
  return { primary, checkDisabled: busy || status.state === 'checking', skipDisabled: busy || status.state === 'checking' };
};

export const formatUpdateBytes = (value) => {
  const bytes = Math.max(0, Number(value) || 0);
  if (bytes < 1024) return `${bytes} B`;
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`;
  return `${(bytes / (1024 * 1024)).toFixed(1)} MB`;
};

export const canShowNotes = (status) => Boolean(status?.update_available && !status.skipped && status.latest_version);

export const cleanReleaseNotes = (notes = '') => String(notes)
  .replace(/^\s{0,3}#{1,6}\s+/gm, '')
  .replace(/\*\*(.*?)\*\*/g, '$1')
  .trim();

export const resolveUpdateStateLabel = (status) => {
  switch (status?.state) {
    case 'checking': return '检查中…';
    case 'up_to_date': return '已是最新版本';
    case 'available': return status.skipped ? `已跳过 ${status.latest_version}` : `发现新版本 ${status.latest_version}`;
    case 'downloading': return '正在下载更新…';
    case 'installing': return '正在校验并安装更新…';
    case 'ready': return `更新 ${status.ready_version} 已就绪`;
    case 'waiting_idle': return `正在等待 ${status.active_requests || 0} 个请求完成…`;
    case 'restarting': return '正在重启以完成更新…';
    case 'error': return status.error || '检查更新失败';
    default: return '尚未检查更新';
  }
};

// 首次读取、手动检查返回值和事件可能乱序到达，只有较新 revision 可替换当前状态。
export const acceptAppUpdateStatus = (current, incoming) => incoming && Number.isFinite(incoming.revision)
  && incoming.revision >= current.revision ? { ...emptyAppUpdateStatus(), ...incoming } : current;
