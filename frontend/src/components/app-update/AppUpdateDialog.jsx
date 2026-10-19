import { useRef } from 'react';
import { ArrowUpRight, Download, RefreshCw } from 'lucide-react';
import Sheet from '@components/desktop/Sheet.jsx';
import { Button } from '@components/ui';
import { useTimezone } from '@contexts/TimezoneContext.jsx';
import { cleanReleaseNotes, formatUpdateBytes, resolveUpdateDialogSections, updateProgressPercent } from './appUpdateView.js';

const AppUpdateDialog = ({ update, onClose, onOpenSettings }) => {
  const { status, notice, operationPending } = update;
  const { formatTimestamp } = useTimezone();
  const initialFocusRef = useRef(null);
  const sections = resolveUpdateDialogSections(status, notice);
  const progress = updateProgressPercent(status);
  const primaryIcon = { check: RefreshCw, download: Download, restart: RefreshCw, external: ArrowUpRight }[sections.primary];

  return (
    <Sheet title="软件更新" subtitle={`当前 v${status.current_version}`} width={480} centered onClose={onClose} initialFocusRef={initialFocusRef}
      footerStart={<button type="button" disabled={operationPending} onClick={onOpenSettings} className="text-xs text-fg-muted hover:text-fg disabled:opacity-50">在设置中查看 ›</button>}
      footer={<>
        {sections.showSkip && <Button type="button" variant="secondary" size="sm" disabled={operationPending || sections.skipDisabled}
          onClick={() => update.skip(sections.skipVersion)}>{sections.skipLabel}</Button>}
        {sections.showExternal && <Button type="button" variant="ghost" size="sm" disabled={operationPending} onClick={update.openRelease}>前往下载</Button>}
        {sections.primaryAction && <Button type="button" size="sm" icon={primaryIcon} loading={sections.primaryLoading}
          disabled={operationPending || sections.primaryDisabled} onClick={update[sections.primaryAction.handler]}>{sections.primaryAction.label}</Button>}
      </>}
    >
      <div className="space-y-3 pt-4">
        <div ref={initialFocusRef} tabIndex={-1} className="rounded-lg border border-line bg-surface p-3.5 focus:outline-none">
          <div className="flex flex-wrap items-center justify-between gap-x-2 gap-y-1">
            <h3 className="selectable text-[13px] font-semibold text-fg">{sections.title}</h3>
            {sections.showPublishedAt && <span className="text-[10.5px] text-fg-muted">发布于 {formatTimestamp(status.published_at).split(' ')[0]}</span>}
            {sections.showLocalBuild && <span className="rounded border border-line px-1 text-[10.5px] text-fg-muted">本地构建</span>}
          </div>
          <p role="status" className={`selectable mt-2 text-xs leading-relaxed ${sections.stateError ? 'text-danger' : 'text-fg-body'}`}>{sections.stateLabel}</p>
          {sections.showCheckedAt && <p className="mt-1 text-[10.5px] text-fg-muted">上次检查：{formatTimestamp(status.checked_at)}</p>}
        </div>

        {sections.showNotes && <div className="selectable max-h-[280px] overflow-y-auto whitespace-pre-wrap break-words rounded-lg border border-line bg-surface p-3.5 text-xs leading-relaxed text-fg-body">
          {cleanReleaseNotes(status.notes, status.latest_version) || '此版本未提供更新说明'}
        </div>}

        {sections.progress && <div className="space-y-1.5 rounded-lg border border-line bg-surface p-3.5">
          {sections.progress === 'download' ? <>
            <div className="flex items-center justify-between text-xs tabular-nums text-fg-muted">
              <span>{formatUpdateBytes(status.progress_written)} / {status.progress_total > 0 ? formatUpdateBytes(status.progress_total) : '总大小未知'}</span>
              {progress !== null && <span>{progress}%</span>}
            </div>
            <div role="progressbar" aria-label="更新下载进度" aria-valuemin={0} aria-valuemax={100} aria-valuenow={progress ?? undefined} className="h-1.5 overflow-hidden rounded-full bg-accent-soft">
              <div className={`h-full rounded-full bg-accent-fg ${progress === null ? 'animate-pulse' : ''}`} style={{ width: progress === null ? '25%' : `${progress}%` }} />
            </div>
          </> : <p className="text-xs text-fg-muted">正在校验并安装…</p>}
        </div>}

        {sections.blockedReason && <p className="rounded-lg border border-line bg-surface p-3.5 text-xs leading-relaxed text-fg-muted">{sections.blockedReason}</p>}
        {sections.notice && <p role="alert" className="selectable rounded-lg border border-line bg-surface p-3.5 text-xs leading-relaxed text-danger">{sections.notice}</p>}
      </div>
    </Sheet>
  );
};

export default AppUpdateDialog;
