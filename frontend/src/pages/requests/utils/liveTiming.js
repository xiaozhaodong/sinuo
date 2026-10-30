// ============================================
// liveTiming - 计时事件补丁
// 2026-10-11
// 后端在首响起点、首响、生成耗时落定的那一刻，把精确值随 request:update 推过来。
// 这里把它们直接补到对应行上，跑秒在事件到达时就定格；整行数据仍走原来的合并刷新。
//
// 两条数据来源（事件补丁 / 列表查询）谁先到都可能，所以合并规则只进不退：
//   - 首响、首字时刻、生成耗时后端只写一次：已有值的一方为准，另一方只补空缺。
//     否则一次在事件之前发出、之后才返回的查询，会把已定格的跑秒冲回去重跑。
//   - 首响起点与重试起点会后移：取较晚者；新口径重试次数只取较大者。
// 事件也可能早于这一行出现在列表里（新请求要等合并刷新与查询），这时补丁打不上。
// 热池开着时紧接着的查询会带回计时；关着时数据库里没有首响起点，补丁丢了首响位就停在「···」。
// 所以补丁再按请求 ID 暂存一小会儿，查询结果到达时补上。
// ============================================

import { parseTimestampOrNull } from './lifecycle.js';

// 事件字段名 -> 行字段名（行对象已过 normalizeRequest，用驼峰）
const TIMING_EVENT_FIELDS = [
  ['first_token_start_at', 'firstTokenStartAt'],
  ['retry_started_at', 'retryStartedAt'],
  ['retry_count', 'retryCount'],
  ['first_token_ms', 'firstTokenMs'],
  ['first_token_at', 'firstTokenAt'],
  ['completion_ms', 'completionMs']
];

const isEmpty = (value) => value === undefined || value === null || value === '';

const laterTimestamp = (left, right) => {
  const leftAt = parseTimestampOrNull(left);
  const rightAt = parseTimestampOrNull(right);
  if (!leftAt) return right;
  if (!rightAt) return left;
  return rightAt.getTime() > leftAt.getTime() ? right : left;
};

// mergeTimingField base 为准，extra 只补空缺；首响起点取较晚者。
const mergeTimingField = (field, baseValue, extraValue) => {
  if (isEmpty(extraValue)) return baseValue;
  if (isEmpty(baseValue)) return extraValue;
  if (field === 'retryCount') return Math.max(baseValue, extraValue);
  return field === 'firstTokenStartAt' || field === 'retryStartedAt' ? laterTimestamp(baseValue, extraValue) : baseValue;
};

// fillTiming 返回补齐计时后的行；没有任何变化时返回原引用，保住行级 memo。
const fillTiming = (base, extra) => {
  let changed = null;
  for (const [, field] of TIMING_EVENT_FIELDS) {
    const merged = mergeTimingField(field, base[field], extra[field]);
    if (merged !== base[field]) {
      changed = changed || {};
      changed[field] = merged;
    }
  }
  return changed ? { ...base, ...changed } : base;
};

/**
 * 从 request:update 负载里取出计时补丁。
 * @returns {{ requestId: string, fields: Object } | null} 没有计时字段时为 null
 */
export const extractTimingPatch = (payload) => {
  const requestId = payload?.request_id;
  if (!requestId) return null;
  const fields = {};
  for (const [eventKey, field] of TIMING_EVENT_FIELDS) {
    if (!isEmpty(payload[eventKey])) {
      fields[field] = payload[eventKey];
    }
  }
  return Object.keys(fields).length > 0 ? { requestId, fields } : null;
};

/**
 * 把计时补丁打到对应行上；行不在当前页或没有变化时返回原数组。
 */
export const applyTimingPatch = (rows, patch) => {
  if (!Array.isArray(rows) || !patch) return rows;
  let changed = false;
  const nextRows = rows.map((row) => {
    if (!row || row.requestId !== patch.requestId) return row;
    const filled = fillTiming(row, patch.fields);
    changed = changed || filled !== row;
    return filled;
  });
  return changed ? nextRows : rows;
};

/**
 * 查询结果到达时，用上一版行补齐查询里还没有的计时（查询可能早于事件发出）。
 * 查询已有的值为准。
 */
export const carryLiveTiming = (prevRows, nextRows) => {
  if (!Array.isArray(prevRows) || prevRows.length === 0 || !Array.isArray(nextRows)) return nextRows;
  const prevById = new Map();
  for (const row of prevRows) {
    if (row?.requestId && !prevById.has(row.requestId)) {
      prevById.set(row.requestId, row);
    }
  }
  return nextRows.map((row) => {
    const prev = row?.requestId ? prevById.get(row.requestId) : null;
    return prev ? fillTiming(row, prev) : row;
  });
};

// 暂存补丁的存活时间：够等过合并刷新与一次查询；行在别的页或被筛掉时永远不会出现，到期丢掉。
export const PENDING_TIMING_PATCH_TTL_MS = 10_000;

const prunePendingTimingPatches = (pending, now) => {
  for (const [requestId, entry] of pending) {
    if (now - entry.storedAt > PENDING_TIMING_PATCH_TTL_MS) {
      pending.delete(requestId);
    }
  }
};

/**
 * 暂存计时补丁，同一请求的多条补丁按行合并规则并在一起。会修改 pending。
 * @param {Map<string, { fields: Object, storedAt: number }>} pending
 */
export const stashTimingPatch = (pending, patch, now) => {
  if (!pending || !patch) return;
  prunePendingTimingPatches(pending, now);
  const existing = pending.get(patch.requestId);
  pending.set(patch.requestId, {
    fields: existing ? fillTiming(existing.fields, patch.fields) : patch.fields,
    storedAt: now
  });
};

/**
 * 把暂存的补丁打到查询结果上，打上的从暂存中移除。会修改 pending。
 * 查询已有的值为准；没有变化时返回原数组。
 */
export const applyPendingTimingPatches = (rows, pending, now) => {
  if (!Array.isArray(rows) || !pending || pending.size === 0) return rows;
  prunePendingTimingPatches(pending, now);
  let changed = false;
  const nextRows = rows.map((row) => {
    const entry = row?.requestId ? pending.get(row.requestId) : null;
    if (!entry) return row;
    pending.delete(row.requestId);
    const filled = fillTiming(row, entry.fields);
    changed = changed || filled !== row;
    return filled;
  });
  return changed ? nextRows : rows;
};
