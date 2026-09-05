// ============================================
// Account Pool Notice Toast
// 2026-03-07
// ============================================

import { createPortal } from 'react-dom';
import { AlertCircle, CheckCircle2, Info } from 'lucide-react';

const NoticeToast = ({ notice, onClose }) => {
  if (!notice || typeof document === 'undefined') return null;

  return createPortal(
    <div
      className="fixed top-4 right-4 z-[70] pointer-events-none"
      style={{ animation: 'account-pool-toast-slide-in 0.25s ease-out' }}
    >
      <style>{`
        @keyframes account-pool-toast-slide-in {
          from { opacity: 0; transform: translateY(-8px); }
          to { opacity: 1; transform: translateY(0); }
        }
      `}</style>
      {/* tone-* 暗色下是 12% alpha 的半透明色，设计前提是叠在实色卡片上。
          本组件 portal 到 body，底下没有任何托底，直接用会透出页面内容，
          所以这层 bg-surface 是必需的，不是冗余包裹。 */}
      <div className="rounded-lg bg-surface shadow-[0_18px_50px_var(--app-shadow-strong)]">
        <div className={`
          pointer-events-auto flex items-center gap-2 px-4 py-3 rounded-lg text-sm border max-w-md
          ${notice.type === 'success' ? 'tone-emerald' : ''}
          ${notice.type === 'error' ? 'tone-rose' : ''}
          ${notice.type === 'info' ? 'tone-slate' : ''}
        `}>
          {notice.type === 'success'
            ? <CheckCircle2 size={16} className="shrink-0" />
            : notice.type === 'info'
              ? <Info size={16} className="shrink-0" />
              : <AlertCircle size={16} className="shrink-0" />}
          <span className="break-words">{notice.text}</span>
          <button
            type="button"
            onClick={onClose}
            className="ml-auto shrink-0 p-0.5 rounded hover:bg-hairline transition-colors"
          >
            <span className="sr-only">关闭</span>
            <svg width="14" height="14" viewBox="0 0 14 14" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round"><path d="M3.5 3.5l7 7M10.5 3.5l-7 7" /></svg>
          </button>
        </div>
      </div>
    </div>,
    document.body
  );
};

export default NoticeToast;
