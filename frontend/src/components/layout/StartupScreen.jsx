// ============================================
// 布局组件 - 启动画面
// 后端常规初始化与活动时区加载期间共用。常规启动数百毫秒内就绪，
// 全程只露与主题一致的底色；超过 800ms 仍未就绪才淡入品牌标识（见 index.css 的 startup-reveal）
// ============================================

import BrandMark from './BrandMark.jsx';

const StartupScreen = () => (
  <div className="h-screen bg-canvas flex items-center justify-center">
    <div className="startup-reveal flex flex-col items-center gap-3">
      <div className="w-12 h-12 bg-inverted rounded-xl flex items-center justify-center text-fg-inverted shadow-lg">
        <BrandMark className="w-[27px] h-[27px]" />
      </div>
      <p className="text-sm text-fg-muted">正在启动…</p>
    </div>
  </div>
);

export default StartupScreen;
