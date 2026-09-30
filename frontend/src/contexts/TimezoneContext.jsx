import { createContext, useCallback, useContext, useEffect, useMemo, useState } from 'react';
import { getConfig, subscribeToEvent } from '@utils/wailsApi.js';
import StartupScreen from '@components/layout/StartupScreen.jsx';
import {
  formatTimeOnly,
  formatMonthDayTime,
  formatTimestamp,
  getRecentDaysRange,
  getTodayTimeRange,
  validateTimezone
} from '@utils/timezone.js';

const TimezoneContext = createContext(null);

export const TimezoneProvider = ({ children }) => {
  const [state, setState] = useState({ timezone: '', loading: true, error: '' });

  const reload = useCallback(async () => {
    try {
      const config = await getConfig();
      const timezone = validateTimezone(config?.timezone ?? config?.Timezone);
      setState({ timezone, loading: false, error: '' });
    } catch (error) {
      setState({ timezone: '', loading: false, error: error?.message || '活动时区加载失败' });
    }
  }, []);

  useEffect(() => {
    // reload 为异步初始加载：setState 发生在 await 之后，非同步级联渲染，规则误报。
    // eslint-disable-next-line react-hooks/set-state-in-effect
    reload();
    const unsubscribe = subscribeToEvent('config:reloaded', reload);
    return () => unsubscribe?.();
  }, [reload]);

  const value = useMemo(() => {
    if (!state.timezone) return null;
    return {
      timezone: state.timezone,
      formatTimestamp: (timestamp) => formatTimestamp(timestamp, state.timezone),
      formatTimeOnly: (timestamp) => formatTimeOnly(timestamp, state.timezone),
      formatMonthDayTime: (timestamp) => formatMonthDayTime(timestamp, state.timezone),
      getTodayTimeRange: (now) => getTodayTimeRange(state.timezone, now),
      getRecentDaysRange: (days, now) => getRecentDaysRange(days, state.timezone, now),
      reload
    };
  }, [reload, state.timezone]);

  // 仅首次加载：紧接在启动画面之后，沿用同一画面，避免启动时一闪一行文字
  if (state.loading) {
    return <StartupScreen />;
  }
  if (state.error || !value) {
    return (
      <div className="flex min-h-screen items-center justify-center bg-surface-sub px-6">
        <div role="alert" className="max-w-lg rounded-xl border border-danger-line bg-surface px-5 py-4 text-sm text-danger shadow-sm">
          时区配置不可用：{state.error || '未知错误'}
        </div>
      </div>
    );
  }
  return <TimezoneContext.Provider value={value}>{children}</TimezoneContext.Provider>;
};

// Context 配套 Hook 与 Provider 同文件是项目惯例；仅影响 HMR 精度，不影响构建。
// eslint-disable-next-line react-refresh/only-export-components
export const useTimezone = () => {
  const context = useContext(TimezoneContext);
  if (!context) throw new Error('useTimezone 必须在 TimezoneProvider 内使用');
  return context;
};
