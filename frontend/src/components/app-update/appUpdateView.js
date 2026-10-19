export const emptyAppUpdateStatus = () => ({
  current_version: '', commit: '', build_time: '', local_build: false,
  state: 'idle', update_available: false, latest_version: '', release_name: '', notes: '',
  published_at: '', release_url: '', checked_at: '', error: '',
  skipped: false, auto_check: false, revision: 0
});

export const resolveUpdateBadge = (status) => status?.update_available && !status.skipped && status.latest_version
  ? `${status.latest_version} 可更新`
  : null;

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
    case 'error': return status.error || '检查更新失败';
    default: return '尚未检查更新';
  }
};

// 首次读取、手动检查返回值和事件可能乱序到达，只有较新 revision 可替换当前状态。
export const acceptAppUpdateStatus = (current, incoming) => incoming && Number.isFinite(incoming.revision)
  && incoming.revision >= current.revision ? { ...emptyAppUpdateStatus(), ...incoming } : current;
