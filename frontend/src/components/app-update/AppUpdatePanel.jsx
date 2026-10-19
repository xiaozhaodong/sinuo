import { ArrowUpRight, Download, RefreshCw } from 'lucide-react';
import { FormBlock, FormRow } from '@components/desktop/FormSection.jsx';
import { Button } from '@components/ui';
import { useTimezone } from '@contexts/TimezoneContext.jsx';
import { canShowNotes, cleanReleaseNotes, formatUpdateBytes, resolveUpdateActions, resolveUpdatePrimaryAction, resolveUpdateStateLabel, updateProgressPercent } from './appUpdateView.js';

const AppUpdatePanel = ({ update }) => {
  const { status, notice, check, skip, openRelease } = update;
  const { formatTimestamp } = useTimezone();
  const checking = status.state === 'checking';
  const available = canShowNotes(status);
  const knownUpdate = status.update_available && Boolean(status.latest_version);
  const actions = resolveUpdateActions(status);
  const progress = updateProgressPercent(status);
  const primary = resolveUpdatePrimaryAction(actions.primary);
  const primaryIcon = { download: Download, restart: RefreshCw, external: ArrowUpRight }[actions.primary];

  return (
    <>
      <FormRow
        label="当前版本"
        badge={status.local_build && <span className="rounded border border-line px-1 text-[10.5px] text-fg-muted">本地构建</span>}
      >
        <span className="selectable font-mono text-[13px] text-fg">{status.current_version || '—'}</span>
        {status.commit && <span className="font-mono text-xs text-fg-muted">{status.commit.slice(0, 7)}</span>}
      </FormRow>
      <FormRow
        label={<span role="status" className={status.state === 'error' ? 'text-danger' : ''}>{resolveUpdateStateLabel(status)}</span>}
        description={status.checked_at ? `上次检查：${formatTimestamp(status.checked_at)}` : '只检查版本信息，不会自动下载'}
      >
        <Button type="button" variant="secondary" size="sm" icon={RefreshCw} loading={checking} disabled={actions.checkDisabled} onClick={check}>
          {checking ? '检查中' : '检查更新'}
        </Button>
      </FormRow>
      {available && (
        <FormBlock>
          <div className="text-[13px] font-medium text-fg">{status.release_name || `Sinuo ${status.latest_version}`}</div>
          {status.published_at && <div className="mt-1 text-xs text-fg-muted">发布于 {formatTimestamp(status.published_at)}</div>}
          <div className="selectable mt-3 max-h-60 overflow-y-auto whitespace-pre-wrap break-words text-xs leading-relaxed text-fg-body">
            {cleanReleaseNotes(status.notes, status.latest_version) || '此版本未提供更新说明'}
          </div>
        </FormBlock>
      )}
      {(knownUpdate || status.release_url) && (
        <FormBlock>
          {status.state === 'downloading' && (
            <div className="mb-3 space-y-1.5">
              <div className="flex items-center justify-between text-xs tabular-nums text-fg-muted">
                <span>{formatUpdateBytes(status.progress_written)} / {status.progress_total > 0 ? formatUpdateBytes(status.progress_total) : '总大小未知'}</span>
                {progress !== null && <span>{progress}%</span>}
              </div>
              <div role="progressbar" aria-label="更新下载进度" aria-valuemin={0} aria-valuemax={100} aria-valuenow={progress ?? undefined} className="h-1.5 overflow-hidden rounded-full bg-accent-soft">
                <div className={`h-full rounded-full bg-accent-fg ${progress === null ? 'animate-pulse' : ''}`} style={{ width: progress === null ? '25%' : `${progress}%` }} />
              </div>
            </div>
          )}
          {knownUpdate && !status.install_supported && status.install_blocked_reason && <p className="mb-3 text-xs leading-relaxed text-fg-muted">{status.install_blocked_reason}</p>}
          <div className="flex flex-wrap items-center gap-2">
            {primary && <Button type="button" size="sm" icon={primaryIcon} onClick={update[primary.handler]}>{actions.primary === 'restart' ? '重启以完成更新' : primary.label}</Button>}
            {actions.primary !== 'external' && status.release_url && <Button type="button" variant="secondary" size="sm" icon={ArrowUpRight} onClick={openRelease}>前往下载</Button>}
            {knownUpdate && status.skipped && status.state !== 'available' && <span className="text-xs text-fg-muted">已跳过 {status.latest_version}</span>}
            {knownUpdate && <Button type="button" variant="secondary" size="sm" disabled={actions.skipDisabled} onClick={() => skip(status.skipped ? '' : status.latest_version)}>
              {status.skipped ? '取消跳过' : '跳过此版本'}
            </Button>}
          </div>
        </FormBlock>
      )}
      {notice && notice !== status.error && <FormBlock><p role="alert" className="selectable text-xs text-danger">{notice}</p></FormBlock>}
    </>
  );
};

export default AppUpdatePanel;
