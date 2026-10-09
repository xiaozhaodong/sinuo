// ============================================
// 隐私规则表格
// 2026-06-11 (v6.1 新增)
// 2026-10-07 桌面化：贴边 desk-table；双击编辑，右键 / 行尾「更多」弹出编辑、复制、调序、删除
// ============================================

import { ArrowDown, ArrowUp, ShieldOff } from 'lucide-react';
import Switch from '@components/desktop/Switch.jsx';
import ListEmpty from '@components/desktop/ListEmpty.jsx';
import RowMoreButton from '@components/desktop/RowMoreButton.jsx';
import useContextMenu from '@components/desktop/useContextMenu.jsx';
import { summarizeScope } from '../utils/privacyRules.js';

const matchTypeLabel = (matchType) => (matchType === 'literal' ? '字面' : '正则');
const actionLabel = (action) => (action === 'detect' ? '仅检测' : '脱敏');

const CompileStatusDot = ({ compileError }) => (
  <span className="inline-flex items-center" title={compileError || '编译正常'}>
    <span className={`inline-block h-[7px] w-[7px] rounded-full ${compileError ? 'bg-danger-solid' : 'bg-success-solid'}`} />
  </span>
);

const MOVE_BUTTON = 'text-fg-subtle hover:text-fg-body disabled:opacity-30';

const PrivacyRulesTable = ({
  rules,
  busy,
  onToggle,
  onEdit,
  onDuplicate,
  onDelete,
  onMove,
  reorderEnabled
}) => {
  const { contextMenu, openContextMenu } = useContextMenu();

  if (!rules.length) {
    return <ListEmpty icon={ShieldOff} title="暂无规则" description="点击工具栏的「导入预设」或 + 开始配置出站隐私保护" />;
  }

  const openRowMenu = (event, rule, index) => openContextMenu(event, [
    { label: '编辑…', disabled: busy, onSelect: () => onEdit(rule) },
    { label: '复制为新规则…', disabled: busy, onSelect: () => onDuplicate(rule) },
    reorderEnabled && '-',
    reorderEnabled && { label: '上移', disabled: busy || index === 0, onSelect: () => onMove(rule, -1) },
    reorderEnabled && { label: '下移', disabled: busy || index === rules.length - 1, onSelect: () => onMove(rule, 1) },
    '-',
    { label: '删除…', danger: true, disabled: busy, onSelect: () => onDelete(rule) }
  ]);

  return (
    <>
      <table className="desk-table">
        <thead>
          <tr>
            <th className="w-[52px] text-center">启用</th>
            <th className="w-[72px]">优先级</th>
            <th>规则名</th>
            <th>类型</th>
            <th>动作</th>
            <th>占位符</th>
            <th>作用域</th>
            <th className="text-center">状态</th>
            <th className="w-[36px]" aria-label="操作" />
          </tr>
        </thead>
        <tbody>
          {rules.map((rule, index) => (
            <tr
              key={rule.id}
              onDoubleClick={() => !busy && onEdit(rule)}
              onContextMenu={(event) => openRowMenu(event, rule, index)}
              className={rule.enabled ? '' : 'text-fg-subtle'}
            >
              <td className="text-center">
                <span className="flex justify-center">
                  <Switch checked={rule.enabled} disabled={busy} label={`${rule.name} 启用`} onChange={(enabled) => onToggle(rule, enabled)} />
                </span>
              </td>
              <td>
                <span className="inline-flex items-center gap-1.5">
                  <span className="tabular-nums text-fg-body">{rule.priority}</span>
                  {reorderEnabled && (
                    <span className="flex flex-col">
                      <button type="button" disabled={busy || index === 0} onClick={() => onMove(rule, -1)} className={MOVE_BUTTON} title="上移" aria-label="上移">
                        <ArrowUp size={11} />
                      </button>
                      <button type="button" disabled={busy || index === rules.length - 1} onClick={() => onMove(rule, 1)} className={MOVE_BUTTON} title="下移" aria-label="下移">
                        <ArrowDown size={11} />
                      </button>
                    </span>
                  )}
                </span>
              </td>
              <td className="max-w-[260px] py-1.5">
                <div className={`truncate font-medium ${rule.enabled ? 'text-fg' : ''}`} title={rule.name}>{rule.name}</div>
                {rule.description && <div className="truncate text-xs text-fg-subtle" title={rule.description}>{rule.description}</div>}
              </td>
              <td className="text-xs text-fg-body">{matchTypeLabel(rule.match_type)}</td>
              <td>
                <span className={`rounded px-1.5 py-0.5 text-[11px] ${rule.action === 'detect' ? 'bg-warn-soft text-warn' : 'bg-accent-soft text-accent'}`}>
                  {actionLabel(rule.action)}
                </span>
              </td>
              <td className="font-mono text-xs text-fg-body">{rule.action === 'redact' ? rule.placeholder : '-'}</td>
              <td className="text-xs text-fg-muted">{summarizeScope(rule.scope)}</td>
              <td className="text-center"><CompileStatusDot compileError={rule.compile_error} /></td>
              <td><RowMoreButton onOpenMenu={(event) => openRowMenu(event, rule, index)} /></td>
            </tr>
          ))}
        </tbody>
      </table>
      {contextMenu}
    </>
  );
};

export default PrivacyRulesTable;
