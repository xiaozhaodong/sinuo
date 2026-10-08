// ============================================
// 概览页局部错误提示
// 2026-09-09 概览改版：单个卡片失败只降级该卡片，不把整页变成错误页
// ============================================

import { AlertCircle, RefreshCw } from 'lucide-react';
import { resolveErrorMessage } from '@utils/errors.js';

const SectionError = ({ error, onRetry, compact = false }) => {
  if (!error) return null;

  return (
    <div className={`flex flex-col items-center justify-center text-center ${compact ? 'py-6' : 'py-10'}`}>
      <AlertCircle size={compact ? 20 : 28} className="text-danger mb-2" />
      <p className="text-sm text-danger max-w-md">{resolveErrorMessage(error, '加载失败')}</p>
      {onRetry && (
        <button
          type="button"
          onClick={onRetry}
          className="mt-2.5 inline-flex h-6 items-center gap-1.5 rounded-md border border-line bg-surface px-2.5 text-xs font-medium text-fg-body shadow-xs hover:bg-surface-sub"
        >
          <RefreshCw size={12} />
          重试
        </button>
      )}
    </div>
  );
};

export default SectionError;
