import { useEffect, useRef, useState } from 'react';

// 只比较两次都存在的策略键，初次加载、新增和删除都不算切换。
export const changedKeys = (previous, next) => [...next.keys()].filter((key) => previous.has(key) && !Object.is(previous.get(key), next.get(key)));

const nextFlashState = (state, values) => {
  const changed = changedKeys(state.values, values);
  if (changed.length === 0 && state.values.size === values.size && [...values.keys()].every((key) => state.values.has(key))) return state;
  const flashes = new Map([...state.flashes].filter(([key]) => values.has(key)));
  changed.forEach((key) => flashes.set(key, {})); // 每次变化用独立标记，旧定时器不能清掉新回执。
  return { values, flashes };
};

const syncFlashTimers = (flashes, timers, duration, expire) => {
  for (const [key, timer] of timers) {
    if (flashes.get(key) !== timer.token || timer.duration !== duration) {
      clearTimeout(timer.id);
      timers.delete(key);
    }
  }
  for (const [key, token] of flashes) {
    if (timers.has(key)) continue;
    const id = setTimeout(() => {
      if (timers.get(key)?.id !== id) return;
      timers.delete(key);
      expire(key, token);
    }, duration);
    timers.set(key, { id, token, duration });
  }
};

// 只保存策略快照；计时器在提交后启动，每项独立计时，卸载后全部清理。
const useChangeFlash = (entries, duration = 1500) => {
  const [state, setState] = useState(() => ({ values: new Map(entries), flashes: new Map() }));
  const timers = useRef(new Map());
  const next = nextFlashState(state, new Map(entries));
  if (next !== state) setState(next);

  useEffect(() => {
    syncFlashTimers(state.flashes, timers.current, duration, (key, token) => {
      setState((current) => {
        if (current.flashes.get(key) !== token) return current;
        const flashes = new Map(current.flashes);
        flashes.delete(key);
        return { ...current, flashes };
      });
    });
  }, [state.flashes, duration]);

  useEffect(() => {
    const pending = timers.current;
    return () => {
      for (const timer of pending.values()) clearTimeout(timer.id);
      pending.clear();
    };
  }, []);

  return new Set(state.flashes.keys());
};

export default useChangeFlash;
