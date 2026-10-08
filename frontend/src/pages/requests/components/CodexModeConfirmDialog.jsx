import { useEffect, useId, useRef } from 'react';
import { createPortal } from 'react-dom';
import { AlertCircle, ArrowRight, Loader2, X } from 'lucide-react';
import { Button } from '@components/ui';
import useModalLifecycle from '@hooks/useModalLifecycle.js';
import {
  MODE_EFFECT_NOTICE,
  SUBSCRIPTION_PROXY_MODE_OPTIONS,
  subscriptionProxyStateMeta
} from '@components/codex-access/subscriptionProxyViewModel.js';

const modeLabel = (mode) => SUBSCRIPTION_PROXY_MODE_OPTIONS.find((option) => option.value === mode)?.label || '未知模式';

const CodexModeConfirmDialog = ({ pending, status, busy, stale, result, notice, onClose, onConfirm, onRefresh, returnFocusRef }) => {
  const cancelRef = useRef(null);
  const dialogRef = useRef(null);
  const titleId = useId();
  const descriptionId = useId();
  const close = () => { if (!busy) onClose(); };
  const open = Boolean(pending);
  useModalLifecycle({ open, onClose: close, initialFocusRef: cancelRef });

  useEffect(() => {
    if (open && busy) dialogRef.current?.focus();
  }, [open, busy]);

  // 保存成功后，原模式按钮已禁用；在共用生命周期恢复之后，回到仍可操作的顶部入口。
  useEffect(() => {
    if (!open) return undefined;
    const returnFocusElement = returnFocusRef?.current;
    return () => { if (returnFocusElement && document.contains(returnFocusElement)) returnFocusElement.focus(); };
  }, [open, returnFocusRef]);

  if (!pending) return null;
  const needsRefresh = stale || result?.saved || notice?.action === 'refresh';

  // 共用生命周期负责 Escape、滚动锁和焦点恢复；这里补充 Tab 焦点范围。
  const trapFocus = (event) => {
    if (event.key !== 'Tab') return;
    const controls = Array.from(event.currentTarget.querySelectorAll('button:not(:disabled), [href], [tabindex="0"]'));
    if (controls.length === 0) {
      event.preventDefault();
      dialogRef.current?.focus();
      return;
    }
    const first = controls[0];
    const last = controls[controls.length - 1];
    if (event.shiftKey && (document.activeElement === first || document.activeElement === dialogRef.current)) {
      event.preventDefault();
      last?.focus();
    } else if (!event.shiftKey && document.activeElement === last) {
      event.preventDefault();
      first?.focus();
    }
  };

  return createPortal(
    <div className="fixed inset-0 z-[100] flex items-start justify-center px-4 pt-[18vh]">
      <div aria-hidden="true" className="motion-overlay absolute inset-0 bg-overlay" onMouseDown={close} />
      <section ref={dialogRef} tabIndex={-1} role="dialog" aria-modal="true" aria-busy={busy} aria-labelledby={titleId} aria-describedby={descriptionId} onKeyDown={trapFocus} className="motion-dialog relative max-h-[76vh] w-full max-w-[400px] overflow-y-auto rounded-xl border border-line bg-surface p-5 shadow-2xl ring-1 ring-hairline outline-none">
        <div className="flex items-start justify-between gap-3">
          <AlertCircle size={28} strokeWidth={1.75} className="text-warn" />
          <button type="button" onClick={close} disabled={busy} aria-label="关闭确认弹窗" className="inline-flex h-6 w-6 items-center justify-center rounded text-fg-muted hover:bg-surface-mut disabled:opacity-50"><X size={14} /></button>
        </div>
        <h2 id={titleId} className="mt-3 text-[13px] font-semibold text-fg">确认切换 Codex 接入模式</h2>
        <div className="mt-3 space-y-3">
          <div className="flex items-center gap-3 text-xs">
            <span className="text-fg-muted">{modeLabel(pending.config.mode)}</span>
            <ArrowRight size={16} className="text-fg-subtle" />
            <span className="font-semibold text-accent-fg">{modeLabel(pending.mode)}</span>
          </div>
          <p id={descriptionId} className="selectable text-xs leading-relaxed text-fg-body">全局生效于所有使用此独立入口的 Codex 客户端，仅切换精确匹配的 Responses 请求；原有入口的账号池路由不受影响。</p>
          <div className="space-y-2 text-xs leading-relaxed text-fg-muted">
            <p>{MODE_EFFECT_NOTICE}在途请求保持原行为。</p>
            <p>{pending.mode === 'pool'
              ? 'Responses 发往账号池选中的上游，客户端身份头始终剥离；正文按当前隐私设置处理。账号不可用时按现有池策略处理，不会自动切回订阅。'
              : 'Responses 使用客户端自带的本次凭据直连 ChatGPT Codex 后端。正文按当前隐私设置处理，账号池策略继续供旧入口使用。'}</p>
            <p>当前入口：{subscriptionProxyStateMeta(status?.state).label}；底层 WS 连接数：{status?.activeWebSockets ?? '未知'}（含上下游两侧）。已有 WS 重连后才读取新模式。</p>
          </div>
          {stale && !busy && !result?.saved && <p role="alert" className="rounded-lg border border-warn-line bg-warn-soft px-3 py-2 text-xs text-warn">配置或运行态已变化，请刷新后重新选择模式并确认。</p>}
          {notice && <div role="alert" className={`flex items-start gap-2 rounded-lg border px-3 py-2 text-xs ${notice.type === 'error' ? 'border-danger-line bg-danger-soft text-danger' : 'border-warn-line bg-warn-soft text-warn'}`}><AlertCircle size={14} className="mt-0.5 shrink-0" /><span>{notice.text}</span></div>}
        </div>
        <div className="mt-5 flex justify-end gap-2">
          <Button ref={cancelRef} type="button" disabled={busy} onClick={close} variant="secondary">{result?.saved ? '关闭' : '取消'}</Button>
          {needsRefresh ? (
            <Button type="button" disabled={busy} onClick={onRefresh}>刷新状态</Button>
          ) : (
            <Button type="button" disabled={busy} onClick={onConfirm}>{busy && <Loader2 size={14} className="animate-spin" />}{busy ? '保存并确认状态…' : `确认切换到${modeLabel(pending.mode)}`}</Button>
          )}
        </div>
      </section>
    </div>,
    document.body
  );
};

export default CodexModeConfirmDialog;
