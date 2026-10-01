import { LockKeyhole, Route } from 'lucide-react';

const RouteModeTag = ({ mode }) => {
  if (mode === 'manual_fixed') {
    return <span className="tone-rose inline-flex items-center gap-1 rounded border px-1.5 py-px text-[10px] font-medium"><LockKeyhole size={10} />固定</span>;
  }
  if (mode === 'manual_preferred') {
    return <span className="tone-indigo inline-flex items-center gap-1 rounded border px-1.5 py-px text-[10px] font-medium"><Route size={10} />优选</span>;
  }
  return null;
};

export default RouteModeTag;
