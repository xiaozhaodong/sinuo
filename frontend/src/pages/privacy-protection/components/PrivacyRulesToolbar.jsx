// ============================================
// 隐私规则筛选工具条
// 2026-06-11 (v6.1 新增)
// 2026-10-07 桌面化：表格上方的范围条，24px 控件
// ============================================

import { Search } from 'lucide-react';
import { CustomSelect } from '@components/ui';
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
  const update = (patch) => onChange({ ...filters, ...patch });

  return (
    <div className="flex shrink-0 flex-wrap items-center gap-1.5 border-b border-line bg-inspector px-3 py-1.5">
      <div className="relative w-56">
        <Search size={13} className="pointer-events-none absolute left-2 top-1/2 -translate-y-1/2 text-fg-subtle" />
        <input
          aria-label="搜索规则"
          value={filters.keyword || ''}
          onChange={(e) => update({ keyword: e.target.value })}
          placeholder="搜索规则名 / 描述 / pattern"
          className="desk-field w-full pl-7 text-xs placeholder:text-fg-subtle"
        />
      </div>
      <CustomSelect
        size="xs"
        options={ENABLED_OPTIONS}
        value={filters.enabled || ''}
        onChange={(value) => update({ enabled: value })}
        className="w-24"
      />
      <CustomSelect
        size="xs"
        options={[{ value: '', label: '全部类型' }, ...PRIVACY_MATCH_TYPE_OPTIONS]}
        value={filters.matchType || ''}
        onChange={(value) => update({ matchType: value })}
        className="w-24"
      />
      <CustomSelect
        size="xs"
        options={[{ value: '', label: '全部动作' }, ...PRIVACY_ACTION_OPTIONS]}
        value={filters.action || ''}
        onChange={(value) => update({ action: value })}
        className="w-24"
      />
      <CustomSelect
        size="xs"
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
