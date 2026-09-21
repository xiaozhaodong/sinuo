// useWailsLogs.js - Wails 日志流 Hook
// 提供实时日志查看功能
import { useState, useEffect, useCallback, useRef } from 'react';
import { EventsOn, EventsOff } from '@wailsjs/runtime';
import { resolveErrorMessage } from '../utils/errors.js';
import {
  GetRecentLogs,
  StartLogStream,
  StopLogStream,
  GetLogStreamStatus
} from '@wailsjs/go/main/App';

/**
 * useWailsLogs Hook
 * @param {Object} options 配置选项
 * @param {number} options.maxLogs 最大日志条数（默认500）
 * @param {boolean} options.autoStart 是否自动启动流（默认true）
 * @param {string} options.levelFilter 日志级别过滤（默认null，显示全部）
 * @param {boolean} options.isActive 页面是否可见（默认true）
 * @returns {Object} { logs, loading, error, isStreaming, start, stop, clear, refresh }
 */
export function useWailsLogs(options = {}) {
  const {
    maxLogs = 500,
    autoStart = true,
    levelFilter = null,
    isActive = true, // 新增：页面可见性
  } = options;

  const [logs, setLogs] = useState([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState(null);
  const [isStreaming, setIsStreaming] = useState(false);

  const unsubscribeRef = useRef(null);
  const isMountedRef = useRef(true);
  // 防止并发调用 startStreaming
  const isStartingRef = useRef(false);
  // 唯一实例 ID，用于防止 StrictMode 下的重复订阅
  const instanceIdRef = useRef(Date.now().toString(36) + Math.random().toString(36).slice(2));

  // 日志级别过滤
  const filterLogs = useCallback((logList) => {
    if (!levelFilter) return logList;
    return logList.filter(log => log.level === levelFilter);
  }, [levelFilter]);

  // 加载历史日志
  const loadRecentLogs = useCallback(async () => {
    try {
      setLoading(true);
      setError(null);
      const recentLogs = await GetRecentLogs(maxLogs);
      if (isMountedRef.current) {
        setLogs(filterLogs(recentLogs || []));
      }
    } catch (err) {
      console.error('❌ 加载历史日志失败:', err);
      if (isMountedRef.current) {
        setError(resolveErrorMessage(err, '加载失败'));
      }
    } finally {
      if (isMountedRef.current) {
        setLoading(false);
      }
    }
  }, [maxLogs, filterLogs]);

  // 启动日志流
  const startStreaming = useCallback(async () => {
    // 防止并发调用（解决 StrictMode 和异步竞态问题）
    if (isStartingRef.current) {
      console.log('📡 [日志流] 已有启动操作进行中，跳过');
      return;
    }
    isStartingRef.current = true;

    try {
      // 检查是否已经在流式传输
      const status = await GetLogStreamStatus();
      if (!status) {
        await StartLogStream();
      }

      // 组件已卸载，不继续订阅
      if (!isMountedRef.current) {
        isStartingRef.current = false;
        return;
      }

      // ⚠️ 关键修复：在订阅之前取消所有现有订阅
      // 放在异步操作之后，确保取消的是已经存在的订阅
      EventsOff('log:batch');
      unsubscribeRef.current = null;

      // 订阅日志事件（批量）
      const currentInstanceId = instanceIdRef.current;
      const unsubscribe = EventsOn('log:batch', (batchLogs) => {
        // 检查是否仍是当前实例且组件未卸载
        if (!isMountedRef.current || instanceIdRef.current !== currentInstanceId) {
          return;
        }

        setLogs(prevLogs => {
          // 合并新日志，限制总数
          const newLogs = [...prevLogs, ...batchLogs];
          return filterLogs(newLogs.slice(-maxLogs));
        });
      });

      unsubscribeRef.current = unsubscribe;

      if (isMountedRef.current) {
        setIsStreaming(true);
      }
    } catch (err) {
      console.error('❌ 启动日志流失败:', err);
      if (isMountedRef.current) {
        setError(resolveErrorMessage(err, '启动失败'));
      }
    } finally {
      isStartingRef.current = false;
    }
  }, [maxLogs, filterLogs]);

  // 停止日志流
  const stopStreaming = useCallback(async () => {
    try {
      // 更新实例 ID，使旧的回调失效
      instanceIdRef.current = Date.now().toString(36) + Math.random().toString(36).slice(2);

      // 用 EventsOff 显式取消所有 log:batch 订阅
      EventsOff('log:batch');
      unsubscribeRef.current = null;

      // 再停止后端流（检查是否正在运行）
      const isRunning = await GetLogStreamStatus();
      if (isRunning) {
        await StopLogStream();
      }

      if (isMountedRef.current) {
        setIsStreaming(false);
      }
    } catch (err) {
      console.error('❌ 停止日志流失败:', err);
    }
  }, []);

  // 清空日志
  const clearLogs = useCallback(() => {
    setLogs([]);
  }, []);

  // 刷新日志
  const refresh = useCallback(async () => {
    await loadRecentLogs();
  }, [loadRecentLogs]);

  // 初始化 - 只在组件挂载时执行一次
  useEffect(() => {
    isMountedRef.current = true;
    // 重置启动锁
    isStartingRef.current = false;
    // 生成新的实例 ID
    instanceIdRef.current = Date.now().toString(36) + Math.random().toString(36).slice(2);

    // 1. 加载历史日志
    loadRecentLogs();

    // 2. 自动启动流
    if (autoStart) {
      startStreaming();
    }

    // 清理函数
    return () => {
      isMountedRef.current = false;
      // 取消所有订阅
      EventsOff('log:batch');
      if (unsubscribeRef.current) {
        unsubscribeRef.current();
        unsubscribeRef.current = null;
      }
    };
    // 注意：依赖数组为空，只在挂载/卸载时执行
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  // 页面可见性控制：当页面不可见时停止监听，节省资源
  useEffect(() => {
    // 跳过初始渲染
    if (!isMountedRef.current) return;

    if (!isActive && isStreaming) {
      // 页面不可见，停止监听
      console.log('📴 日志页面不可见，停止日志流');
      stopStreaming();
    } else if (isActive && !isStreaming && autoStart && !isStartingRef.current) {
      // 页面重新可见，重新启动（确保没有正在启动的操作）
      console.log('📡 日志页面可见，重新启动日志流');
      startStreaming();
    }
  }, [isActive, isStreaming, autoStart, startStreaming, stopStreaming]);

  return {
    logs,
    loading,
    error,
    isStreaming,
    start: startStreaming,
    stop: stopStreaming,
    clear: clearLogs,
    refresh,
  };
}

export default useWailsLogs;
