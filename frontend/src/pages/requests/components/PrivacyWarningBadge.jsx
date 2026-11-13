import { ShieldAlert } from 'lucide-react';
import { privacyWarningGroups } from '../utils/privacyWarnings.js';

const PrivacyWarningBadge = ({ codes, detailed = false }) => {
  const groups = privacyWarningGroups(codes);
  if (!groups.length) return null;
  if (!detailed) {
    const description = groups.map((group) => `${group.title}\n${group.message}`).join('\n\n');
    return (
      <span className="inline-flex shrink-0 cursor-help items-center text-warn" role="img"
        data-privacy-warning={groups.map((group) => group.stage).join(' ')} title={description} aria-label={description}>
        <ShieldAlert size={13} aria-hidden="true" />
      </span>
    );
  }
  return (
    <span className="inline-flex flex-col gap-1">
      {groups.map((group) => (
        <span key={group.stage} data-privacy-warning={group.stage}
          className="inline-flex items-start gap-1.5 text-xs leading-relaxed text-warn"
          title={group.message} aria-label={group.title}>
          <ShieldAlert size={12} className="mt-0.5 shrink-0" aria-hidden="true" />
          {group.message}
        </span>
      ))}
    </span>
  );
};

export default PrivacyWarningBadge;
