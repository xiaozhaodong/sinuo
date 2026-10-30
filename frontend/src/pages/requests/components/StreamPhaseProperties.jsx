import { Copy } from 'lucide-react';
import { Property } from '@components/desktop/Inspector.jsx';
import { stopReasonView } from '../utils/streamPhases.js';
import { copyTextToClipboard } from './clipboard.js';

const ICON_BUTTON = 'inline-flex -my-1 h-6 w-6 shrink-0 items-center justify-center rounded text-fg-muted hover:bg-surface-mut hover:text-fg';

export default function StreamPhaseProperties({ request }) {
  const stop = stopReasonView(request.stopReason);
  const id = request.upstreamRequestId;
  return <>
    {stop.label && <Property label="结束原因"><span className={stop.className} title={stop.raw}>{stop.label}</span></Property>}
    {id && <Property label="上游请求 ID" truncate={false}>
      <span className="inline-flex min-w-0 items-center gap-1.5">
        <span className="font-mono text-xs" title={id}>{id.length > 14 ? `${id.slice(0, 14)}…` : id}</span>
        <button type="button" title="复制上游请求 ID" aria-label="复制上游请求 ID" className={ICON_BUTTON} onClick={() => copyTextToClipboard(id, '上游请求 ID')}>
          <Copy size={13} />
        </button>
      </span>
    </Property>}
  </>;
}
