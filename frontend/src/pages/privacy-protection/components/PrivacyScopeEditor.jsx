// ============================================
// 隐私规则作用域编辑器
// 2026-06-11 (v6.1 新增)
// scope JSON 由本组件结构化生成，不让用户手写 JSON
// ============================================

import {
  PRIVACY_PATH_OPTIONS,
  PRIVACY_PROVIDER_TYPE_OPTIONS,
  PRIVACY_UPSTREAM_TYPE_OPTIONS
} from '../utils/privacyRules.js';

const CheckboxGroup = ({ label, hint, options, values, onChange }) => {
  const toggle = (value) => {
    const set = new Set(values);
    if (set.has(value)) {
      set.delete(value);
    } else {
      set.add(value);
    }
    onChange([...set]);
  };

  return (
    <div>
      <div className="flex items-baseline gap-2 mb-1.5">
        <span className="text-[13px] font-medium text-fg-body">{label}</span>
        {hint && <span className="text-xs text-fg-subtle">{hint}</span>}
      </div>
      <div className="flex flex-wrap gap-1.5">
        {options.map((opt) => {
          const checked = values.includes(opt.value);
          return (
            <button
              key={String(opt.value)}
              type="button"
              aria-pressed={checked}
              onClick={() => toggle(opt.value)}
              className={`px-2 py-1 rounded text-xs border transition-colors break-all ${
                checked
                  ? 'bg-accent-soft border-accent-line text-accent-fg'
                  : 'bg-surface border-line text-fg-muted hover:border-line-strong'
              }`}
            >
              {opt.label}
            </button>
          );
        })}
        {options.length === 0 && (
          <span className="text-xs text-fg-subtle">暂无可选项</span>
        )}
      </div>
    </div>
  );
};

const PrivacyScopeEditor = ({ scope, onChange, endpointOptions = [], accountOptions = [] }) => {
  const update = (key, values) => onChange({ ...scope, [key]: values });

  return (
    <div className="space-y-3">
      <p className="text-xs text-fg-subtle">
        空选项表示不限。多个维度之间是 AND，同一维度内是 OR；路径为精确匹配。
      </p>
      <CheckboxGroup
        label="请求路径"
        options={PRIVACY_PATH_OPTIONS.map((path) => ({ value: path, label: path }))}
        values={scope.paths || []}
        onChange={(values) => update('paths', values)}
      />
      <CheckboxGroup
        label="上游类型"
        options={PRIVACY_UPSTREAM_TYPE_OPTIONS}
        values={scope.upstream_types || []}
        onChange={(values) => update('upstream_types', values)}
      />
      <CheckboxGroup
        label="Claude 端点"
        hint="仅对 Claude Messages 链路生效"
        options={endpointOptions}
        values={scope.endpoint_names || []}
        onChange={(values) => update('endpoint_names', values)}
      />
      <CheckboxGroup
        label="账号"
        hint="仅对 Codex 账号池链路生效"
        options={accountOptions}
        values={scope.account_ids || []}
        onChange={(values) => update('account_ids', values)}
      />
      <CheckboxGroup
        label="账号类型"
        options={PRIVACY_PROVIDER_TYPE_OPTIONS}
        values={scope.provider_types || []}
        onChange={(values) => update('provider_types', values)}
      />
    </div>
  );
};

export default PrivacyScopeEditor;
