// ============================================
// mergeRequestRows - 请求行引用稳定化
// 2026-08-28
// 后端每次返回的都是全新对象，直接 setRequests 会让整页所有行的
// React.memo 失效（引用恒不等），一次 request:update 就重渲染 20 行 × 12 列。
// 这里按 requestId 比对渲染相关字段，未变化的行复用旧对象引用，
// 使「新到一条请求」只重渲染真正变化的那 1~2 行。
// ============================================

// 表格实际渲染用到的字段。
// ⚠️ 新增可见列时必须同步这里，否则该列的数据变化不会触发行重渲染。
const TABLE_FIELDS = [
  'requestId',
  'timestamp',
  'status',
  'model',
  'requestFamily',
  'upstreamName',
  'duration',
  'firstTokenMs',
  'completionMs',
  'isStreaming',
  'inputTokens',
  'outputTokens',
  'cacheCreationTokens',
  'cacheReadTokens',
  'cost',
  // 表格用它们决定 token / 成本列显示数字还是占位符
  'usageKnown',
  'costKnown'
];

// 表格不渲染、但详情面板会读的字段。
// 行对象是「选中瞬间的先行渲染」数据源（RequestDetailPane.jsx 的 initialRequest），
// 详情接口返回后会被权威数据覆盖 —— 但若这里不比对，行引用会一直停在旧快照，
// 面板首帧就会闪一个过期值（连续重试时 retryCount 尤其明显），
// 且详情接口失败降级时（lifecycleDetailController 的 found:false 分支）
// 会持久显示错误数据。
// 后端同时下发 camelCase 与 snake_case 两套命名，面板也两套都读，故都要比。
const DETAIL_ONLY_FIELDS = [
  'cacheCreation5mTokens',
  'cacheCreation1hTokens',
  'effectiveEndpoint',
  'requestedEndpoint',
  'routeMode',
  'upstreamType',
  'fallbackReason',
  'failure_reason',
  'cancel_reason',
  'httpStatusCode',
  'http_status_code',
  'retryCount',
  'retry_count',
  'method',
  'path',
  'requestKind',
  'usageKnown',
  'costKnown',
  // v6.3 模型一致性：列表 ModelTag 警告与详情「模型与思考」卡片都读这 5 项
  'requestModel',
  'upstreamModel',
  'responseModel',
  'requestReasoning',
  'responseReasoning'
];

// 参与比对的字段 = 行对象在 UI 任何位置被读到的字段，而不只是被渲染的字段。
// 两张表与实际消费方的一致性由 mergeRequestRows.test.js 的静态扫描用例锁定。
export const COMPARED_FIELDS = Object.freeze([...TABLE_FIELDS, ...DETAIL_ONLY_FIELDS]);

// hasSameData 只做浅比较；NaN 不参与（后端不会下发 NaN 数值）。
const hasSameData = (left, right) => COMPARED_FIELDS.every((field) => left[field] === right[field]);

/**
 * 合并新旧两页请求行，未变化的行沿用旧引用。
 *
 * @param {Array<Object>} prevRows 上一次渲染用的行（可为空）
 * @param {Array<Object>} nextRows 本次接口返回的行
 * @returns {Array<Object>} 与 nextRows 顺序、长度一致的新数组
 */
export const mergeRequestRows = (prevRows, nextRows) => {
  if (!Array.isArray(nextRows)) return [];
  if (!Array.isArray(prevRows) || prevRows.length === 0) return nextRows;

  const prevById = new Map();
  for (const row of prevRows) {
    // 同 ID 只认第一条，与列表渲染的 key 去重行为保持一致。
    if (row && row.requestId && !prevById.has(row.requestId)) {
      prevById.set(row.requestId, row);
    }
  }

  return nextRows.map((row) => {
    if (!row || !row.requestId) return row;
    const prev = prevById.get(row.requestId);
    return prev && hasSameData(prev, row) ? prev : row;
  });
};

export default mergeRequestRows;
