// ============================================
// 桌面化组件 - 列表空状态
// 2026-10-07
// ============================================
// 放在列表的滚动容器里（容器需有高度）：居中的细线图标 + 一句说明 + 可选的下一步提示。

const ListEmpty = ({ icon: Icon, title, description }) => (
  <div className="flex h-full min-h-[160px] flex-col items-center justify-center gap-2 px-6 text-center text-[13px] text-fg-subtle">
    {Icon && <Icon size={32} strokeWidth={1.25} />}
    <div className="text-fg-body">{title}</div>
    {description && <div className="text-xs">{description}</div>}
  </div>
);

export default ListEmpty;
