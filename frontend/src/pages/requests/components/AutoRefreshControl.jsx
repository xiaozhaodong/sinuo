// ============================================
// RequestRefreshStatus - 状态栏中的降级刷新提示
// ============================================

import { WifiOff } from 'lucide-react';

/**
 * @param {Object} props
 * @param {'connecting'|'live'|'fallback'} props.mode - 实时刷新状态
 * @param {number} props.fallbackInterval - 降级刷新间隔（秒）
 */
const RequestRefreshStatus = ({
  mode = 'connecting',
  fallbackInterval = 30
}) => {
  if (mode !== 'fallback') return null;

  return (
    <span
      className="inline-flex shrink-0 items-center gap-1.5 text-[11.5px] text-warn"
      title={`实时事件不可用或连续刷新失败，当前每 ${fallbackInterval} 秒自动校准一次`}
    >
      <WifiOff size={12} aria-hidden="true" />
      <span>已降级 · 每 {fallbackInterval} 秒刷新</span>
    </span>
  );
};

export default RequestRefreshStatus;
