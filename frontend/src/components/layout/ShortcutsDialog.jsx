// ============================================
// 快捷键速查面板（⌘/ 或 Ctrl+/ 打开）
// ============================================

import { Keyboard, X } from 'lucide-react';
import { AlertDialog } from '@components/ui/ConfirmDialog.jsx';
import { formatAltShortcut, formatShortcut } from '@utils/platform.js';

const KeyBadge = ({ children }) => (
  <kbd className="inline-flex h-5 min-w-[22px] items-center justify-center rounded border border-line bg-surface-sub px-1.5 font-mono text-[11px] font-medium text-fg-body shadow-2xs">
    {children}
  </kbd>
);

const ShortcutsDialog = ({ open, onClose }) => {
  if (!open) return null;

  const groups = [
    {
      title: '全局导航与窗口',
      items: [
        { label: '切换主页面（概览 → 基础定价）', keys: `${formatShortcut('1')} – ${formatShortcut('7')}` },
        { label: '打开系统设置', keys: formatShortcut(',') },
        { label: '显示 / 隐藏左侧边栏', keys: formatAltShortcut('S') },
        { label: '显示 / 隐藏右侧详情面板', keys: formatAltShortcut('I') },
        { label: '打开 / 关闭快捷键速查', keys: formatShortcut('/') }
      ]
    },
    {
      title: '列表、搜索与筛选',
      items: [
        { label: '聚焦当前页面搜索框', keys: formatShortcut('F') },
        { label: '清空搜索 / 关闭菜单或弹窗', keys: 'Esc' },
        { label: '列表上下切换选中行', keys: '↑ / ↓' },
        { label: '跳至列表首行 / 末行', keys: 'Home / End' },
        { label: '复制选中请求的 Request ID', keys: formatShortcut('C') }
      ]
    },
    {
      title: '表单与分页控件',
      items: [
        { label: '保存系统设置更改（设置页）', keys: formatShortcut('S') },
        { label: '子页签 / 分段控件左右切换', keys: '← / →' },
        { label: '拖拽面板分隔条并双击复位尺寸', keys: '双击分隔线' }
      ]
    }
  ];

  return (
    <AlertDialog open={open} role="dialog" title="快捷键速查" width={460} onClose={onClose}>
      <div data-shortcuts-dialog="true">
        <div className="flex items-center justify-between gap-2 pb-3 border-b border-line">
          <div className="flex items-center gap-2">
            <Keyboard size={16} strokeWidth={1.75} className="text-accent" />
            <h3 className="text-[13px] font-semibold text-fg">快捷键速查</h3>
          </div>
          <button
            type="button"
            onClick={onClose}
            aria-label="关闭快捷键速查"
            className="inline-flex h-6 w-6 items-center justify-center rounded text-fg-muted transition-colors hover:bg-surface-mut hover:text-fg"
          >
            <X size={14} />
          </button>
        </div>

        <div className="mt-3 space-y-4">
          {groups.map((group) => (
            <section key={group.title}>
              <h4 className="mb-1.5 text-[11px] font-semibold text-fg-subtle">{group.title}</h4>
              <div className="divide-y divide-line-soft rounded-lg border border-line bg-surface-sub/40">
                {group.items.map((item) => (
                  <div key={item.label} className="flex items-center justify-between gap-3 px-3 py-1.5 text-xs">
                    <span className="text-fg-body">{item.label}</span>
                    <KeyBadge>{item.keys}</KeyBadge>
                  </div>
                ))}
              </div>
            </section>
          ))}
        </div>
      </div>
    </AlertDialog>
  );
};

export default ShortcutsDialog;
