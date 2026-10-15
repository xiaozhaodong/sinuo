import { useCallback, useEffect, useRef } from 'react';
import { useTimezone } from '@/contexts/TimezoneContext.jsx';
import { resolveDayRollover } from '../utils/dayRollover.js';

const DAY_ROLLOVER_CHECK_INTERVAL_MS = 60_000;

export const useDayRollover = ({ activeRange, filters, onRollover }) => {
  const { timezone } = useTimezone();
  const latestRef = useRef({ activeRange, filters, onRollover, timezone });

  // 监听器保持稳定，检查时读取最近一次提交的筛选与业务时区。
  useEffect(() => {
    latestRef.current = { activeRange, filters, onRollover, timezone };
  }, [activeRange, filters, onRollover, timezone]);

  const checkDayRollover = useCallback(() => {
    const nextRange = resolveDayRollover(latestRef.current);
    if (!nextRange) return false;
    latestRef.current.onRollover(nextRange);
    return true;
  }, []);

  useEffect(() => {
    const timer = window.setInterval(checkDayRollover, DAY_ROLLOVER_CHECK_INTERVAL_MS);
    const handleVisibilityChange = () => {
      if (!document.hidden) checkDayRollover();
    };
    document.addEventListener('visibilitychange', handleVisibilityChange);
    window.addEventListener('focus', checkDayRollover);
    return () => {
      window.clearInterval(timer);
      document.removeEventListener('visibilitychange', handleVisibilityChange);
      window.removeEventListener('focus', checkDayRollover);
    };
  }, [checkDayRollover]);

  return checkDayRollover;
};
