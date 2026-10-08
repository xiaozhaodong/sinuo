// 接入模式单选行（订阅直通 / 端点 / 账号池）：当前项打勾，右侧一句灰字说明
// 2026-10-07 桌面化：替代原来的 ModeCard 卡片
import { CheckSlot } from '@components/desktop/PopoverParts.jsx';

const ModeRow = ({ icon: Icon, label, meta = '', checked = false, disabled = false, onSelect }) => (
  <button
    type="button"
    role="radio"
    aria-checked={checked}
    disabled={disabled}
    onClick={onSelect}
    className="flex h-[26px] w-full items-center gap-1.5 rounded-[5px] pl-1.5 pr-2 text-left hover:bg-surface-mut disabled:opacity-50"
  >
    <CheckSlot checked={checked} />
    {Icon && <Icon size={13} className="shrink-0 text-fg-muted" />}
    <span className={`shrink-0 ${checked ? 'font-medium text-fg' : 'text-fg-body'}`}>{label}</span>
    <span className="min-w-0 flex-1 truncate text-right text-[11.5px] text-fg-subtle" title={meta}>{meta}</span>
  </button>
);

export default ModeRow;
