// ============================================
// 桌面化组件 - 开关（macOS 尺寸 32×18）
// 2026-10-07
// ============================================

const Switch = ({ checked = false, onChange, disabled = false, label, title }) => (
  <button
    type="button"
    role="switch"
    aria-checked={checked}
    aria-label={label}
    title={title ?? label}
    disabled={disabled}
    onClick={() => onChange?.(!checked)}
    className={`relative h-[18px] w-8 shrink-0 rounded-full transition-colors disabled:opacity-50 ${
      checked ? 'bg-indigo-600' : 'bg-line-strong'
    }`}
  >
    <span
      className={`absolute left-0.5 top-0.5 h-3.5 w-3.5 rounded-full bg-white shadow-sm transition-transform ${
        checked ? 'translate-x-3.5' : ''
      }`}
    />
  </button>
);

export default Switch;
