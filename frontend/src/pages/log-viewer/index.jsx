// LogsPage - 系统日志查看页面（主组件）
// 2026-10-07：控制台式全宽列表；级别、搜索、跟随与流控制都在工具栏，计数在状态栏
import { useState, useMemo, useRef, useEffect } from 'react';
import { useWailsLogs } from '@/hooks/useWailsLogs';
import { AlertCircle, ArrowDownToLine, FileText, RefreshCw } from 'lucide-react';
import { LogEntry, LogControls, LogFilters } from './components';
import { PageToolbar } from '@components/layout/AppToolbar.jsx';
import { ToolbarButton, ToolbarSeparator, ToolbarSpacer } from '@components/layout/ToolbarButton.jsx';
import ToolbarSearch from '@components/layout/ToolbarSearch.jsx';
import { StatusBar, StatusItem, StatusSpacer } from '@components/desktop/StatusBar.jsx';

// 主页面组件
function LogsPage() {
  // 日志数据和操作
  const {
    logs,
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
  const logsEndRef = useRef(null);
  const prevLogsLengthRef = useRef(0);
  const hasInitialScrollDoneRef = useRef(false);

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

  // 自动滚动逻辑
  useEffect(() => {
    if (!hasInitialScrollDoneRef.current && !loading && logs.length > 0 && logsEndRef.current) {
      hasInitialScrollDoneRef.current = true;
      prevLogsLengthRef.current = logs.length;
      // 首次加载完成后定位到底部，确保显示最新日志
      const rafId = requestAnimationFrame(() => {
        logsEndRef.current?.scrollIntoView({ behavior: 'auto', block: 'end' });
      });
      return () => cancelAnimationFrame(rafId);
    }

    const hasNewLogs = logs.length > prevLogsLengthRef.current;
    if (autoScroll && hasNewLogs && !loading && logsEndRef.current) {
      const rafId = requestAnimationFrame(() => {
        logsEndRef.current?.scrollIntoView({ behavior: 'smooth', block: 'end' });
      });
      prevLogsLengthRef.current = logs.length;
      return () => cancelAnimationFrame(rafId);
    }

    prevLogsLengthRef.current = logs.length;
  }, [logs.length, autoScroll, loading]);

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
    hasInitialScrollDoneRef.current = false;
    prevLogsLengthRef.current = 0;
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
        <ToolbarButton icon={ArrowDownToLine} title={autoScroll ? '跟随最新：开' : '跟随最新：关'} pressed={autoScroll} onClick={() => setAutoScroll((value) => !value)} />
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
      <div className="min-h-0 flex-1 overflow-y-auto py-1">
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
            {filteredLogs.map((log, index) => (
              <LogEntry
                key={`${log.timestamp}-${index}`}
                log={log}
                searchQuery={searchQuery}
              />
            ))}
            <div ref={logsEndRef} className="h-2" />
          </>
        )}
      </div>

      <StatusBar>
        <StatusItem label="显示" value={`${filteredLogs.length} / ${logs.length}`} />
        <StatusItem label="错误" value={errorCount} valueClassName={errorCount > 0 ? 'text-danger' : 'text-fg-body'} />
        <StatusItem label="警告" value={warnCount} valueClassName={warnCount > 0 ? 'text-warn' : 'text-fg-body'} />
        <StatusSpacer />
        <StatusItem label={isStreaming ? '实时接收中' : '已暂停接收'} />
        <StatusItem label={autoScroll ? '跟随最新' : '已停止跟随'} />
      </StatusBar>
    </div>
  );
}

export default LogsPage;
