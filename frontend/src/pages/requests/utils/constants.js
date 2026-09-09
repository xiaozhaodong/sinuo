// ============================================
// 请求追踪页面 - 常量定义
// 2025-11-28 17:02:47
// ============================================

// 状态选项配置 (v3.5.0 状态机)
export const STATUS_OPTIONS = {
  all: '全部状态',
  pending: '等待中',
  forwarding: '转发中',
  processing: '处理中',
  retry: '重试中',
  suspended: '已挂起',
  completed: '已完成',
  failed: '已失败',
  cancelled: '已取消'
};

// 「进行中」的状态集合，与上面的 STATUS_OPTIONS 状态机对齐。
// suspended（已挂起）不算进行中：它在等人工干预 —— 轨道对它不给流光，
// 耗时列对它不跑秒，两处都靠这一份集合判定。
export const IN_FLIGHT_STATUSES = new Set(['pending', 'forwarding', 'processing', 'retry']);

// 「还没落定」的状态。比 IN_FLIGHT 多一个 suspended —— 它不该有流光和跑秒
// （在等人工干预，不是在工作），但请求确实还没结束。
// 用途：token 与成本只在终态被写入，这些状态下那几列的 0 是「还不知道」
// 而不是「用了 0 个」，必须渲染成占位符。
export const UNSETTLED_STATUSES = new Set([...IN_FLIGHT_STATUSES, 'suspended']);

// 转换为 Select 组件需要的格式
export const STATUS_SELECT_OPTIONS = Object.entries(STATUS_OPTIONS).map(([value, label]) => ({
  value,
  label
}));

// 分页配置
export const PAGINATION_CONFIG = {
  DEFAULT_PAGE_SIZE: 10,
  PAGE_SIZE_OPTIONS: [10, 20, 50, 100]
};

// 默认筛选器状态（v4.0: 移除group筛选）
export const DEFAULT_FILTERS = {
  status: 'all',
  model: '',
  requestFamily: 'all',
  sourceView: 'all',
  upstreamName: 'all',
  startDate: '',
  endDate: ''
};

// 表格列定义。这个数组的顺序就是表格的渲染顺序（表头与数据格同源，
// 见 RequestsTable 的 visibleColumnConfigs）。
export const TABLE_COLUMNS = [
  // 状态列排首位：轨道是这张表的扫描锚点 —— 先看「这条现在怎么样」，
  // 再往右读「是哪条」。夹在长请求 ID 和时间戳后面时，轨道的动态感被隔断，
  // 表格读起来像历史日志而不是监控列表。
  { id: 'status', label: '状态', alwaysVisible: false, width: 'auto' },
  { id: 'requestId', label: '请求ID', alwaysVisible: true, width: 'auto' },
  { id: 'timestamp', label: '时间', alwaysVisible: true, width: 'auto' },
  { id: 'model', label: '模型', alwaysVisible: false, width: 'auto' },
  { id: 'requestFamily', label: '类型', alwaysVisible: false, width: 'auto' },
  { id: 'upstreamName', label: '上游', alwaysVisible: false, width: 'auto' },
  { id: 'duration', label: '首响 / 生成', alwaysVisible: false, width: 'auto' },
  { id: 'inputTokens', label: '输入', alwaysVisible: false, width: 'auto', align: 'right' },
  { id: 'outputTokens', label: '输出', alwaysVisible: false, width: 'auto', align: 'right' },
  { id: 'cacheCreationTokens', label: '缓存创建', alwaysVisible: false, width: 'auto', align: 'right' },
  { id: 'cacheReadTokens', label: '缓存读取', alwaysVisible: false, width: 'auto', align: 'right' },
  { id: 'cost', label: '成本', alwaysVisible: false, width: 'auto', align: 'right' }
];

// 默认全部可见。必须从 TABLE_COLUMNS 派生而不是手抄一份 id 列表 ——
// 手抄的版本会漏列、会写错 id，也会在 TABLE_COLUMNS 调整顺序时悄悄失配。
export const DEFAULT_VISIBLE_COLUMNS = TABLE_COLUMNS.map(col => col.id);

// 时间范围快捷选项
export const TIME_RANGE_OPTIONS = [
  { value: 'today', label: '今天' },
  { value: '7days', label: '7天' },
  { value: '30days', label: '30天' }
];
