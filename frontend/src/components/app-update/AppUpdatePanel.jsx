import { ArrowUpRight, RefreshCw } from 'lucide-react';
import { FormBlock, FormRow } from '@components/desktop/FormSection.jsx';
import { Button } from '@components/ui';
import { useTimezone } from '@contexts/TimezoneContext.jsx';
import { canShowNotes, cleanReleaseNotes, resolveUpdateStateLabel } from './appUpdateView.js';

const AppUpdatePanel = ({ update }) => {
  const { status, notice, check, skip, openRelease } = update;
  const { formatTimestamp } = useTimezone();
  const checking = status.state === 'checking';
  const available = canShowNotes(status);
  const knownUpdate = status.update_available && Boolean(status.latest_version);

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
        <Button type="button" variant="secondary" size="sm" icon={RefreshCw} loading={checking} disabled={checking} onClick={check}>
          {checking ? '检查中' : '检查更新'}
        </Button>
      </FormRow>
      {available && (
        <FormBlock>
          <div className="text-[13px] font-medium text-fg">{status.release_name || `Sinuo ${status.latest_version}`}</div>
          {status.published_at && <div className="mt-1 text-xs text-fg-muted">发布于 {formatTimestamp(status.published_at)}</div>}
          <div className="selectable mt-3 max-h-60 overflow-y-auto whitespace-pre-wrap break-words text-xs leading-relaxed text-fg-body">
            {cleanReleaseNotes(status.notes) || '此版本未提供更新说明'}
          </div>
        </FormBlock>
      )}
      {(knownUpdate || status.release_url) && (
        <FormBlock>
          <div className="flex flex-wrap items-center gap-2">
            <Button type="button" size="sm" icon={ArrowUpRight} disabled={!status.release_url} onClick={openRelease}>前往下载</Button>
            {knownUpdate && status.skipped && status.state !== 'available' && <span className="text-xs text-fg-muted">已跳过 {status.latest_version}</span>}
            {knownUpdate && <Button type="button" variant="secondary" size="sm" disabled={checking} onClick={() => skip(status.skipped ? '' : status.latest_version)}>
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
