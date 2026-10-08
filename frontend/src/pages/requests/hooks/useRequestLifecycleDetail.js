// ============================================
// useRequestLifecycleDetail - 详情权威数据源 Hook（方案 F3）
// 2026-08-13
// React 绑定层：核心逻辑在 lifecycleDetailController（纯函数可测）。
// 详情面板选中请求即拉取；request:update 事件命中当前 requestId 时防抖重拉；
// 关闭 / 切换请求时旧响应不回写。
// loading 由「当前 key 是否已有数据」派生，避免在 effect 内同步 setState。
// ============================================

import { useEffect, useState } from 'react';
import { isWailsEnvironment, subscribeToEvent } from '@utils/wailsApi.js';
import { fetchRequestLifecycleDetail } from '@utils/api.js';
import { createLifecycleDetailController } from '../utils/lifecycleDetailController.js';

export const useRequestLifecycleDetail = (requestId, isOpen) => {
  const key = isOpen && requestId ? requestId : null;
  const [state, setState] = useState({ key: null, data: null });

  useEffect(() => {
    if (!key) {
      return undefined;
    }

    let mounted = true;
    const controller = createLifecycleDetailController({
      fetchDetail: (id) => fetchRequestLifecycleDetail(id),
      subscribe: isWailsEnvironment()
        ? (callback) => subscribeToEvent('request:update', callback)
        : null,
      onChange: (result) => {
        if (!mounted) return;
        setState({ key, data: result });
      }
    });

    controller.open(requestId);

    return () => {
      mounted = false;
      controller.close();
    };
  }, [key, requestId]);

  const lifecycle = state.key === key ? state.data : null;
  const lifecycleLoading = Boolean(key) && state.key !== key;

  return { lifecycle, lifecycleLoading };
};

export default useRequestLifecycleDetail;
