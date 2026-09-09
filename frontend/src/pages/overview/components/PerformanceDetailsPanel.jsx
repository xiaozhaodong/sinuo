// ============================================
// 详细性能监控折叠区（概览页第三层信息）
// 2026-09-09 概览改版：响应时间与连接活动默认折叠，首次展开才加载
// ============================================

import { useState } from 'react';
import { ChevronDown, Gauge } from 'lucide-react';
import ResponseTimeChart from './ResponseTimeChart.jsx';
import ConnectionActivityChart from './ConnectionActivityChart.jsx';

/**
 * @param {number} refreshKey - 顶部刷新按钮的信号，展开状态下会同步重取两张图
 */
const PerformanceDetailsPanel = ({ refreshKey = 0 }) => {
  const [isOpen, setIsOpen] = useState(false);

  return (
    <section className="bg-surface rounded-2xl border border-line/60 shadow-sm overflow-hidden">
      <button
        type="button"
        onClick={() => setIsOpen((current) => !current)}
        aria-expanded={isOpen}
        className="flex w-full items-center justify-between gap-4 px-6 py-4 text-left transition-colors hover:bg-surface-sub"
      >
        <span className="flex flex-wrap items-center gap-x-3 gap-y-1">
          <span className="flex items-center gap-2 text-sm font-semibold text-fg">
            <Gauge size={16} className="text-fg-muted" />
            详细性能监控
          </span>
          <span className="text-xs text-fg-muted">响应时间 · 连接活动 · 按需展开</span>
        </span>
        <ChevronDown
          size={18}
          className={`shrink-0 text-fg-muted transition-transform duration-200 ${isOpen ? 'rotate-180' : ''}`}
        />
      </button>

      {/* 保持挂载：折叠时 active=false 不发起请求，展开后数据不因再次折叠而丢失 */}
      <div className={`grid grid-cols-1 gap-4 px-4 pb-4 lg:grid-cols-2 ${isOpen ? '' : 'hidden'}`}>
        <ResponseTimeChart active={isOpen} refreshKey={refreshKey} />
        <ConnectionActivityChart active={isOpen} refreshKey={refreshKey} />
      </div>
    </section>
  );
};

export default PerformanceDetailsPanel;
