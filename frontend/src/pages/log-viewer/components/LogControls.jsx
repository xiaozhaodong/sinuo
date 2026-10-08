// LogControls.jsx - 日志控制按钮组件
// 2026-10-07：放进统一工具栏，改为工具栏按钮；实时 / 暂停状态改在状态栏显示，不再循环闪烁
import {
  Pause,
  Play,
  RefreshCw,
  Trash2,
  Download,
} from 'lucide-react';
import { ToolbarButton } from '@components/layout/ToolbarButton.jsx';

/**
 * 日志控制按钮组
 * @param {boolean} isStreaming 是否正在流式传输
 * @param {boolean} loading 是否加载中
 * @param {number} logsCount 日志数量
 * @param {Function} onToggleStream 切换流状态
 * @param {Function} onRefresh 刷新
 * @param {Function} onClear 清空
 * @param {Function} onExport 导出
 */
function LogControls({
  isStreaming,
  loading,
  logsCount,
  onToggleStream,
  onRefresh,
  onClear,
  onExport,
}) {
  return (
    <div className="flex items-center gap-1">
      <ToolbarButton icon={isStreaming ? Pause : Play} title={isStreaming ? '暂停' : '开始'} onClick={onToggleStream} />
      <ToolbarButton icon={RefreshCw} title="刷新" onClick={onRefresh} loading={loading} />
      <ToolbarButton icon={Trash2} title="清空" onClick={onClear} />
      <ToolbarButton icon={Download} title="导出" onClick={onExport} disabled={logsCount === 0} />
    </div>
  );
}

export default LogControls;
