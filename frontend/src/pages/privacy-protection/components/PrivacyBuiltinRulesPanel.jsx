// ============================================
// 内置规则表格（手机号 / 身份证 / 银行卡）
// 2026-10-07 桌面化：贴边 desk-table；双击或行尾按钮编辑
// ============================================

import { Pencil, ShieldCheck } from 'lucide-react';
import Switch from '@components/desktop/Switch.jsx';
import ListEmpty from '@components/desktop/ListEmpty.jsx';
import { summarizeScope } from '../utils/privacyRules.js';

const actionLabel = (action) => (action === 'detect' ? '仅检测' : '脱敏');

const PrivacyBuiltinRulesPanel = ({ rules = [], busy, onToggle, onEdit }) => {
  if (!rules.length) {
    return <ListEmpty icon={ShieldCheck} title="暂无内置规则" description="启动后会自动补齐手机号、身份证和银行卡内置规则" />;
  }

  return (
    <table className="desk-table">
      <thead>
        <tr>
          <th className="w-[52px] text-center">启用</th>
          <th>内置规则</th>
          <th>动作</th>
          <th>占位符</th>
          <th>作用域</th>
          <th className="w-[36px]" aria-label="操作" />
        </tr>
      </thead>
      <tbody>
        {rules.map((rule) => (
          <tr key={rule.id} onDoubleClick={() => !busy && onEdit(rule)} className={rule.enabled ? '' : 'text-fg-subtle'}>
            <td className="text-center">
              <span className="flex justify-center">
                <Switch checked={rule.enabled} disabled={busy} label={`${rule.name} 启用`} onChange={(enabled) => onToggle(rule, enabled)} />
              </span>
            </td>
            <td className="py-1.5">
              <div className={`font-medium ${rule.enabled ? 'text-fg' : ''}`}>{rule.name}</div>
              <div className="font-mono text-xs text-fg-subtle">{rule.pattern}</div>
              {rule.description && <div className="text-xs text-fg-subtle">{rule.description}</div>}
              {rule.compile_error && <div className="break-all text-xs text-danger">{rule.compile_error}</div>}
            </td>
            <td className="text-xs text-fg-body">{actionLabel(rule.action)}</td>
            <td className="font-mono text-xs text-fg-body">{rule.action === 'redact' ? rule.placeholder : '-'}</td>
            <td className="text-xs text-fg-muted">{summarizeScope(rule.scope)}</td>
            <td>
              <button
                type="button"
                disabled={busy}
                onClick={() => onEdit(rule)}
                className="inline-flex h-6 w-6 items-center justify-center rounded text-fg-muted hover:bg-surface-mut hover:text-fg disabled:opacity-40"
                title="编辑"
                aria-label={`编辑 ${rule.name}`}
              >
                <Pencil size={13} />
              </button>
            </td>
          </tr>
        ))}
      </tbody>
    </table>
  );
};

export default PrivacyBuiltinRulesPanel;
