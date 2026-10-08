// ============================================
// 桌面化组件 - 浮层构件（控制中心模块式）
// 2026-10-07
// ============================================
// 请求页 Claude / Codex 切换器等浮层共用的一套「菜单式」写法：
// 灰色小标题分组、26px 一行、左侧 14px 勾选列标当前项、分隔线隔开各组、底部菜单项。
// 文字都从勾选列之后起（左缩进 26px），所以小标题、说明和行内文字是对齐的。

import { AlertCircle, Check } from 'lucide-react';

// 勾选列：当前项打勾，其它行留空占位保证对齐
export const CheckSlot = ({ checked = false }) => (
  <span className="flex w-3.5 shrink-0 justify-center text-accent" aria-hidden="true">
    {checked && <Check size={13} strokeWidth={2.5} />}
  </span>
);

export const PopoverSection = ({ title, aside = null, sub = false }) => (
  <div className={`flex items-center gap-1.5 whitespace-nowrap pl-[26px] pr-2 text-[11px] text-fg-subtle ${sub ? 'h-5 font-medium' : 'h-6 font-semibold'}`}>
    <span className="shrink-0">{title}</span>
    {aside && <span className="ml-auto flex min-w-0 items-center gap-1.5 font-normal text-fg-muted">{aside}</span>}
  </div>
);

const NOTE_TONES = {
  subtle: 'text-fg-subtle',
  muted: 'text-fg-muted',
  danger: 'text-danger'
};

// nested：展开区内的说明，与缩进的目标行文字对齐
export const PopoverNote = ({ children, tone = 'subtle', nested = false }) => (
  <div className={`pb-1.5 pr-2 pt-px text-[11.5px] leading-relaxed ${nested ? 'pl-[48px]' : 'pl-[26px]'} ${NOTE_TONES[tone] || NOTE_TONES.subtle}`}>{children}</div>
);

export const PopoverSeparator = () => <div className="mx-2 my-[5px] h-px bg-line" role="separator" />;

export const PopoverMenuItem = ({ children, onClick, disabled = false }) => (
  <button
    type="button"
    onClick={onClick}
    disabled={disabled}
    className="flex h-[26px] w-full items-center rounded-[5px] pl-[26px] pr-2 text-left text-fg-body hover:bg-surface-mut disabled:opacity-50"
  >
    {children}
  </button>
);

const ALERT_TONES = {
  warn: 'bg-warn-soft text-warn',
  danger: 'bg-danger-soft text-danger',
  success: 'bg-success-soft text-success'
};

// 浮层内的一条提示：浅色底、无边框，图标对齐勾选列
export const PopoverAlert = ({ tone = 'warn', children, role = 'alert' }) => (
  <div role={role} className={`mb-1 flex gap-1.5 rounded-md px-1.5 py-1.5 text-xs leading-relaxed ${ALERT_TONES[tone] || ALERT_TONES.warn}`}>
    <AlertCircle size={13} className="mt-0.5 shrink-0" />
    <div className="min-w-0 flex-1">{children}</div>
  </div>
);
