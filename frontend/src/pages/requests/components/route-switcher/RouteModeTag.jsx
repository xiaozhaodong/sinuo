// 当前目标的路由方式标签（优选 / 固定）
// 2026-10-07 桌面化：18px 小标签，与浮层 26px 行配套
import { LockKeyhole, Route } from 'lucide-react';

const TAG = 'inline-flex h-[18px] items-center gap-0.5 rounded border px-1 text-[11px]';

const RouteModeTag = ({ mode }) => {
  if (mode === 'manual_fixed') {
    return <span className={`${TAG} border-danger-line bg-danger-soft text-danger`}><LockKeyhole size={10} />固定</span>;
  }
  if (mode === 'manual_preferred') {
    return <span className={`${TAG} border-accent-line bg-accent-soft text-accent`}><Route size={10} />优选</span>;
  }
  return null;
};

export default RouteModeTag;
