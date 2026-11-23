// LogsPage - 系统日志查看页面（主组件）
// 2026-10-07：控制台式全宽列表；级别、搜索、跟随与流控制都在工具栏，计数在状态栏
// 2026-11：向上滚看历史时自动暂停跟随并浮现「回到底部」按钮，滚回底部自动恢复
import { useState, useMemo, useRef, useEffect, useCallback } from 'react';
import { useWailsLogs } from '@/hooks/useWailsLogs';
import { AlertCircle, ArrowDown, ArrowDownToLine, FileText, RefreshCw } from 'lucide-react';
import { LogEntry, LogControls, LogFilters } from './components';
import { PageToolbar } from '@components/layout/AppToolbar.jsx';
import { ToolbarButton, ToolbarSeparator, ToolbarSpacer } from '@components/layout/ToolbarButton.jsx';
import ToolbarSearch from '@components/layout/ToolbarSearch.jsx';
import { StatusBar, StatusItem, StatusSpacer } from '@components/desktop/StatusBar.jsx';

// 日志没有自带 ID：按对象身份发一个稳定序号做 key。
// 用数组下标的话，日志到上限后每来一条都会整体错位，行内的展开状态会跳到相邻行。
const logKeys = new WeakMap();
let nextLogKey = 0;
const logKeyOf = (log) => {
  let key = logKeys.get(log);
  if (key === undefined) {
    key = nextLogKey++;
    logKeys.set(log, key);
  }
  return key;
};

const SCROLL_PAUSE_THRESHOLD = 36;
const SCROLL_RESUME_THRESHOLD = 16;

