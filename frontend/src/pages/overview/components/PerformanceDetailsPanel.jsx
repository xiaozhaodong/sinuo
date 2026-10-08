// ============================================
// 详细性能监控折叠区（概览页第三层信息）
// 2026-09-09 概览改版：响应时间与连接活动默认折叠，首次展开才加载
// ============================================

import { useState } from 'react';
import { ChevronRight } from 'lucide-react';
import ResponseTimeChart from './ResponseTimeChart.jsx';
import ConnectionActivityChart from './ConnectionActivityChart.jsx';

/**
 * @param {number} refreshKey - 顶部刷新按钮的信号，展开状态下会同步重取两张图
 */
const PerformanceDetailsPanel = ({ refreshKey = 0 }) => {
  const [isOpen, setIsOpen] = useState(false);

  return (
    <section className="overflow-hidden rounded-lg border border-line bg-surface">
      {/* 披露三角（macOS 折叠行惯例）：▸ 收起 / ▾ 展开 */}
      <button
        type="button"
        onClick={() => setIsOpen((current) => !current)}
        aria-expanded={isOpen}
        className="flex h-[34px] w-full items-center gap-1.5 px-3 text-left hover:bg-surface-sub"
      >
        <ChevronRight size={14} className={`shrink-0 text-fg-muted transition-transform duration-150 ${isOpen ? 'rotate-90' : ''}`} />
        <span className="text-[13px] font-semibold text-fg">响应时间与连接活动</span>
        <span className="text-xs text-fg-muted">按需加载</span>
      </button>

      {/* 保持挂载：折叠时 active=false 不发起请求，展开后数据不因再次折叠而丢失 */}
      <div className={`grid grid-cols-2 gap-3 border-t border-line-soft p-3 ${isOpen ? '' : 'hidden'}`}>
        <ResponseTimeChart active={isOpen} refreshKey={refreshKey} />
        <ConnectionActivityChart active={isOpen} refreshKey={refreshKey} />
      </div>
    </section>
  );
};

export default PerformanceDetailsPanel;
