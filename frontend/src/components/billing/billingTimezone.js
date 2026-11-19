import { timezoneCities } from './timezoneCities.js';

export function timezoneOption(id, instant) {
  let city = timezoneCities[id];
  let offset = '';
  try {
    const formatter = new Intl.DateTimeFormat('en-US', { timeZone: id, timeZoneName: 'shortOffset' });
    const canonical = formatter.resolvedOptions().timeZone;
    city ??= timezoneCities[canonical] ?? (/^Etc\/GMT[+-]/.test(canonical) ? '固定偏移' : undefined);
    const gmt = formatter.formatToParts(instant).find((part) => part.type === 'timeZoneName')?.value || '';
    offset = !gmt ? '' : gmt === 'GMT' ? 'UTC+0' : gmt.replace(/^GMT/, 'UTC');
  } catch (err) {
    // 较旧的 WebView 可能不认识已保存的时区，仍显示原 ID 供用户辨认。
    if (!(err instanceof RangeError)) throw err;
  }
  const label = offset ? `${city || id}（${offset}）` : city || id;
  return { id, label, searchText: `${id} ${label}`.toLowerCase() };
}

// 同时支持空白分词（如「上海 utc+8」）与下划线连写（如「new york」匹配 America/New_York）。
export function matchesTimezoneQuery(option, query) {
  const raw = (query || '').trim().toLowerCase();
  if (!raw) return true;
  const text = option?.searchText || '';
  if (text.includes(raw.replace(/\s+/g, '_'))) return true;
  return raw.split(/\s+/).every((token) => text.includes(token));
}
