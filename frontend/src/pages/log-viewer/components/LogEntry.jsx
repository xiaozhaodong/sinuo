// LogEntry.jsx - 单条日志显示组件
// 2026-10-07：控制台式单行（时间 / 级别 / 消息 / 附加属性四列对齐），错误行淡红底
// 2026-11：支持展开完整附加属性并一键复制单条日志
import { useState } from 'react';
import { ChevronRight, Copy } from 'lucide-react';
import { LOG_LEVELS } from '../constants';
import { splitByKeyword } from '../utils/highlight.js';
import { useTimezone } from '@contexts/TimezoneContext.jsx';
import { copyTextToClipboard } from '@pages/requests/components/clipboard.js';

const MAX_ATTRS = 3;
const MAX_ATTR_LENGTH = 40;

const formatAttrValue = (value) => {
  const text = typeof value === 'object' && value !== null ? JSON.stringify(value) : String(value);
  return text.length > MAX_ATTR_LENGTH ? `${text.slice(0, MAX_ATTR_LENGTH)}…` : text;
};

const formatFullAttrValue = (value) => (
  typeof value === 'object' && value !== null ? JSON.stringify(value, null, 2) : String(value)
);

/**
 * 单条日志组件
 * @param {Object} log 日志对象
 * @param {string} searchQuery 搜索关键词
 */
function LogEntry({ log, searchQuery }) {
  const { formatTimeOnly } = useTimezone();
  const [expanded, setExpanded] = useState(false);
  const levelConfig = LOG_LEVELS[log.level] || LOG_LEVELS.INFO;
  const attrs = log.attrs ? Object.entries(log.attrs) : [];
  const hasAttrs = attrs.length > 0;

  const handleToggle = () => {
    if (!hasAttrs) return;
    // 用户正在拖选日志文字时不误触折叠/展开
    if (window.getSelection()?.toString()) return;
    setExpanded((value) => !value);
  };

  const handleCopy = (event) => {
    event.stopPropagation();
    const attrsText = hasAttrs ? `\n${JSON.stringify(log.attrs, null, 2)}` : '';
    void copyTextToClipboard(`[${log.timestamp}] [${log.level}] ${log.message}${attrsText}`, '日志条目');
  };

  return (
    <div className={log.level === 'ERROR' ? 'bg-danger-soft/60' : ''}>
      <div
        onClick={handleToggle}
        className={`group grid grid-cols-[14px_8ch_5ch_minmax(0,1fr)_auto] items-start gap-x-2.5 px-3 py-px font-mono text-xs leading-5 hover:bg-surface-mut ${hasAttrs ? 'cursor-pointer' : ''}`}
      >
        <span className="flex h-5 items-center justify-center text-fg-subtle">
          {hasAttrs && (
            <button
              type="button"
              aria-expanded={expanded}
              aria-label={expanded ? '收起附加属性' : '展开附加属性'}
              onClick={(event) => {
                event.stopPropagation();
                setExpanded((value) => !value);
              }}
              className="inline-flex h-4 w-4 items-center justify-center rounded text-fg-subtle hover:text-fg"
            >
              <ChevronRight size={12} className={`transition-transform ${expanded ? 'rotate-90' : ''}`} />
            </button>
          )}
        </span>
        <span className="text-fg-subtle">{formatTimeOnly(log.timestamp)}</span>
        <span className={`font-semibold ${levelConfig.color}`}>{log.level}</span>
        <span className="break-all text-fg-body">
          {splitByKeyword(log.message, searchQuery).map((part, index) => (part.match
            ? <mark key={index} className="rounded-sm bg-warn-solid/40 text-fg">{part.text}</mark>
            : part.text))}
        </span>
        <div className="flex items-center gap-1.5">
          {hasAttrs && !expanded && (
            <span className="max-w-[340px] truncate text-[11px] text-fg-subtle" title={attrs.map(([key, value]) => `${key}=${formatFullAttrValue(value)}`).join('  ')}>
              {attrs.slice(0, MAX_ATTRS).map(([key, value]) => `${key}=${formatAttrValue(value)}`).join('  ')}
              {attrs.length > MAX_ATTRS && `  +${attrs.length - MAX_ATTRS}`}
            </span>
          )}
          <button
            type="button"
            onClick={handleCopy}
            title="复制日志条目"
            aria-label="复制日志条目"
            className="inline-flex h-5 w-5 shrink-0 items-center justify-center rounded text-fg-subtle opacity-0 transition-opacity hover:bg-surface hover:text-fg focus-visible:opacity-100 group-hover:opacity-100"
          >
            <Copy size={11} />
          </button>
        </div>
      </div>

      {hasAttrs && expanded && (
        <div className="mx-3 mb-1.5 ml-8 rounded-md border border-line bg-surface-sub/80 px-3 py-2 font-mono text-[11.5px]">
          <dl className="grid grid-cols-[auto_minmax(0,1fr)] gap-x-3 gap-y-1">
            {attrs.map(([key, value]) => (
              <div key={key} className="contents">
                <dt className="text-fg-muted">{key}</dt>
                <dd className="selectable whitespace-pre-wrap break-all text-fg-body">{formatFullAttrValue(value)}</dd>
              </div>
            ))}
          </dl>
        </div>
      )}
    </div>
  );
}

export default LogEntry;
