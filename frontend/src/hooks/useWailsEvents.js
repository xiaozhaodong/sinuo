// ============================================
// Wails Events Hook
// 2025-12-04
// 使用 Wails Events 替代 SSE 实时数据
// ============================================

import { useState, useRef, useCallback, useEffect } from 'react';
import { isWailsEnvironment, initWails, subscribeToEvent } from '@utils/wailsApi.js';

// Wails 事件名称（与后端 app_events.go 对应）
const WAILS_EVENTS = {
  SYSTEM_STATUS: 'system:status',
  ENDPOINT_UPDATE: 'endpoint:update',
  USAGE_UPDATE: 'usage:update',
  CONFIG_RELOADED: 'config:reloaded',
  ERROR: 'error',
  NOTIFICATION: 'notification'
};

// 事件映射：前端事件名 -> Wails 事件名（模块级常量，避免每次渲染重建）
const eventMapping = {
  'status': WAILS_EVENTS.SYSTEM_STATUS,
  'endpoint': WAILS_EVENTS.ENDPOINT_UPDATE,
  'usage': WAILS_EVENTS.USAGE_UPDATE,
  'config': WAILS_EVENTS.CONFIG_RELOADED,
  'error': WAILS_EVENTS.ERROR,
  'notification': WAILS_EVENTS.NOTIFICATION
};

// 连接状态
export const WAILS_STATUS = {
  DISCONNECTED: 'disconnected',
  CONNECTING: 'connecting',
  CONNECTED: 'connected',
  ERROR: 'error',
  NOT_AVAILABLE: 'not_available'
};

/**
 * Wails Events Hook
 * 替代 useSSE，使用 Wails 事件系统
 * @param {Function} onDataUpdate - 数据更新回调函数 (data, eventType) => void
 * @param {Object} options - 配置选项
 */
const useWailsEvents = (onDataUpdate, options = {}) => {
  const {
    events = 'status,endpoint'
  } = options;

  const [connectionStatus, setConnectionStatus] = useState(WAILS_STATUS.CONNECTING);
  const onDataUpdateRef = useRef(onDataUpdate);
  const unsubscribersRef = useRef([]);
  const isInitializedRef = useRef(false);

  // 保持回调引用最新
  useEffect(() => {
    onDataUpdateRef.current = onDataUpdate;
  }, [onDataUpdate]);

  // 订阅事件
  const subscribe = useCallback(async () => {
    // 检查是否在 Wails 环境
    if (!isWailsEnvironment()) {
      console.log('📡 [Wails Events] 非 Wails 环境，跳过事件订阅');
      setConnectionStatus(WAILS_STATUS.NOT_AVAILABLE);
      return;
    }

    try {
      console.log('📡 [Wails Events] 初始化中...');
      setConnectionStatus(WAILS_STATUS.CONNECTING);

      // 等待 Wails 初始化
      const initialized = await initWails();
      if (!initialized) {
        console.warn('⚠️ [Wails Events] Wails 初始化失败');
        setConnectionStatus(WAILS_STATUS.ERROR);
        return;
      }

      // 订阅请求的事件
      const eventTypes = events.split(',').map(e => e.trim());

      eventTypes.forEach(eventType => {
        const wailsEventName = eventMapping[eventType];
        if (!wailsEventName) {
          console.warn(`⚠️ [Wails Events] 未知事件类型: ${eventType}`);
          return;
        }

        const unsubscribe = subscribeToEvent(wailsEventName, (data) => {
          console.log(`📡 [Wails Events] 收到 ${eventType} 事件:`, data);

          if (onDataUpdateRef.current) {
            // 转换数据格式以兼容 SSE 处理逻辑
            const wrappedData = {
              data: data,
              event: eventType,
              ...data
            };
            onDataUpdateRef.current(wrappedData, eventType);
          }
        });

        unsubscribersRef.current.push(unsubscribe);
      });

      console.log('✅ [Wails Events] 事件订阅成功');
      setConnectionStatus(WAILS_STATUS.CONNECTED);
      isInitializedRef.current = true;

    } catch (error) {
      console.error('❌ [Wails Events] 订阅失败:', error);
      setConnectionStatus(WAILS_STATUS.ERROR);
    }
  }, [events]);

  // 取消订阅
  const unsubscribe = useCallback(() => {
    unsubscribersRef.current.forEach(unsub => {
      if (typeof unsub === 'function') {
        unsub();
      }
    });
    unsubscribersRef.current = [];
    setConnectionStatus(WAILS_STATUS.DISCONNECTED);
    console.log('📡 [Wails Events] 已取消订阅');
  }, []);

  // 重新连接
  const reconnect = useCallback(() => {
    unsubscribe();
    subscribe();
  }, [subscribe, unsubscribe]);

  // 组件挂载时自动订阅
  useEffect(() => {
    const timer = setTimeout(() => {
      subscribe();
    }, 100); // 延迟初始化，等待 DOM 准备就绪

    return () => {
      clearTimeout(timer);
      unsubscribe();
    };
  }, []); // eslint-disable-line react-hooks/exhaustive-deps

  return {
    connectionStatus,
    reconnectAttempts: 0,
    connect: subscribe,
    disconnect: unsubscribe,
    reconnect,
    isConnected: connectionStatus === WAILS_STATUS.CONNECTED,
    isWailsAvailable: isWailsEnvironment()
  };
};

export default useWailsEvents;
