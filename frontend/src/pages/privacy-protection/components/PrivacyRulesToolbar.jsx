// ============================================
// 隐私规则筛选工具条
// 2026-06-11 (v6.1 新增)
// 2026-10-07 桌面化：表格上方的范围条，24px 控件
// 2026-11：支持 ⌘F 聚焦搜索与 Esc / 按钮一键清空
// ============================================

import { useRef } from 'react';
import { Search, X } from 'lucide-react';
import { CustomSelect } from '@components/ui';
import useSearchShortcut from '@hooks/useSearchShortcut.js';
import { formatShortcut } from '@utils/platform.js';
import {
  PRIVACY_ACTION_OPTIONS,
  PRIVACY_MATCH_TYPE_OPTIONS
} from '../utils/privacyRules.js';

const ENABLED_OPTIONS = [
  { value: '', label: '全部状态' },
  { value: 'enabled', label: '已启用' },
  { value: 'disabled', label: '已禁用' }
];

const SOURCE_OPTIONS = [
  { value: '', label: '全部来源' },
  { value: 'custom', label: '自定义' },
  { value: 'preset', label: '预设' }
];

const PrivacyRulesToolbar = ({ filters, onChange, total, filtered }) => {
  const searchInputRef = useRef(null);
  const update = (patch) => onChange({ ...filters, ...patch });
  useSearchShortcut(searchInputRef);
  const keyword = filters.keyword || '';

  return (
    <div className="flex shrink-0 flex-wrap items-center gap-1.5 border-b border-line bg-inspector px-3 py-1.5">
      <div className="relative w-56">
        <Search size={13} className="pointer-events-none absolute left-2 top-1/2 -translate-y-1/2 text-fg-subtle" />
        <input
          ref={searchInputRef}
          type="search"
          aria-label="搜索规则"
          value={keyword}
          onChange={(e) => update({ keyword: e.target.value })}
          onKeyDown={(e) => {
            if (e.key === 'Escape' && keyword) {
              e.stopPropagation();
              update({ keyword: '' });
            }
          }}
          placeholder="搜索规则名 / 描述 / pattern"
          title={`搜索规则  ${formatShortcut('F')}`}
          className="desk-field w-full pl-7 pr-6 text-xs placeholder:text-fg-subtle [&::-webkit-search-cancel-button]:hidden"
        />
        {keyword && (
          <button
            type="button"
            onClick={() => update({ keyword: '' })}
            aria-label="清空搜索"
            className="absolute right-1.5 top-1/2 -translate-y-1/2 rounded-full p-0.5 text-fg-subtle hover:text-fg"
          >
            <X size={11} />
          </button>
        )}
      </div>
      <CustomSelect
        size="xs"
        ariaLabel="按状态筛选"
        options={ENABLED_OPTIONS}
        value={filters.enabled || ''}
        onChange={(value) => update({ enabled: value })}
        className="w-24"
      />
      <CustomSelect
        size="xs"
        ariaLabel="按匹配类型筛选"
        options={[{ value: '', label: '全部类型' }, ...PRIVACY_MATCH_TYPE_OPTIONS]}
        value={filters.matchType || ''}
        onChange={(value) => update({ matchType: value })}
        className="w-24"
      />
      <CustomSelect
        size="xs"
        ariaLabel="按动作筛选"
        options={[{ value: '', label: '全部动作' }, ...PRIVACY_ACTION_OPTIONS]}
        value={filters.action || ''}
        onChange={(value) => update({ action: value })}
        className="w-24"
      />
      <CustomSelect
        size="xs"
        ariaLabel="按来源筛选"
        options={SOURCE_OPTIONS}
        value={filters.source || ''}
        onChange={(value) => update({ source: value })}
        className="w-24"
      />
      <span className="ml-auto text-xs tabular-nums text-fg-subtle">
        {filtered} / {total} 条
      </span>
    </div>
  );
};

export default PrivacyRulesToolbar;
