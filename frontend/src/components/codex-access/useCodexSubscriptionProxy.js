import { useEffect, useState, useSyncExternalStore } from 'react';
import {
  getCodexSubscriptionProxyConfig,
  getCodexSubscriptionProxyStatus,
  isWailsEnvironment,
  saveCodexSubscriptionProxyConfig,
  subscribeToEvent
} from '@utils/wailsApi.js';
import { createSubscriptionProxyController } from './subscriptionProxyOperations.js';

const useCodexSubscriptionProxy = () => {
  const [controller] = useState(() => createSubscriptionProxyController({
    getConfig: getCodexSubscriptionProxyConfig,
    getStatus: getCodexSubscriptionProxyStatus,
    saveConfig: saveCodexSubscriptionProxyConfig
  }));
  const state = useSyncExternalStore(controller.subscribe, controller.getSnapshot);

  useEffect(() => {
    controller.refresh();
    let cancelled = false;
    let unsubscribe;
    if (isWailsEnvironment()) {
      if (!cancelled) {
        unsubscribe = subscribeToEvent('codex-subscription-proxy:status', () => {
          controller.refresh({ silent: true });
        });
      }
    }
    return () => {
      cancelled = true;
      controller.invalidateReads();
      if (typeof unsubscribe === 'function') unsubscribe();
    };
  }, [controller]);

  return {
    ...state,
    refresh: controller.refresh,
    switchMode: controller.switchMode,
    clearNotice: controller.clearNotice
  };
};

export default useCodexSubscriptionProxy;
