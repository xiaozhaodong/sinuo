// 托盘导航只接受已知页面，不把后端事件解释为 URL 或脚本。
export const resolveTrayNavigation = (target) => {
  switch (target) {
    case 'requests': case 'logs': return { tab: target };
    case 'endpoints': return { tab: 'endpoints', claudeTab: 'endpoints' };
    case 'account-pool': return { tab: 'account-pool', codexTab: 'accounts' };
    case 'codex-access': return { tab: 'account-pool', codexTab: 'client-access' };
    case 'claude-model-routing': return { tab: 'endpoints', claudeTab: 'endpoints', modelRouting: 'claude' };
    case 'codex-model-routing': return { tab: 'account-pool', codexTab: 'accounts', modelRouting: 'codex' };
    default: return null;
  }
};

// 先完成事件订阅再通知 Go，保留 WebView2 首次启动期间排队的导航。
export const connectTrayNavigation = (loadRuntime, navigate, onError) => {
  let cancelled = false;
  let unsubscribe;
  loadRuntime().then(({ Events }) => {
    if (cancelled) return;
    unsubscribe = Events.On('desktop:navigate', (event) => {
      const route = resolveTrayNavigation(event.data);
      if (route) navigate(route);
    });
    return Events.Emit('desktop:navigation-ready');
  }).catch((error) => { if (!cancelled) onError(error); });
  return () => { cancelled = true; unsubscribe?.(); };
};
