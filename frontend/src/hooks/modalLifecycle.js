import { lockAppScroll } from '../utils/scrollLock.js';

const getDocumentTarget = () => (typeof document === 'undefined' ? null : document);

const FOCUSABLE_SELECTOR = 'a[href], button, input:not([type="hidden"]), select, textarea, [tabindex], [contenteditable="true"]';

const getFocusableElements = (container) => Array.from(container.querySelectorAll(FOCUSABLE_SELECTOR))
  .filter((element) => element.tabIndex >= 0 && !element.disabled
    && !element.closest('[inert]') && element.getClientRects().length > 0);

const canRestoreFocus = (documentTarget, element) => Boolean(
  element
  && typeof element.focus === 'function'
  && documentTarget?.contains(element)
);

export const createModalLifecycleManager = () => {
  const stack = [];

  return {
    activate({ getOnClose, initialFocusElement, focusTrapElement }) {
      const documentTarget = getDocumentTarget();
      const previousFocus = documentTarget?.activeElement || null;
      const unlockScroll = lockAppScroll();
      const entry = { getOnClose, initialFocusElement, previousFocus };
      let active = true;

      stack.push(entry);

      const handleKeyDown = (event) => {
        if (stack[stack.length - 1] !== entry) return;
        if (event.key === 'Escape') {
          getOnClose?.()?.();
          return;
        }
        if (event.key !== 'Tab' || !focusTrapElement) return;
        const elements = getFocusableElements(focusTrapElement);
        const index = elements.indexOf(documentTarget?.activeElement);
        // 中间控件沿用浏览器的 Tab 顺序；仅在边界或焦点游离时接管。
        if (index === -1 || (event.shiftKey ? index === 0 : index === elements.length - 1)) {
          event.preventDefault();
          const target = event.shiftKey ? elements[elements.length - 1] : elements[0];
          (target || focusTrapElement).focus();
        }
      };

      const handleFocusIn = (event) => {
        if (stack[stack.length - 1] !== entry || focusTrapElement.contains(event.target)) return;
        (getFocusableElements(focusTrapElement)[0] || focusTrapElement).focus();
      };

      documentTarget?.addEventListener('keydown', handleKeyDown);
      if (focusTrapElement) documentTarget?.addEventListener('focusin', handleFocusIn);
      initialFocusElement?.focus?.();

      return () => {
        if (!active) return;
        active = false;

        documentTarget?.removeEventListener('keydown', handleKeyDown);
        if (focusTrapElement) documentTarget?.removeEventListener('focusin', handleFocusIn);
        const index = stack.indexOf(entry);
        const wasTopmost = index === stack.length - 1;
        const nextEntry = index >= 0 ? stack[index + 1] : null;
        if (nextEntry && !canRestoreFocus(documentTarget, nextEntry.previousFocus)) {
          nextEntry.previousFocus = entry.previousFocus;
        }
        if (index >= 0) stack.splice(index, 1);
        unlockScroll();

        if (wasTopmost && canRestoreFocus(documentTarget, entry.previousFocus)) {
          entry.previousFocus.focus();
        }
      };
    }
  };
};

export const modalLifecycleManager = createModalLifecycleManager();
