// ============================================
// 隐私规则 Pattern 编辑器
// 2026-06-11 (v6.1 新增)
// ============================================

import SegmentedControl from '@components/desktop/SegmentedControl.jsx';
import { PRIVACY_MATCH_TYPE_OPTIONS } from '../utils/privacyRules.js';

const PrivacyPatternEditor = ({ matchType, pattern, error, onMatchTypeChange, onPatternChange }) => (
  <div className="space-y-2">
    <div className="flex items-center justify-between">
      <span className="text-[13px] font-medium text-fg-body">匹配方式与 Pattern</span>
      <SegmentedControl options={PRIVACY_MATCH_TYPE_OPTIONS} value={matchType} onChange={onMatchTypeChange} ariaLabel="匹配方式与 Pattern" />
    </div>
    <textarea
      value={pattern}
      onChange={(e) => onPatternChange(e.target.value)}
      rows={3}
      spellCheck={false}
      placeholder={matchType === 'regex'
        ? String.raw`Go RE2 正则，例如 sk-(?:proj-)?[A-Za-z0-9_-]{20,}`
        : '普通包含匹配，例如内部项目代号'}
      aria-invalid={Boolean(error)}
      className={`desk-field h-auto w-full py-1.5 font-mono leading-relaxed break-all ${
        error ? 'border-danger-line' : 'border-line'
      }`}
    />
    {error && <span className="text-xs text-danger">{error}</span>}
    {matchType === 'regex' && (
      <p className="text-xs text-fg-subtle">
        使用 Go RE2 语法（不支持回溯/前瞻）。保存时由后端编译校验，失败不会落库。
      </p>
    )}
  </div>
);

export default PrivacyPatternEditor;
