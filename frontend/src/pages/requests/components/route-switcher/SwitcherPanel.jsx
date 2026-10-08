// 请求页 Claude / Codex 切换器的浮层外壳
// 2026-10-07 桌面化：去掉标题栏与页签，改为控制中心模块式的一列（半透明底 + 模糊），
//            内容由 PopoverParts 的小标题 / 行 / 分隔线组成
import { createPortal } from 'react-dom';

const SwitcherPanel = ({ open, position, panelRef, panelId, title, children }) => {
  if (!open || !position) return null;

  return createPortal(
    <div
      ref={panelRef}
      id={panelId}
      role="dialog"
      aria-label={title}
      style={position}
      className="fixed z-50 overflow-y-auto rounded-[10px] bg-surface/95 p-[5px] text-[13px] text-fg shadow-xl ring-1 ring-hairline backdrop-blur-xl motion-popover origin-top-left"
    >
      {children}
    </div>,
    document.body
  );
};

export default SwitcherPanel;
