// LogEntry.jsx - 单条日志显示组件
// 2026-10-07：控制台式单行（时间 / 级别 / 消息 / 附加属性四列对齐），错误行淡红底
import { LOG_LEVELS } from '../constants';
import { splitByKeyword } from '../utils/highlight.js';
import { useTimezone } from '@contexts/TimezoneContext.jsx';

const MAX_ATTRS = 3;
const MAX_ATTR_LENGTH = 40;

const formatAttrValue = (value) => {
  const text = String(value);
  return text.length > MAX_ATTR_LENGTH ? `${text.slice(0, MAX_ATTR_LENGTH)}…` : text;
};

/**
 * 单条日志组件
 * @param {Object} log 日志对象
 * @param {string} searchQuery 搜索关键词
 */
function LogEntry({ log, searchQuery }) {
  const { formatTimeOnly } = useTimezone();
  const levelConfig = LOG_LEVELS[log.level] || LOG_LEVELS.INFO;
  const attrs = log.attrs ? Object.entries(log.attrs) : [];

  return (
    <div className={`grid grid-cols-[104px_52px_minmax(0,1fr)_auto] gap-x-3 px-4 py-px font-mono text-xs leading-5 hover:bg-surface-mut ${log.level === 'ERROR' ? 'bg-danger-soft/60' : ''}`}>
      <span className="text-fg-subtle">{formatTimeOnly(log.timestamp)}</span>
      <span className={`font-semibold ${levelConfig.color}`}>{log.level}</span>
      <span className="break-all text-fg-body">
        {splitByKeyword(log.message, searchQuery).map((part, index) => (part.match
          ? <mark key={index} className="rounded-sm bg-warn-solid/40 text-fg">{part.text}</mark>
          : part.text))}
      </span>
      {attrs.length > 0 ? (
        <span className="max-w-[360px] truncate text-[11px] text-fg-subtle" title={attrs.map(([key, value]) => `${key}=${value}`).join('  ')}>
          {attrs.slice(0, MAX_ATTRS).map(([key, value]) => `${key}=${formatAttrValue(value)}`).join('  ')}
          {attrs.length > MAX_ATTRS && `  +${attrs.length - MAX_ATTRS}`}
        </span>
      ) : <span />}
    </div>
  );
}

export default LogEntry;