// 主页面组件
function LogsPage() {
  // 日志数据和操作
  const {
    logs,
    receivedCount,
    loading,
    error,
    isStreaming,
    start,
    stop,
    clear,
    refresh,
  } = useWailsLogs({
    maxLogs: 1000,
    autoStart: true,
    isActive: true,
  });

  // 本地状态
  const [searchQuery, setSearchQuery] = useState('');
  const [levelFilter, setLevelFilter] = useState('ALL');
  const [autoScroll, setAutoScroll] = useState(true);
  // 历史快照只替换列表，暂停后的未读数始终按实时接收计数计算。
  const [readThroughCount, setReadThroughCount] = useState(0);
  const unseenCount = autoScroll ? 0 : Math.max(0, receivedCount - readThroughCount);
  const scrollContainerRef = useRef(null);
  const logsEndRef = useRef(null);
  const prevReceivedCountRef = useRef(0);
  const prevLoadingRef = useRef(loading);
  const hasShownLogsRef = useRef(false);
  const programmaticScrollRef = useRef(false);
  const pausedByScrollRef = useRef(false);

  // 过滤日志
  const filteredLogs = useMemo(() => {
    let result = logs;

    // 日志级别过滤
    if (levelFilter !== 'ALL') {
      result = result.filter(log => log.level === levelFilter);
    }

    // 搜索过滤
    if (searchQuery) {
      const query = searchQuery.toLowerCase();
      result = result.filter(log =>
        log.message.toLowerCase().includes(query) ||
        (log.attrs && JSON.stringify(log.attrs).toLowerCase().includes(query))
      );
    }

    return result;
  }, [logs, levelFilter, searchQuery]);

  // 统计信息
  const stats = useMemo(() => {
    const counts = { DEBUG: 0, INFO: 0, WARN: 0, ERROR: 0 };
    logs.forEach(log => {
      if (counts[log.level] !== undefined) {
        counts[log.level]++;
      }
    });
    return counts;
  }, [logs]);

  const scrollToBottom = useCallback((behavior = 'smooth') => {
    const container = scrollContainerRef.current;
    if (!container) return;
    programmaticScrollRef.current = true;
    if (logsEndRef.current) {
      logsEndRef.current.scrollIntoView({ behavior, block: 'end' });
    } else {
      container.scrollTop = container.scrollHeight;
    }
    window.setTimeout(() => {
      programmaticScrollRef.current = false;
    }, behavior === 'smooth' ? 280 : 40);
  }, []);

  // 初次显示日志或刷新成功时直接定位；后续实时批次平滑跟随。
  useEffect(() => {
    const refreshed = prevLoadingRef.current && !loading && !error;
    prevLoadingRef.current = loading;
    if (loading) return;

    const hasNewLogs = receivedCount > prevReceivedCountRef.current;
    prevReceivedCountRef.current = receivedCount;
    if (!logsEndRef.current) return;

    const alignToEnd = !hasShownLogsRef.current || refreshed;
    hasShownLogsRef.current = true;
    if (!autoScroll || (!alignToEnd && !hasNewLogs)) return;

    const rafId = requestAnimationFrame(() => scrollToBottom(alignToEnd ? 'auto' : 'smooth'));
    return () => cancelAnimationFrame(rafId);
  }, [logs.length, receivedCount, autoScroll, loading, error, scrollToBottom]);

  // 用户手动上滚看历史时暂停跟随；滚回底部自动恢复
  const handleLogScroll = useCallback(() => {
    if (programmaticScrollRef.current) return;
    const container = scrollContainerRef.current;
    if (!container) return;
    const distanceFromBottom = container.scrollHeight - container.scrollTop - container.clientHeight;

    if (distanceFromBottom > SCROLL_PAUSE_THRESHOLD && autoScroll) {
      pausedByScrollRef.current = true;
      setReadThroughCount(receivedCount);
      setAutoScroll(false);
    } else if (!autoScroll && distanceFromBottom <= SCROLL_RESUME_THRESHOLD) {
      setReadThroughCount(receivedCount);
      if (pausedByScrollRef.current) {
        pausedByScrollRef.current = false;
        setAutoScroll(true);
      }
    }
  }, [autoScroll, receivedCount]);

  const handleToggleAutoScroll = () => {
    pausedByScrollRef.current = false;
    setReadThroughCount(receivedCount);
    setAutoScroll(!autoScroll);
    if (!autoScroll) requestAnimationFrame(() => scrollToBottom('smooth'));
  };

  const handleJumpToBottom = () => {
    pausedByScrollRef.current = false;
    setReadThroughCount(receivedCount);
    setAutoScroll(true);
    scrollToBottom('smooth');
  };

  // 导出日志
  const handleExport = () => {
    const content = filteredLogs
      .map(log => `[${log.timestamp}] [${log.level}] ${log.message}`)
      .join('\n');
    const blob = new Blob([content], { type: 'text/plain' });
    const url = URL.createObjectURL(blob);
    const a = document.createElement('a');
    a.href = url;
    a.download = `logs-${new Date().toISOString()}.txt`;
    a.click();
    URL.revokeObjectURL(url);
  };

  // 清空日志
  const handleClear = () => {
    clear();
    hasShownLogsRef.current = false;
    prevReceivedCountRef.current = 0;
    pausedByScrollRef.current = false;
    setReadThroughCount(0);
  };

  const errorCount = stats.ERROR;
  const warnCount = stats.WARN;

  return (
    <div className="motion-page flex min-h-0 flex-1 flex-col bg-surface">
      <PageToolbar subtitle={`共 ${logs.length} 条${filteredLogs.length !== logs.length ? ` · 筛选后 ${filteredLogs.length} 条` : ''}`}>
        <LogFilters
          stats={stats}
          levelFilter={levelFilter}
          onLevelFilterChange={setLevelFilter}
          totalLogs={logs.length}
        />
        <ToolbarSpacer />
        <ToolbarSearch value={searchQuery} onChange={setSearchQuery} placeholder="过滤日志" width={180} />
        <ToolbarButton icon={ArrowDownToLine} title={autoScroll ? '跟随最新：开' : '跟随最新：关'} pressed={autoScroll} onClick={handleToggleAutoScroll} />
        <ToolbarSeparator />
        <LogControls
          isStreaming={isStreaming}
          loading={loading}
          logsCount={filteredLogs.length}
          onToggleStream={isStreaming ? stop : start}
          onRefresh={refresh}
          onClear={handleClear}
          onExport={handleExport}
        />
      </PageToolbar>

      {/* 已有日志时出错只给一条提示，不把已加载的日志整屏换成错误页 */}
      {error && logs.length > 0 && (
        <div role="alert" className="selectable flex shrink-0 items-center gap-2 border-b border-danger-line bg-danger-soft px-4 py-1.5 text-xs text-danger">
          <AlertCircle size={13} className="shrink-0" />
          {error}
        </div>
      )}

      {/* 日志内容区域 - 可滚动；等宽文本可直接拖选复制 */}
      <div className="relative flex min-h-0 flex-1 flex-col">
        <div ref={scrollContainerRef} onScroll={handleLogScroll} className="min-h-0 flex-1 overflow-y-auto py-1">
          {loading && logs.length === 0 ? (
            <div className="flex h-full items-center justify-center gap-2 text-[13px] text-fg-subtle">
              <RefreshCw size={16} className="animate-spin" />
              加载中…
            </div>
          ) : error && logs.length === 0 ? (
            <div role="alert" className="selectable flex h-full items-center justify-center gap-2 text-[13px] text-danger">
              <AlertCircle size={16} />
              {error}
            </div>
          ) : filteredLogs.length === 0 ? (
            <div className="flex h-full flex-col items-center justify-center gap-2 text-[13px] text-fg-subtle">
              <FileText size={32} strokeWidth={1.25} />
              {searchQuery || levelFilter !== 'ALL' ? '没有符合条件的日志，试试调整级别或关键词' : '暂无日志'}
            </div>
          ) : (
            <>
              {filteredLogs.map((log) => (
                <LogEntry
                  key={logKeyOf(log)}
                  log={log}
                  searchQuery={searchQuery}
                />
              ))}
              <div ref={logsEndRef} className="h-2" />
            </>
          )}
        </div>

        {!autoScroll && filteredLogs.length > 0 && (
          <button
            type="button"
            onClick={handleJumpToBottom}
            className="motion-popover absolute bottom-3 right-4 inline-flex h-7 items-center gap-1.5 rounded-full border border-line bg-surface/95 px-3 text-xs font-medium text-fg shadow-md ring-1 ring-hairline backdrop-blur-md transition-colors hover:bg-surface-sub"
          >
            <ArrowDown size={13} className="text-accent" />
            <span>{unseenCount > 0 ? `${unseenCount} 条新日志 · 回到底部` : '回到底部'}</span>
          </button>
        )}
      </div>

      <StatusBar>
        <StatusItem label="显示" value={`${filteredLogs.length} / ${logs.length}`} />
        <StatusItem label="错误" value={errorCount} valueClassName={errorCount > 0 ? 'text-danger' : 'text-fg-body'} />
        <StatusItem label="警告" value={warnCount} valueClassName={warnCount > 0 ? 'text-warn' : 'text-fg-body'} />
        <StatusSpacer />
        <StatusItem label={isStreaming ? '实时接收中' : '已暂停接收'} />
        <StatusItem label={autoScroll ? '跟随最新' : '已暂停跟随'} />
      </StatusBar>
    </div>
  );
}

export default LogsPage;
