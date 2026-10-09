import { useTimezone } from '@contexts/TimezoneContext.jsx';
import { splitRoutingMembers } from './routingView.js';

const MEMBER_CLASS = 'rounded border px-1.5 text-[11px] leading-[18px]';

export const MemberChipsCell = ({ members, emptyText }) => {
  const { visible, hidden, overflowTone } = splitRoutingMembers(members);
  return (
    <td className="max-w-[200px]">
      <div className="flex flex-nowrap items-center gap-1 overflow-hidden">
        {visible.map((member) => (
          <span key={member.key} title={member.title} className={`${MEMBER_CLASS} min-w-0 max-w-[120px] truncate ${member.tone} ${member.className || ''}`}>
            {member.label}
          </span>
        ))}
        {hidden.length > 0 && (
          <span
            title={hidden.map((member) => member.title).join('\n')}
            className={`${MEMBER_CLASS} shrink-0 ${overflowTone}`}
          >
            +{hidden.length}
          </span>
        )}
        {members.length === 0 && <span className="text-danger">{emptyText}</span>}
      </div>
    </td>
  );
};

const OUTCOME_DOT_CLASSES = {
  success: 'bg-success-solid',
  neutral: 'bg-fg-subtle',
  danger: 'bg-danger-solid'
};

export const RecentHitCell = ({ name, at, outcomeLabel, outcomeTone }) => {
  const { formatListTimestamp, formatTimestamp } = useTimezone();
  const displayName = name || '';
  const displayTime = at ? formatListTimestamp(at) : '';
  const text = [displayName, displayTime].filter(Boolean).join(' · ') || '—';
  const title = [displayName, at ? formatTimestamp(at) : '', outcomeLabel].filter(Boolean).join(' · ');
  const dotClass = OUTCOME_DOT_CLASSES[outcomeTone];

  return (
    <td className="max-w-[180px] text-[11.5px] text-fg-muted @max-[900px]:hidden" title={title || undefined}>
      <div className="flex items-center gap-1.5">
        {dotClass && (displayName || displayTime) && <span className={`h-1.5 w-1.5 shrink-0 rounded-full ${dotClass}`} aria-hidden="true" />}
        <span className="truncate">{text}</span>
      </div>
    </td>
  );
};
