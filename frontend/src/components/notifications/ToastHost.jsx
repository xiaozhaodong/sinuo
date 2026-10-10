// 桌面化（2026-10-10）——中性浮层与 ContextMenu 同一家族，状态只由图标着色；页面操作反馈经 useNotice 投递到这里。
import { createPortal } from 'react-dom';
import {
  AlertCircle,
  AlertTriangle,
  ArrowRight,
  CheckCircle2,
  Info,
  X
} from 'lucide-react';

const VARIANT_STYLES = {
  info: { icon: 'text-fg-muted', Icon: Info },
  success: { icon: 'text-success-solid', Icon: CheckCircle2 },
  warning: { icon: 'text-warn-solid', Icon: AlertTriangle },
  error: { icon: 'text-danger-solid', Icon: AlertCircle }
};

const laneLabel = (lane) => (lane === 'codex' ? 'Codex 账号' : 'Claude 端点');

const ToastHost = ({ toasts = [], pendingCount = 0, onDismiss }) => {
  if (typeof document === 'undefined' || (toasts.length === 0 && pendingCount === 0)) return null;

  // 顶部 60px = 52px 窗口工具栏 + 8px 间距，避免覆盖工具栏。
  return createPortal(
    <div
      className="pointer-events-none fixed right-3 top-[60px] z-[120] flex w-[min(100vw-1.5rem,20rem)] flex-col gap-2"
      aria-live="polite"
      aria-label="全局通知"
    >
      {toasts.map((toast) => {
        const variant = VARIANT_STYLES[toast.level] || VARIANT_STYLES.info;
        const Icon = variant.Icon;
        const hasRoute = toast.kind === 'failover' && toast.from && toast.to;
        return (
          <article
            key={toast.id}
            data-testid="global-toast"
            role={toast.level === 'error' || toast.kind === 'failover' ? 'alert' : 'status'}
            className="group motion-toast pointer-events-auto relative flex gap-2.5 rounded-lg border border-line bg-surface/95 py-2.5 pl-3 pr-8 shadow-lg ring-1 ring-hairline backdrop-blur-xl dark:bg-surface-sub/95"
          >
            <Icon size={15} strokeWidth={2} className={`mt-[1.5px] shrink-0 ${variant.icon}`} />
            <div className="min-w-0 flex-1">
              {toast.title && (
                <p className="text-[13px] font-semibold leading-[18px] text-fg">{toast.title}</p>
              )}
              {hasRoute ? (
                <>
                  <div className="mt-0.5 flex min-w-0 items-center gap-1 text-xs leading-[17px] text-fg-body">
                    <span className="shrink-0">{laneLabel(toast.lane)} ·</span>
                    <span className="min-w-0 truncate font-medium text-fg" title={toast.from}>{toast.from}</span>
                    <ArrowRight size={11} className="shrink-0 text-fg-subtle" />
                    <span className="min-w-0 truncate font-medium text-fg" title={toast.to}>{toast.to}</span>
                  </div>
                  <p className="mt-0.5 break-words text-xs leading-[17px] text-fg-muted">
                    {toast.reasonLabel ? `原因：${toast.reasonLabel}` : toast.message}
                  </p>
                </>
              ) : (
                <p className={toast.title
                  ? 'mt-0.5 break-words text-xs leading-[17px] text-fg-muted'
                  : 'break-words text-[12.5px] leading-[18px] text-fg-body'}
                >
                  {toast.message}
                </p>
              )}
            </div>
            <button
              type="button"
              className="absolute right-1.5 top-1.5 flex h-[18px] w-[18px] items-center justify-center rounded text-fg-subtle opacity-0 transition-opacity hover:bg-hairline hover:text-fg-body focus-visible:opacity-100 group-hover:opacity-100 group-focus-within:opacity-100"
              onClick={() => onDismiss?.(toast.id)}
              aria-label="关闭通知"
            >
              <X size={12} />
            </button>
          </article>
        );
      })}
      {pendingCount > 0 && (
        <div
          className="pointer-events-auto self-end rounded border border-line bg-surface/95 px-1.5 py-0.5 text-[11px] text-fg-muted shadow-sm backdrop-blur-xl dark:bg-surface-sub/95"
          role="status"
        >
          还有 {pendingCount} 条通知排队
        </div>
      )}
    </div>,
    document.body
  );
};

export default ToastHost;
