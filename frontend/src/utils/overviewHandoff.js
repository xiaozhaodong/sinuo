// ============================================
// 概览 → 请求追踪 的一次性筛选交接
// 2026-09-09 概览改版：模型表点击行/来源标签后带条件跳到请求追踪页。
// 两个页面不会同时挂载，模块级变量足够；请求追踪页挂载时消费一次即清空，
// 避免用户下次从导航栏进入请求页时被上一次的筛选条件粘住。
// 时间范围必须一起交接：概览选了 7d/30d 时，请求页若还停在默认「今天」就对不上账。
// 概览给的是滚动区间（近 24 小时 / 近 7 天），与请求页的「今天 / 7天 / 30天」
// 业务日预设不等价，所以请求页消费到日期后不再点亮任何预设按钮。
// ============================================

let pendingRequestsFilters = null;

export const setPendingRequestsFilters = (filters) => {
  if (!filters) {
    pendingRequestsFilters = null;
    return;
  }
  const next = {};
  if (filters.model) next.model = filters.model;
  if (filters.requestFamily && filters.requestFamily !== 'all') next.requestFamily = filters.requestFamily;
  if (filters.sourceView === 'endpoint' || filters.sourceView === 'account' || filters.sourceView === 'subscription') next.sourceView = filters.sourceView;
  // 「含独立压缩 / 仅生成」必须随交接保留：概览数字是生成口径，默认对齐为仅生成。
  if (filters.includeCompaction === false) next.includeCompaction = false;
  if (filters.startDate) next.startDate = filters.startDate;
  if (filters.endDate) next.endDate = filters.endDate;
  pendingRequestsFilters = Object.keys(next).length > 0 ? next : null;
};

export const consumePendingRequestsFilters = () => {
  const filters = pendingRequestsFilters;
  pendingRequestsFilters = null;
  return filters;
};
