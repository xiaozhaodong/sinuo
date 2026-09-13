import { useEffect, useState, useSyncExternalStore } from 'react';
import {
  getCodexSubscriptionProxyConfig,
  getCodexSubscriptionProxyStatus,
  isWailsEnvironment,
  saveCodexSubscriptionProxyConfig
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
      import('@wailsjs/runtime/runtime').then(({ EventsOn }) => {
        if (cancelled) return;
        unsubscribe = EventsOn('codex-subscription-proxy:status', () => {
          controller.refresh({ silent: true });
        });
      }).catch((error) => {
        if (!cancelled) controller.reportEventError(error);
      });
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
