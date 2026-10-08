// 请求页 Claude / Codex 切换器的触发按钮
// 2026-10-07 桌面化：26px 弹出按钮，灰色品牌名 + 加粗取值，宽度随内容（窄工具栏最多 180px，宽时 236px），
//            不再定宽留白；状态点、额度角标与上下箭头同原型
import { ChevronsUpDown, Loader2 } from 'lucide-react';

const SwitcherTrigger = ({
  brand,
  label,
  warn = false,
  busy = false,
  badge = null,
  open = false,
  triggerRef,
  menuId,
  ariaLabel,
  title,
  onClick
}) => (
  <button
    ref={triggerRef}
    type="button"
    aria-haspopup="dialog"
    aria-expanded={open}
    aria-controls={menuId}
    aria-label={ariaLabel || `${brand}：${label}`}
    title={title}
    onClick={onClick}
    className={`inline-flex h-[26px] max-w-[180px] shrink-0 items-center gap-1.5 rounded-md border border-line pl-2 pr-1 text-[12.5px] shadow-xs transition-colors @4xl:max-w-[236px] ${open ? 'bg-surface-mut' : 'bg-surface hover:bg-surface-sub'}`}
  >
    {busy
      ? <Loader2 size={12} className="shrink-0 animate-spin text-accent" />
      : <span className={`h-[7px] w-[7px] shrink-0 rounded-full ${warn ? 'bg-warn-solid' : 'bg-success-solid'}`} />}
    <span className="shrink-0 text-fg-muted">{brand}</span>
    <span className={`min-w-0 truncate font-medium ${warn ? 'text-warn' : 'text-fg'}`}>{label}</span>
    {badge && <span className={`shrink-0 font-mono text-[11px] ${badge.toneClass}`}>{badge.text}</span>}
    <ChevronsUpDown size={12} className="shrink-0 text-fg-subtle" />
  </button>
);

export default SwitcherTrigger;
