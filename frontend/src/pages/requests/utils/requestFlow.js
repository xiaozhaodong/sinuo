export const formatBytesCompact = (bytes) => {
  const n = Number(bytes);
  if (!Number.isFinite(n) || n <= 0) return '';
  if (n < 1024) return `${Math.round(n)} B`;
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KB`;
  return `${(n / (1024 * 1024)).toFixed(2)} MB`;
};

export const summarizeFlowIntegrity = (integrity) => {
  if (!integrity || typeof integrity !== 'object') return [];
  const items = [];
  if (integrity.corrupt_flow_json) items.push('路径 JSON 损坏（已保留原始值）');
  if (integrity.unsupported_schema) items.push('路径版本不受支持');
  if (integrity.truncated) {
    const omitted = [];
    if (integrity.omitted_legs > 0) omitted.push(`${integrity.omitted_legs} 段`);
    if (integrity.omitted_contacts > 0) omitted.push(`${integrity.omitted_contacts} 次联系`);
    items.push(omitted.length > 0 ? `路径已裁剪（省略 ${omitted.join(' / ')}）` : '路径已裁剪');
  }
  if (integrity.write_gap)
    items.push(`写入缺口${integrity.write_gap_code ? ` (${integrity.write_gap_code})` : ''}`);
  if (integrity.record_missing_flow) items.push('未记录逐次路径');
  if (integrity.incomplete_reason) items.push(`未完整收尾: ${integrity.incomplete_reason}`);
  if (
    integrity.diagnostic_message &&
    integrity.diagnostic_code !== 'legacy_unrecorded' &&
    !items.includes(integrity.diagnostic_message)
  ) {
    items.push(integrity.diagnostic_message);
  }
  return items;
};
